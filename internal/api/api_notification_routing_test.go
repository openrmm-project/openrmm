package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
)

func TestNotificationRoutingScopeAndPrivacyE2E(t *testing.T) {
	f := newBulkFixture(t)
	recipient := func(name, scope, id string) *models.User {
		t.Helper()
		var scopes []models.UserScope
		if scope != "" {
			scopes = []models.UserScope{{ScopeType: scope, ScopeID: id}}
		}
		u, err := f.st.CreateUserWithScopes(f.ctx, name, "hash", models.RoleTech, scopes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.UpdateUserEmail(f.ctx, u.ID, name+"@example.test"); err != nil {
			t.Fatal(err)
		}
		return u
	}
	own := recipient("own", "site", f.siteA1.ID)
	sibling := recipient("sibling-private", "site", f.siteA2.ID)
	outside := recipient("outside-private", "site", f.siteB.ID)
	clientPath := "/api/clients/" + f.siteA1.ClientID + "/notification-routing"
	sitePath := "/api/sites/" + f.siteA1.ID + "/notification-routing"
	body := map[string]any{"expected_version": 0, "mode": "selected", "user_ids": []string{own.ID, sibling.ID}}
	if r := f.do(f.admin, http.MethodPut, clientPath, body); r.Code != 200 {
		t.Fatalf("client save=%d %s", r.Code, r.Body)
	}
	user, err := f.st.CreateUserWithScopes(f.ctx, "site-manager", "hash", models.RoleAdmin, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: f.siteA1.ID}})
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err = f.st.CreateSession(f.ctx, user.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sitePath, sitePath + "/recipients"} {
		r := f.do(token, http.MethodGet, path, nil)
		if r.Code != 200 {
			t.Fatalf("read %s=%d %s", path, r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), sibling.ID) || strings.Contains(r.Body.String(), sibling.Username) || strings.Contains(r.Body.String(), outside.ID) {
			t.Fatalf("private candidate leaked: %s", r.Body)
		}
	}
	for _, test := range []struct{ token, path string }{{token, clientPath}, {token, "/api/sites/" + f.siteB.ID + "/notification-routing"}, {f.tech, sitePath}} {
		if r := f.do(test.token, http.MethodGet, test.path, nil); r.Code != 403 {
			t.Fatalf("unauthorized read=%d %s", r.Code, r.Body)
		}
	}
	body["user_ids"] = []string{outside.ID}
	if r := f.do(token, http.MethodPut, sitePath, body); r.Code != 400 {
		t.Fatalf("outscope selection=%d %s", r.Code, r.Body)
	}
	body["user_ids"] = []string{own.ID, own.ID}
	r := f.do(token, http.MethodPut, sitePath, body)
	if r.Code != 200 {
		t.Fatalf("site save=%d %s", r.Code, r.Body)
	}
	var got models.NotificationRoutingView
	if err = json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Local.UserIDs) != 1 || got.Local.Version != 1 {
		t.Fatalf("dedupe=%+v", got)
	}
	if r = f.do(token, http.MethodPut, sitePath, body); r.Code != 409 {
		t.Fatalf("lost update accepted=%d %s", r.Code, r.Body)
	}
	body = map[string]any{"expected_version": 1, "mode": "inherit", "user_ids": []string{}}
	if r = f.do(token, http.MethodPut, sitePath, body); r.Code != 200 {
		t.Fatalf("inherit=%d %s", r.Code, r.Body)
	}
	if r = f.do(token, http.MethodGet, sitePath+"/recipients?q="+url.QueryEscape(strings.Repeat("界", 200)), nil); r.Code != 200 {
		t.Fatalf("multibyte query rejected=%d %s", r.Code, r.Body)
	}
	if r = f.do(f.admin, http.MethodGet, "/api/sites/bad/notification-routing", nil); r.Code != 400 {
		t.Fatalf("invalid ID=%d", r.Code)
	}
	req := httptest.NewRequest(http.MethodGet, sitePath, nil)
	res := httptest.NewRecorder()
	f.handler.ServeHTTP(res, req)
	if res.Code != 401 {
		t.Fatalf("unauthenticated=%d", res.Code)
	}
}

func TestNotificationRoutingStrictPayload(t *testing.T) {
	for _, raw := range []string{`{}`, `{"expected_version":0,"mode":"silent"}`, `{"expected_version":0,"mode":"selected","user_ids":[]}`, `{"expected_version":0,"expected_version":1,"mode":"inherit"}`, `{"expected_version":0,"mode":"inherit","scope_id":"injected"}`, `{"expected_version":null,"mode":"inherit"}`, `{"expected_version":0,"mode":"inherit","user_ids":null}`, `{"expected_version":-1,"mode":"inherit"}`, `{"expected_version":0,"mode":"inherit"} {}`, strings.Repeat(" ", 20001)} {
		if _, _, err := decodeNotificationRouting(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
