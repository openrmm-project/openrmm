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

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/store"
)

// One desktop session per operator per device. A second one from the same
// operator is a forgotten tab whose input fights the real one; the console is
// told so, and may move the session instead ("continue here").
func TestStartRemoteRefusesTheSameOperatorTwiceOnOneDevice(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := newTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	agent, err := st.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	other, err := st.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	user, err := st.CreateUser(ctx, "admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	token := auth.NewToken()
	must(st.CreateSession(ctx, user.ID, auth.Hash(token), time.Now().Add(time.Hour)))

	h := hub.New()
	srv := New(&config.Config{APIRateLimit: 10000}, st, h, nil, remote.NewManager(st, h, "", "", "", false, false), remote.NewRelay(), nil, nil, nil)
	handler := srv.Router()
	start := func(agentID string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/devices/"+agentID+"/remote", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// The operator's first tab is attached to the device.
	srv.presence.Join(remote.Viewer{SessionID: "first-tab", AgentID: agent.ID, User: user.ID, Kind: "native"})

	rec := start(agent.ID, map[string]any{"provider": "native"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"remote_session_active"`) {
		t.Fatalf("a second desktop session was not refused: %d %s", rec.Code, rec.Body)
	}

	// Not a duplicate: another device, and the same device over another kind.
	// (Both then fail only because the fixture's devices are offline.)
	for name, tc := range map[string]struct {
		agentID string
		body    map[string]any
	}{
		"another device": {other.ID, map[string]any{"provider": "native"}},
		"file transfer":  {agent.ID, map[string]any{"provider": "files"}},
		"terminal":       {agent.ID, map[string]any{"provider": "terminal"}},
		// A viewer whose own connection dropped takes its session back
		// without asking.
		"own reconnect": {agent.ID, map[string]any{"provider": "native", "resume": "first-tab"}},
	} {
		if rec := start(tc.agentID, tc.body); rec.Code == http.StatusConflict {
			t.Errorf("%s was refused as a duplicate: %s", name, rec.Body)
		}
	}
	// The reconnect above replaced the first tab; attach it again.
	srv.presence.Join(remote.Viewer{SessionID: "first-tab", AgentID: agent.ID, User: user.ID, Kind: "native"})

	// Resuming some other session is not a way around the rule.
	if rec := start(agent.ID, map[string]any{"provider": "native", "resume": "not-mine"}); rec.Code != http.StatusConflict {
		t.Fatalf("resume of an unrelated session bypassed the rule: %d %s", rec.Code, rec.Body)
	}

	// "Continue here" moves the session: the old tab is let go and recorded.
	if rec := start(agent.ID, map[string]any{"provider": "native", "replace": true}); rec.Code == http.StatusConflict {
		t.Fatalf("replace was refused: %s", rec.Body)
	}
	if live := srv.presence.ForUser(agent.ID, user.ID, "native"); len(live) != 0 {
		t.Fatalf("the replaced tab is still attached: %+v", live)
	}
	entries, _, err := st.ListAudit(ctx, store.AuditFilter{Action: "remote.replace", Target: agent.ID, Limit: 10})
	must(err)
	if len(entries) == 0 {
		t.Fatal("replacing a session left no audit record")
	}
}
