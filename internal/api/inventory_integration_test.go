package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// Runs in the PostgreSQL CI job. Follow a Windows snapshot from an authenticated
// connection's frame through JSONB storage and the permission-scoped REST API.
// No network listener is needed; requests go straight through the router.
func TestInventoryFrameToAPIIntegration(t *testing.T) {
	f := newBulkFixture(t)
	s := &Server{store: f.st, hub: hub.New()}
	lease := s.hub.Add(f.a1, nil)
	defer func() { s.hub.Remove(f.a1, lease) }()
	frame := func(raw string) []byte {
		t.Helper()
		data, err := json.Marshal(protocol.InventoryMsg{Data: json.RawMessage(raw)})
		if err != nil {
			t.Fatal(err)
		}
		env, err := json.Marshal(protocol.Envelope{Type: protocol.MsgInventory, Data: data})
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	get := func(token, id string, status int) *models.Inventory {
		t.Helper()
		rr := f.do(token, http.MethodGet, "/api/devices/"+id+"/inventory", nil)
		if rr.Code != status {
			t.Fatalf("GET inventory: %d %s, want %d", rr.Code, rr.Body.String(), status)
		}
		if status != http.StatusOK {
			return nil
		}
		var inv models.Inventory
		if err := json.Unmarshal(rr.Body.Bytes(), &inv); err != nil {
			t.Fatal(err)
		}
		return &inv
	}
	get(f.admin, f.a1, http.StatusNotFound)

	raw := `{"hostname":"windows-11","os":"windows","cpu_model":"CPU\u0000","software":[{"name":"Example\u0000 App"}],"size":18446744073709551615,"path":"C:\\u0000\\app"}`
	s.handleAgentFrame(f.ctx, f.a1, lease, frame(raw))
	inv := get(f.admin, f.a1, http.StatusOK)
	if inv.AgentID != f.a1 || inv.CollectedAt.IsZero() {
		t.Fatalf("missing persisted metadata: %+v", inv)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(inv.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if string(snapshot["cpu_model"]) != `"CPU"` || string(snapshot["size"]) != "18446744073709551615" || string(snapshot["path"]) != `"C:\\u0000\\app"` {
		t.Fatalf("corrupted inventory: %s", inv.Data)
	}
	get(f.scoped, f.a1, http.StatusOK)
	get(f.scoped, f.b1, http.StatusForbidden)
	get("", f.a1, http.StatusUnauthorized)

	// Invalid or empty snapshots cannot erase the last good one.
	for _, bad := range []string{
		"null", `[]`, `"text"`, `{"bad\u0000key":1}`,
		strings.Repeat(`{"n":`, 1000) + `0` + strings.Repeat(`}`, 1000),
		`{"values":[` + strings.Repeat(`{},`, 30000) + `{}]}`,
	} {
		s.handleAgentFrame(f.ctx, f.a1, lease, frame(bad))
		if got := get(f.admin, f.a1, http.StatusOK); string(got.Data) != string(inv.Data) {
			t.Fatalf("invalid snapshot replaced the last good one: %s", got.Data)
		}
	}

	// A reconnect replaces the transport's lease: a frame already read from the
	// displaced connection must not update inventory on the new connection.
	old := lease
	lease = s.hub.Add(f.a1, nil)
	s.handleAgentFrame(f.ctx, f.a1, old, frame(`{"hostname":"stale"}`))
	if got := get(f.admin, f.a1, http.StatusOK); string(got.Data) != string(inv.Data) {
		t.Fatal("a displaced connection changed inventory")
	}
	s.handleAgentFrame(f.ctx, f.a1, lease, frame(`{"hostname":"fresh"}`))
	got := get(f.admin, f.a1, http.StatusOK)
	if err := json.Unmarshal(got.Data, &snapshot); err != nil || string(snapshot["hostname"]) != `"fresh"` {
		t.Fatalf("current connection could not replace inventory: %s", got.Data)
	}
}
