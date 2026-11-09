package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// meshTokenUser opens the MeshCentral login token in a connect URL with the
// server's key, as MeshCentral does, and returns the user id it signs in as.
func meshTokenUser(t *testing.T, keyHex, connectURL string) string {
	t.Helper()
	u, err := url.Parse(connectURL)
	if err != nil {
		t.Fatal(err)
	}
	tok := u.Query().Get("login")
	if tok == "" {
		t.Fatalf("no login token in %s", connectURL)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.NewReplacer("@", "+", "$", "/").Replace(tok))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(keyHex)
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv, tag, ct := raw[:12], raw[12:28], raw[28:]
	plain, err := gcm.Open(nil, iv, append(append([]byte{}, ct...), tag...), nil)
	if err != nil {
		t.Fatalf("the login token does not open with the server's key: %v", err)
	}
	var payload struct {
		UserID string `json:"userid"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.UserID
}

// With login tokens, each user signs in to MeshCentral as the account an
// administrator gave them, so MeshCentral's own permissions bound what they
// reach there. The shared account is only for users with access to every
// device, and can be turned off.
func TestMeshCentralSignsEachUserInAsTheirOwnAccount(t *testing.T) {
	dbURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := newTestStore(t, dbURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	suffix := strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	linked, err := st.CreateAgent(ctx, "LINKED-"+suffix, "windows", "amd64", "0.9.3", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	node := "node//" + suffix
	must(st.SetRemoteIDs(ctx, linked.ID, &node))
	unlinked, err := st.CreateAgent(ctx, "DESKTOP-"+suffix, "windows", "amd64", "0.9.3", "tok-"+uuid.NewString(), nil, nil)
	must(err)

	session := func(u *models.User) string {
		t.Helper()
		tok := auth.NewToken()
		must(st.CreateSession(ctx, u.ID, auth.Hash(tok), time.Now().Add(time.Hour)))
		return tok
	}
	admin, err := st.CreateUser(ctx, "admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	adminToken := session(admin)
	// A technician restricted to these two devices, who may also manage users
	// inside that scope: the admin role, bound to scope rows.
	tech, err := st.CreateUser(ctx, "tech-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	must(st.ReplaceUserScopes(ctx, tech.ID, []models.UserScope{
		{ScopeType: models.ScopeAgent, ScopeID: linked.ID},
		{ScopeType: models.ScopeAgent, ScopeID: unlinked.ID},
	}))
	techToken := session(tech)
	peer, err := st.CreateUser(ctx, "peer-"+uuid.NewString(), "hash", models.RoleTech)
	must(err)
	must(st.ReplaceUserScopes(ctx, peer.ID, []models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: linked.ID}}))

	h := hub.New()
	cfg := &config.Config{APIRateLimit: 100000}
	chat := ai.NewEngine(ai.NewClient("", "", ""), st, nil, nil, cfg)
	srv := New(cfg, st, h, nil, remote.NewManager(st, h, "", "", "", false, false), remote.NewRelay(), chat, nil, nil)
	handler := srv.Router()
	call := func(bearer, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	t.Cleanup(func() {
		for _, key := range []string{settings.KeyMeshCentralURL, settings.KeyMeshCentralEnabled, settings.KeyMeshCentralUser,
			settings.KeyMeshCentralTokenKey, settings.KeyMeshCentralMatchHostname, settings.KeyMeshCentralSharedAccount} {
			_ = st.DeleteAppSetting(context.Background(), key)
		}
	})
	keyHex := strings.Repeat("5a", 32)
	configure := func(shared bool) {
		t.Helper()
		rec := call(adminToken, http.MethodPut, "/api/config", map[string]any{"remote": map[string]any{
			"meshcentral_url": "https://mesh.example.test", "meshcentral_enabled": true, "meshcentral_user": "admin",
			"meshcentral_token_key": keyHex, "meshcentral_match_hostname": true, "meshcentral_shared_account": shared,
		}})
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/config: %d %s", rec.Code, rec.Body)
		}
		var c struct {
			Remote struct {
				Shared bool `json:"meshcentral_shared_account"`
			} `json:"remote"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &c))
		if c.Remote.Shared != shared {
			t.Fatalf("GET after PUT says meshcentral_shared_account=%t, want %t", c.Remote.Shared, shared)
		}
	}
	start := func(bearer, agentID string) (*httptest.ResponseRecorder, models.RemoteSession) {
		t.Helper()
		rec := call(bearer, http.MethodPost, "/api/devices/"+agentID+"/remote", map[string]any{"provider": "meshcentral"})
		var sess models.RemoteSession
		if rec.Code == http.StatusCreated {
			must(json.Unmarshal(rec.Body.Bytes(), &sess))
		}
		return rec, sess
	}
	setAccount := func(bearer, userID string, name any) *httptest.ResponseRecorder {
		t.Helper()
		return call(bearer, http.MethodPut, "/api/users/"+userID+"/meshcentral", map[string]any{"user": name})
	}
	configure(true)

	// Before an administrator gives the technician an account, the shared one
	// is not theirs to use: MeshCentral would let them reach every computer.
	if rec, _ := start(techToken, linked.ID); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no MeshCentral account of your own") {
		t.Fatalf("a restricted technician signed in as the shared account: %d %s", rec.Code, rec.Body)
	}
	// The administrator, with access to every device, uses the shared account.
	rec, sess := start(adminToken, linked.ID)
	if rec.Code != http.StatusCreated || sess.MeshAccount != "shared" || meshTokenUser(t, keyHex, sess.ConnectURL) != "user//admin" {
		t.Fatalf("the administrator did not sign in as the shared account: %d %s", rec.Code, rec.Body)
	}

	// Only a principal with access to every device names MeshCentral accounts:
	// the restricted technician manages users in their scope, but naming one
	// would hand over whatever that account can do in MeshCentral.
	if rec := setAccount(techToken, peer.ID, "admin"); rec.Code != http.StatusForbidden {
		t.Fatalf("a restricted principal set a MeshCentral account: %d %s", rec.Code, rec.Body)
	}
	if rec := setAccount(techToken, tech.ID, "admin"); rec.Code != http.StatusForbidden {
		t.Fatalf("a restricted principal set their own MeshCentral account: %d %s", rec.Code, rec.Body)
	}
	// Nor does a user manager without settings.manage, who could otherwise give
	// themselves the account of MeshCentral's administrator.
	role, err := st.CreateRole(ctx, &models.Role{Key: "usermgr-" + strings.ToLower(suffix), Name: "User manager",
		Permissions: []string{models.PermUsersManage, models.PermDevicesView, models.PermDevicesRemote}})
	must(err)
	t.Cleanup(func() { _ = st.DeleteRole(context.Background(), role.Key) })
	manager, err := st.CreateUser(ctx, "usermgr-"+uuid.NewString(), "hash", role.Key)
	must(err)
	if rec := setAccount(session(manager), manager.ID, "admin"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "settings.manage") {
		t.Fatalf("a user manager without settings.manage set a MeshCentral account: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []any{"tech one", "tech/one", "../admin", strings.Repeat("a", 129), nil} {
		if rec := setAccount(adminToken, tech.ID, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("MeshCentral account %v was accepted: %d %s", bad, rec.Code, rec.Body)
		}
	}
	// MeshCentral keeps account names in lowercase.
	if rec := setAccount(adminToken, tech.ID, "  Tech.One@Example.com "); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tech.one@example.com"`) {
		t.Fatalf("setting the technician's account: %d %s", rec.Code, rec.Body)
	}
	rec = call(adminToken, http.MethodGet, "/api/users", nil)
	if !strings.Contains(rec.Body.String(), `"meshcentral_user":"tech.one@example.com"`) {
		t.Fatalf("the users list does not show the account: %s", rec.Body)
	}

	// Signed in as their own account, the technician opens a linked device, and
	// an unlinked one by hostname: MeshCentral looks the name up among the
	// computers that account sees, not every computer.
	rec, sess = start(techToken, linked.ID)
	if rec.Code != http.StatusCreated || sess.MeshAccount != "personal" || meshTokenUser(t, keyHex, sess.ConnectURL) != "user//tech.one@example.com" {
		t.Fatalf("the technician did not sign in as their own account: %d %s", rec.Code, rec.Body)
	}
	rec, sess = start(techToken, unlinked.ID)
	if rec.Code != http.StatusCreated || sess.MatchedBy != "hostname" || sess.MeshAccount != "personal" {
		t.Fatalf("with an account of their own, the technician could not open an unlinked device by hostname: %d %s", rec.Code, rec.Body)
	}

	// Given the shared account's name as their own, a restricted user is refused.
	must(st.SetUserMeshCentralUser(ctx, tech.ID, "admin"))
	if rec, _ := start(techToken, linked.ID); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is the shared one") {
		t.Fatalf("a restricted technician signed in as the shared account under their own name: %d %s", rec.Code, rec.Body)
	}

	// With the shared account turned off, everyone needs their own.
	configure(false)
	if rec, _ := start(adminToken, linked.ID); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "turned off") {
		t.Fatalf("with the shared account off, the administrator still signed in as it: %d %s", rec.Code, rec.Body)
	}
	if rec := setAccount(adminToken, admin.ID, "alice"); rec.Code != http.StatusOK {
		t.Fatalf("setting the administrator's account: %d %s", rec.Code, rec.Body)
	}
	rec, sess = start(adminToken, linked.ID)
	if rec.Code != http.StatusCreated || sess.MeshAccount != "personal" || meshTokenUser(t, keyHex, sess.ConnectURL) != "user//alice" {
		t.Fatalf("the administrator did not sign in as their own account: %d %s", rec.Code, rec.Body)
	}
	// Cleared, the account is gone.
	if rec := setAccount(adminToken, admin.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("clearing the administrator's account: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := start(adminToken, linked.ID); rec.Code != http.StatusBadRequest {
		t.Fatalf("a cleared account still signed in: %d %s", rec.Code, rec.Body)
	}

	// The audit trail names who set which account, and which one each session used.
	entries, _, err := st.ListAudit(ctx, store.AuditFilter{Action: "user.meshcentral_account", Target: tech.ID, Limit: 10})
	must(err)
	if len(entries) != 1 || entries[0].Actor != admin.ID || meshAuditDetail(t, entries[0].Detail)["meshcentral_user"] != "tech.one@example.com" {
		t.Fatalf("setting a MeshCentral account is not audited: %+v", entries)
	}
	entries, _, err = st.ListAudit(ctx, store.AuditFilter{Action: "remote.start", Target: unlinked.ID, Limit: 10})
	must(err)
	if len(entries) != 1 {
		t.Fatalf("want one remote.start for the unlinked device, got %+v", entries)
	}
	if d := meshAuditDetail(t, entries[0].Detail); d["mesh_account"] != "personal" || d["mesh_user"] != "tech.one@example.com" {
		t.Fatalf("remote.start does not record the MeshCentral account: %+v", entries)
	}
}
