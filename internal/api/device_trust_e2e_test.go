package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// Runs in the PostgreSQL CI job. Policy assignments filter on a device's OS
// and role, so what the device says must not move it between them: a device
// enrolled as Linux that reconnects naming Windows, and reports a Windows
// Server inventory, stays a Linux device without a role, through the real
// registration and frame handlers and the REST API. Only an administrator's
// correction changes the role.
func TestDeviceCannotRetargetItselfE2E(t *testing.T) {
	f := newBulkFixture(t)
	s := &Server{cfg: &config.Config{}, store: f.st, hub: hub.New()}

	rr := httptest.NewRecorder()
	s.createEnrollToken(rr, enrollTokenRequest(http.MethodPost, "/api/enroll-tokens", `{"site_id":"`+f.siteA1.ID+`"}`, "admin-"+uuid.NewString()[:8], "", ""))
	var tok models.EnrollToken
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &tok) != nil {
		t.Fatalf("enroll token: %d %s", rr.Code, rr.Body.String())
	}
	id, token, _, err := s.registerAgent(f.ctx, &protocol.RegisterReq{EnrollToken: tok.Token, Hostname: "LIN-TRUST", OS: "linux", Arch: "amd64", Version: "0.11.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.registerAgent(f.ctx, &protocol.RegisterReq{AgentID: id, AgentToken: token, Hostname: "LIN-TRUST", OS: "windows", Arch: "amd64", Version: "0.11.1"}); err != nil {
		t.Fatalf("a reconnect naming another OS must still connect: %v", err)
	}
	lease := s.hub.Add(id, nil)
	defer func() { s.hub.Remove(id, lease) }()
	data, _ := json.Marshal(protocol.InventoryMsg{Data: json.RawMessage(`{"os":"windows","platform":"Microsoft Windows Server 2022 Standard","platform_family":"Server"}`)})
	env, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgInventory, Data: data})
	s.handleAgentFrame(f.ctx, id, lease, env)

	device := func() models.Agent {
		t.Helper()
		rr := f.do(f.admin, http.MethodGet, "/api/devices/"+id, nil)
		var a models.Agent
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &a) != nil {
			t.Fatalf("GET device: %d %s", rr.Code, rr.Body.String())
		}
		return a
	}
	if a := device(); a.OS != "linux" || a.Version != "0.11.1" || a.OSRole != "" || a.Role != "" {
		t.Fatalf("after the claims: os=%q version=%q detected=%q role=%q", a.OS, a.Version, a.OSRole, a.Role)
	}
	if rr := f.do(f.admin, http.MethodPut, "/api/devices/"+id+"/role", map[string]string{"role": models.RoleServer}); rr.Code != http.StatusOK {
		t.Fatalf("role correction: %d %s", rr.Code, rr.Body.String())
	}
	if a := device(); a.Role != models.RoleServer || a.OS != "linux" {
		t.Fatalf("after the correction: os=%q role=%q", a.OS, a.Role)
	}
}
