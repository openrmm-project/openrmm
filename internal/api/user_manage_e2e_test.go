package api

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// An unscoped custom role with users.manage used to skip the permission check
// on the target entirely, so it could change an administrator's email, reset
// their second factor and take the account over.
func TestUnscopedUserManagerCannotActOnStrongerAccountsIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	if _, err := e.store.CreateRole(e.ctx, &models.Role{Key: "usermgr", Name: "User manager",
		Permissions: []string{models.PermUsersManage, models.PermUsersEmailManage}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateRole(e.ctx, &models.Role{Key: "mailonly", Name: "Mail only",
		Permissions: []string{models.PermUsersEmailManage}}); err != nil {
		t.Fatal(err)
	}
	mgr := e.session("usermgr", nil)
	admin, err := e.store.CreateUser(e.ctx, "admin-"+uuid.NewString()[:8], "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	for _, attempt := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/users/" + admin.ID + "/email", `{"email":"attacker@example.test"}`},
		{http.MethodPost, "/api/users/" + admin.ID + "/email/verify", ""},
		{http.MethodPost, "/api/users/" + admin.ID + "/2fa/reset", ""},
		{http.MethodPatch, "/api/users/" + admin.ID, `{"role":"read_only"}`},
		{http.MethodPut, "/api/users/" + admin.ID + "/scopes", `{"scopes":[]}`},
		{http.MethodPost, "/api/users/" + admin.ID + "/disable", ""},
		{http.MethodDelete, "/api/users/" + admin.ID, ""},
	} {
		if code, _ := e.do(attempt.method, attempt.path, attempt.body, mgr); code != http.StatusForbidden {
			t.Errorf("%s %s by a weaker unscoped manager: HTTP %d, want 403", attempt.method, attempt.path, code)
		}
	}
	after, err := e.store.GetUserByID(e.ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Role != models.RoleAdmin || after.Email != "" || after.DisabledAt != nil {
		t.Fatalf("administrator changed by a weaker manager: role=%s email=%q disabled=%v",
			after.Role, after.Email, after.DisabledAt != nil)
	}

	// Control: the same manager still manages an account whose permissions it holds.
	weaker, err := e.store.CreateUser(e.ctx, "mail-"+uuid.NewString()[:8], "hash", "mailonly")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := e.do(http.MethodPut, "/api/users/"+weaker.ID+"/email", `{"email":"helpdesk@example.test"}`, mgr); code >= 300 {
		t.Fatalf("manager setting a weaker account's email: HTTP %d %v", code, body)
	}
	// And an administrator still manages everyone.
	if code, body := e.do(http.MethodPost, "/api/users/"+admin.ID+"/disable", "", e.admin); code >= 300 {
		t.Fatalf("administrator disabling another administrator: HTTP %d %v", code, body)
	}
}
