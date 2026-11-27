package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

func TestDockerSnapshotScopedAPIAndConnectionLease(t *testing.T) {
	f := newBulkFixture(t)
	s := &Server{store: f.st, hub: hub.New()}
	old := s.hub.Add(f.a1, nil)
	other := s.hub.Add(f.b1, nil)
	report := func(id, name string, ch chan []byte) {
		t.Helper()
		data, _ := json.Marshal(protocol.DockerSnapshot{Status: protocol.DockerAvailable, Containers: []protocol.DockerContainer{{ID: strings.Repeat("a", 64), Name: name, Image: "private/image:1", State: "running", Health: "healthy"}}})
		frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgDockerSnapshot, Data: data})
		s.handleAgentFrame(f.ctx, id, ch, frame)
	}
	report(f.a1, "mine", old)
	report(f.b1, "other-tenant", other)
	if !s.hub.SupportsDocker(f.a1) {
		t.Fatal("valid report did not enable current capability")
	}
	get := func(token, path string, status int) []byte {
		t.Helper()
		r := f.do(token, http.MethodGet, path, nil)
		if r.Code != status {
			t.Fatalf("GET %s = %d %s", path, r.Code, r.Body.String())
		}
		return r.Body.Bytes()
	}
	raw := get(f.scoped, "/api/docker?limit=1", http.StatusOK)
	var fleet store.DockerFleet
	if err := json.Unmarshal(raw, &fleet); err != nil {
		t.Fatal(err)
	}
	if fleet.Total != 1 || len(fleet.Containers) != 1 || fleet.Containers[0].Name != "mine" || strings.Contains(string(raw), "other-tenant") {
		t.Fatalf("scope leak or incorrect total: %s", raw)
	}
	for _, h := range fleet.Hosts {
		if h.AgentID == f.b1 {
			t.Fatal("out-of-scope collection status leaked")
		}
	}
	get(f.scoped, "/api/devices/"+f.b1+"/docker", http.StatusForbidden)
	get(f.scoped, "/api/docker?agent_id="+f.b1, http.StatusForbidden)
	get(f.scoped, "/api/docker?scope=site&id="+f.siteB.ID, http.StatusForbidden)
	for _, query := range []string{"limit=201", "offset=-1", "health=green", "state=bad"} {
		get(f.admin, "/api/docker?"+query, http.StatusBadRequest)
	}
	get("", "/api/docker", http.StatusUnauthorized)
	current := s.hub.Add(f.a1, nil)
	report(f.a1, "displaced", old)
	got, _ := f.st.DockerInventory(f.ctx, f.a1)
	if got.Containers[0].Name != "mine" || s.hub.SupportsDocker(f.a1) {
		t.Fatal("old transport changed inventory/capability")
	}
	report(f.a1, "replacement", current)
	got, _ = f.st.DockerInventory(f.ctx, f.a1)
	if got.Containers[0].Name != "replacement" {
		t.Fatal("new lease failed")
	}
	if err := f.st.RevokeAgent(f.ctx, f.a1); err != nil {
		t.Fatal(err)
	}
	s.hub.Disconnect(f.a1)
	report(f.a1, "revoked", current)
	if s.hub.SupportsDocker(f.a1) {
		t.Fatal("revoked transport enabled capability")
	}
}

func TestDockerInventoryAndCheckPermissions(t *testing.T) {
	f := newBulkFixture(t)
	tokenFor := func(role string) string {
		t.Helper()
		u, err := f.st.CreateUser(f.ctx, "docker-"+uuid.NewString(), "hash", role)
		if err != nil {
			t.Fatal(err)
		}
		token := auth.NewToken()
		if err = f.st.CreateSession(f.ctx, u.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		return token
	}
	viewer := tokenFor(models.RoleReadOnly)
	if r := f.do(viewer, http.MethodGet, "/api/docker", nil); r.Code != http.StatusOK {
		t.Fatalf("viewer: %d %s", r.Code, r.Body.String())
	}
	body := map[string]any{"agent_id": f.a1, "type": "dockercontainer", "name": "Web", "config": map[string]string{"container": "web", "mode": "healthy"}}
	if r := f.do(viewer, http.MethodPost, "/api/checks", body); r.Code != http.StatusForbidden {
		t.Fatalf("viewer configured check: %d", r.Code)
	}
	if r := f.do(f.admin, http.MethodPost, "/api/checks", body); r.Code != http.StatusCreated {
		t.Fatalf("Docker check create: %d %s", r.Code, r.Body.String())
	}
	if _, err := f.st.CreateRole(f.ctx, &models.Role{Key: "docker_none", Name: "No device access", Permissions: []string{models.PermAuditView}}); err != nil {
		t.Fatal(err)
	}
	denied := tokenFor("docker_none")
	for _, path := range []string{"/api/docker", "/api/devices/" + f.a1 + "/docker"} {
		if r := f.do(denied, http.MethodGet, path, nil); r.Code != http.StatusForbidden {
			t.Fatalf("missing permission: %s %d", path, r.Code)
		}
	}
}
