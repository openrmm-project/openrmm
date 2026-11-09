package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// meshAuditDetail reads an audit entry's detail. PostgreSQL keeps it as jsonb
// and hands it back reformatted ("key": "value"), so compare fields, not text.
func meshAuditDetail(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var detail map[string]string
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("audit detail %s: %v", raw, err)
	}
	return detail
}

// Opening an unlinked device in MeshCentral by its hostname (issue #226): off
// until an administrator turns it on, a linked node always wins, and a
// hostname a MeshCentral link cannot carry is refused rather than sent.
func TestMeshCentralOpensUnlinkedDevicesByHostnameOnlyWhenTurnedOn(t *testing.T) {
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
	host := "DESKTOP-" + suffix
	unlinked, err := st.CreateAgent(ctx, host, "windows", "amd64", "0.9.3", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	linked, err := st.CreateAgent(ctx, "LINKED-"+suffix, "windows", "amd64", "0.9.3", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	node := "node//" + suffix
	must(st.SetRemoteIDs(ctx, linked.ID, &node))
	hostile, err := st.CreateAgent(ctx, "x&login=attacker-"+suffix, "windows", "amd64", "0.9.3", "tok-"+uuid.NewString(), nil, nil)
	must(err)

	admin, err := st.CreateUser(ctx, "admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	token := auth.NewToken()
	must(st.CreateSession(ctx, admin.ID, auth.Hash(token), time.Now().Add(time.Hour)))

	h := hub.New()
	cfg := &config.Config{APIRateLimit: 100000}
	chat := ai.NewEngine(ai.NewClient("", "", ""), st, nil, nil, cfg)
	srv := New(cfg, st, h, nil, remote.NewManager(st, h, "", "", "", false, false), remote.NewRelay(), chat, nil, nil)
	handler := srv.Router()
	callAs := func(bearer, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		return callAs(token, method, path, body)
	}
	configure := func(byHostname bool) {
		t.Helper()
		rec := call(http.MethodPut, "/api/config", map[string]any{"remote": map[string]any{
			"meshcentral_url": "https://mesh.example.test", "meshcentral_enabled": true, "meshcentral_match_hostname": byHostname,
		}})
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/config: %d %s", rec.Code, rec.Body)
		}
		var c struct {
			Remote struct {
				MatchHostname bool `json:"meshcentral_match_hostname"`
			} `json:"remote"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &c))
		if c.Remote.MatchHostname != byHostname {
			t.Fatalf("GET after PUT says meshcentral_match_hostname=%t, want %t", c.Remote.MatchHostname, byHostname)
		}
	}
	t.Cleanup(func() {
		for _, key := range []string{settings.KeyMeshCentralURL, settings.KeyMeshCentralEnabled, settings.KeyMeshCentralMatchHostname} {
			_ = st.DeleteAppSetting(context.Background(), key)
		}
	})
	startAs := func(bearer, agentID string) (*httptest.ResponseRecorder, models.RemoteSession) {
		t.Helper()
		rec := callAs(bearer, http.MethodPost, "/api/devices/"+agentID+"/remote", map[string]any{"provider": "meshcentral"})
		var sess models.RemoteSession
		if rec.Code == http.StatusCreated {
			must(json.Unmarshal(rec.Body.Bytes(), &sess))
		}
		return rec, sess
	}
	start := func(agentID string) (*httptest.ResponseRecorder, models.RemoteSession) {
		t.Helper()
		return startAs(token, agentID)
	}

	// Off: an unlinked device is refused, as before; a linked one opens by node.
	configure(false)
	if rec, _ := start(unlinked.ID); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no MeshCentral node id") {
		t.Fatalf("with matching off, an unlinked device was not refused: %d %s", rec.Code, rec.Body)
	}
	rec, sess := start(linked.ID)
	if rec.Code != http.StatusCreated || sess.ConnectURL != "https://mesh.example.test/?viewmode=11&hide=31&node="+node || sess.MatchedBy != "node" {
		t.Fatalf("the linked device did not open by its node: %d %s", rec.Code, rec.Body)
	}

	// On: the unlinked device opens by hostname; the linked one still by node.
	configure(true)
	rec, sess = start(unlinked.ID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("with matching on, the unlinked device was refused: %d %s", rec.Code, rec.Body)
	}
	if want := "https://mesh.example.test/?viewmode=11&hide=31&gotodevicername=" + host; sess.ConnectURL != want || sess.MatchedBy != "hostname" {
		t.Fatalf("hostname link:\n got %s (%s)\nwant %s (hostname)", sess.ConnectURL, sess.MatchedBy, want)
	}
	if rec, sess := start(linked.ID); rec.Code != http.StatusCreated || !strings.Contains(sess.ConnectURL, "&node="+node) || strings.Contains(sess.ConnectURL, "gotodevicername") {
		t.Fatalf("a linked node must win over the hostname: %d %s", rec.Code, rec.Body)
	}

	// A restricted technician, here scoped to exactly these two devices, needs
	// a linked node: MeshCentral would look the name up among every computer
	// it knows, including other clients'. The linked device still opens.
	tech, err := st.CreateUser(ctx, "tech-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	must(st.ReplaceUserScopes(ctx, tech.ID, []models.UserScope{
		{ScopeType: models.ScopeAgent, ScopeID: unlinked.ID},
		{ScopeType: models.ScopeAgent, ScopeID: linked.ID},
	}))
	techToken := auth.NewToken()
	must(st.CreateSession(ctx, tech.ID, auth.Hash(techToken), time.Now().Add(time.Hour)))
	if rec, _ := startAs(techToken, unlinked.ID); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "access to every device") {
		t.Fatalf("a restricted technician opened an unlinked device by hostname: %d %s", rec.Code, rec.Body)
	}
	if rec, sess := startAs(techToken, linked.ID); rec.Code != http.StatusCreated || sess.MatchedBy != "node" {
		t.Fatalf("a restricted technician could not open a linked device in scope: %d %s", rec.Code, rec.Body)
	}

	// A hostname that would add its own arguments to the link is refused, and
	// not echoed back.
	rec, _ = start(hostile.ID)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "link its node instead") || strings.Contains(rec.Body.String(), "attacker") {
		t.Fatalf("a hostile hostname was not refused cleanly: %d %s", rec.Code, rec.Body)
	}

	// The audit trail says how each device was found, and that the setting moved.
	entries, _, err := st.ListAudit(ctx, store.AuditFilter{Action: "remote.start", Target: unlinked.ID, Limit: 10})
	must(err)
	if len(entries) != 1 || meshAuditDetail(t, entries[0].Detail)["matched_by"] != "hostname" {
		t.Fatalf("remote.start by hostname is not recorded as such: %+v", entries)
	}
	entries, _, err = st.ListAudit(ctx, store.AuditFilter{Action: "config.update", Limit: 50})
	must(err)
	found := false
	for _, e := range entries {
		if e.Actor == admin.ID && strings.Contains(string(e.Detail), "remote.meshcentral_match_hostname") {
			found = true
		}
	}
	if !found {
		t.Fatal("turning hostname matching on is not named in config.update")
	}
}
