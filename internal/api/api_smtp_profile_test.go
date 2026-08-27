package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/settings"
)

// Which transport a request writes is decided at route registration, not by
// inspecting the path — the earlier implementation derived it from the path, and
// a trailing slash was enough to make /config/smtp/alerts fall through to the
// account profile, which on a save would overwrite password-reset delivery.
//
// That the two never share storage is pinned here (distinct key prefixes) and,
// end to end, by TestSaveSMTPAlertsDoesNotTouchAccountKeysIntegration in
// internal/settings.

// The profile constants must stay distinct and correctly prefixed: they are
// concatenated with field names to form settings keys, so a collision would make
// one transport read and write the other's rows.
func TestSMTPProfilePrefixesAreDistinct(t *testing.T) {
	if settings.ProfileAccount == settings.ProfileAlerts {
		t.Fatal("the two profiles must not share a key prefix")
	}
	accountKeys := settings.SMTPKeys(settings.ProfileAccount)
	for _, alertKey := range settings.SMTPKeys(settings.ProfileAlerts) {
		for _, accountKey := range accountKeys {
			if alertKey == accountKey {
				t.Errorf("key %q belongs to both profiles", alertKey)
			}
		}
		if !strings.HasPrefix(alertKey, settings.ProfileAccount) {
			// The alerts prefix nests under the account one by design
			// (smtp.alerts.*); what matters is that no key is shared.
			t.Errorf("unexpected alerts key shape: %q", alertKey)
		}
	}
}

// requirePermission is what stands between users.manage and an account takeover
// via the reset address. A route table that lost the second middleware would
// otherwise pass every test in this package.
func TestRequirePermissionRejectsWithoutTheEmailPermission(t *testing.T) {
	s := &Server{}
	var reached bool
	handler := s.requirePermission(models.PermUsersEmailManage)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	withPerms := func(perms ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "/api/users/x/email", nil)
		set := map[string]bool{}
		for _, p := range perms {
			set[p] = true
		}
		return r.WithContext(context.WithValue(r.Context(), permsKey, set))
	}

	// users.manage alone is deliberately not enough.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, withPerms(models.PermUsersManage))
	if reached {
		t.Error("a caller with only users.manage reached the address handler")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}

	reached = false
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, withPerms(models.PermUsersManage, models.PermUsersEmailManage))
	if !reached {
		t.Errorf("a caller holding both permissions was rejected with %d", w.Code)
	}
}

// The admin role must actually carry the permission, or the route above is
// unreachable for everyone and address management silently breaks.
func TestAdminHoldsEmailPermissionAndOthersDoNot(t *testing.T) {
	holds := func(role, permission string) bool {
		for _, p := range models.DefaultRolePermissions(role) {
			if p == permission {
				return true
			}
		}
		return false
	}
	if !holds(models.RoleAdmin, models.PermUsersEmailManage) {
		t.Error("admin should hold users.email.manage")
	}
	for _, role := range []string{models.RoleTech, models.RoleReadOnly} {
		if holds(role, models.PermUsersEmailManage) {
			t.Errorf("%s should not hold users.email.manage", role)
		}
		if holds(role, models.PermUsersManage) {
			t.Errorf("%s should not hold users.manage", role)
		}
	}
	if !models.ValidPermission(models.PermUsersEmailManage) {
		t.Error("the permission must be in the catalog, or no role screen can grant it")
	}
}
