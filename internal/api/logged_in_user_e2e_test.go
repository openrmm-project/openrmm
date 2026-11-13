package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// Runs in the PostgreSQL CI job. Follow the logged-in user from an agent's
// heartbeat frame through the agents table to the permission-scoped REST API
// (device, device list, machines and hardware reports): cleaned on the way in,
// kept by an agent that does not report it, cleared when no one is logged in
// (the last logged-in user is kept), and only shown to principals who may see
// the device.
func TestLoggedInUserHeartbeatToAPIE2E(t *testing.T) {
	f := newBulkFixture(t)
	s := &Server{store: f.st, hub: hub.New()}
	lease := s.hub.Add(f.a1, nil)
	defer func() { s.hub.Remove(f.a1, lease) }()
	other := s.hub.Add(f.b1, nil)
	defer func() { s.hub.Remove(f.b1, other) }()

	heartbeat := func(agentID string, ch chan []byte, data string) {
		t.Helper()
		env, err := json.Marshal(protocol.Envelope{Type: protocol.MsgHeartbeat, Data: json.RawMessage(data)})
		if err != nil {
			t.Fatal(err)
		}
		s.handleAgentFrame(f.ctx, agentID, ch, env)
	}
	device := func(token, id string, status int) *models.Agent {
		t.Helper()
		rr := f.do(token, http.MethodGet, "/api/devices/"+id, nil)
		if rr.Code != status {
			t.Fatalf("GET device: %d %s, want %d", rr.Code, rr.Body.String(), status)
		}
		if status != http.StatusOK {
			return nil
		}
		var a models.Agent
		if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		return &a
	}
	listed := func(token string) map[string]string {
		t.Helper()
		rr := f.do(token, http.MethodGet, "/api/devices", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET devices: %d %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Devices []models.Agent `json:"devices"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		users := map[string]string{}
		for _, a := range out.Devices {
			users[a.ID] = a.LoggedInUser
		}
		return users
	}

	// Before any report: empty, no timestamp.
	if a := device(f.admin, f.a1, http.StatusOK); a.LoggedInUser != "" || a.LoggedInUserSince != nil {
		t.Fatalf("fresh device: %q %v", a.LoggedInUser, a.LoggedInUserSince)
	}

	// Reported, cleaned of control and invisible characters.
	heartbeat(f.a1, lease, `{"logged_in_user":"  ACME\\maria\u0000\u200b\r\n"}`)
	heartbeat(f.b1, other, `{"logged_in_user":"AzureAD\\Other"}`)
	a := device(f.admin, f.a1, http.StatusOK)
	if a.LoggedInUser != `ACME\maria` || a.LoggedInUserSince == nil || a.Status != "online" {
		t.Fatalf("after heartbeat: %+v", a)
	}
	since := *a.LoggedInUserSince

	// The same user again keeps "since"; an agent older than 0.11 (no field,
	// or the empty object it always sent) leaves the user alone.
	time.Sleep(20 * time.Millisecond)
	heartbeat(f.a1, lease, `{"logged_in_user":"ACME\\maria"}`)
	heartbeat(f.a1, lease, `{}`)
	heartbeat(f.a1, lease, `null`)
	heartbeat(f.a1, lease, `"not an object"`)
	if a := device(f.admin, f.a1, http.StatusOK); a.LoggedInUser != `ACME\maria` || !a.LoggedInUserSince.Equal(since) {
		t.Fatalf("unchanged user moved: %q %v (was %v)", a.LoggedInUser, a.LoggedInUserSince, since)
	}

	// Scope: the restricted principal sees its client's device and user, and
	// neither the other client's device nor its user.
	if users := listed(f.scoped); users[f.a1] != `ACME\maria` {
		t.Fatalf("scoped list: %v", users)
	} else if _, leaked := users[f.b1]; leaked {
		t.Fatalf("scoped list leaked another client's device: %v", users)
	}
	device(f.scoped, f.b1, http.StatusForbidden)
	if users := listed(f.admin); users[f.b1] != `AzureAD\Other` {
		t.Fatalf("admin list: %v", users)
	}

	// The machines report carries it too, within scope.
	rr := f.do(f.scoped, http.MethodGet, "/api/reports/machines", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("machines report: %d %s", rr.Code, rr.Body.String())
	}
	var report struct {
		Rows []struct {
			Hostname     string `json:"hostname"`
			LoggedInUser string `json:"logged_in_user"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range report.Rows {
		if row.LoggedInUser == `AzureAD\Other` {
			t.Fatalf("scoped machines report leaked another client's user: %+v", report.Rows)
		}
		found = found || row.LoggedInUser == `ACME\maria`
	}
	if !found {
		t.Fatalf("machines report misses the logged-in user: %+v", report.Rows)
	}

	// Logging off clears it and moves "since".
	time.Sleep(20 * time.Millisecond)
	heartbeat(f.a1, lease, `{"logged_in_user":""}`)
	a = device(f.admin, f.a1, http.StatusOK)
	if a.LoggedInUser != "" || a.LoggedInUserSince == nil || !a.LoggedInUserSince.After(since) {
		t.Fatalf("after logoff: %q %v", a.LoggedInUser, a.LoggedInUserSince)
	}

	// The last logged-in user outlives the logoff: the device API and the
	// hardware report (Hardware & System) still say who uses the machine, and
	// the report keeps to the caller's scope.
	if a.LastLoggedInUser != `ACME\maria` || a.LastLoggedInUserAt == nil {
		t.Fatalf("last user after logoff: %q %v", a.LastLoggedInUser, a.LastLoggedInUserAt)
	}
	for _, id := range []string{f.a1, f.b1} {
		if err := f.st.UpsertInventory(f.ctx, id, []byte(`{"cpu_model":"Test CPU"}`)); err != nil {
			t.Fatal(err)
		}
	}
	lastUsers := func(token string) []string {
		t.Helper()
		rr := f.do(token, http.MethodGet, "/api/reports/hardware", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("hardware report: %d %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Rows []struct {
				LastLoggedInUser   string     `json:"last_logged_in_user"`
				LastLoggedInUserAt *time.Time `json:"last_logged_in_user_at"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var users []string
		for _, row := range out.Rows {
			if row.LastLoggedInUser != "" && row.LastLoggedInUserAt == nil {
				t.Fatalf("last user without a time: %+v", row)
			}
			users = append(users, row.LastLoggedInUser)
		}
		return users
	}
	if users := lastUsers(f.scoped); !slices.Contains(users, `ACME\maria`) || slices.Contains(users, `AzureAD\Other`) {
		t.Fatalf("scoped hardware report: %v", users)
	}
	if users := lastUsers(f.admin); !slices.Contains(users, `ACME\maria`) || !slices.Contains(users, `AzureAD\Other`) {
		t.Fatalf("admin hardware report: %v", users)
	}

	// A frame from a replaced connection is not applied.
	s.hub.Remove(f.a1, lease)
	heartbeat(f.a1, lease, `{"logged_in_user":"ACME\\stale"}`)
	if a := device(f.admin, f.a1, http.StatusOK); a.LoggedInUser != "" || a.LastLoggedInUser != `ACME\maria` {
		t.Fatalf("stale connection wrote the user: %q (last %q)", a.LoggedInUser, a.LastLoggedInUser)
	}

	// A revoked device's record is not touched.
	current := s.hub.Add(f.a2, nil)
	defer func() { s.hub.Remove(f.a2, current) }()
	if err := f.st.RevokeAgent(f.ctx, f.a2); err != nil {
		t.Fatal(err)
	}
	heartbeat(f.a2, current, `{"logged_in_user":"ACME\\ghost"}`)
	if ghost, err := f.st.GetAgent(f.ctx, f.a2); err != nil {
		t.Fatal(err)
	} else if ghost.LoggedInUser != "" || ghost.LastLoggedInUser != "" || ghost.Status != "revoked" {
		t.Fatalf("revoked device recorded a heartbeat: %q %s", ghost.LoggedInUser, ghost.Status)
	}
}
