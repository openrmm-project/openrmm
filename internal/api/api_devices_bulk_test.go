package api

import (
	"context"
	"encoding/json"
	"fmt"
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

// Bulk move and delete, and the proposals the AI writes for them, through the
// router: permission and scope middleware are where these are allowed or not.

type bulkFixture struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	handler http.Handler
	admin   string // unrestricted admin
	scoped  string // admin role confined to company A
	tech    string // unrestricted tech: neither clients.manage nor devices.delete
	siteA1  *models.Site
	siteA2  *models.Site
	deptA2  *models.DeviceGroup
	siteB   *models.Site
	a1, a2  string
	b1      string
}

func newBulkFixture(t *testing.T) *bulkFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st := newTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &bulkFixture{t: t, ctx: ctx, st: st}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	clientA, err := st.CreateClient(ctx, "a-"+uuid.NewString())
	must(err)
	clientB, err := st.CreateClient(ctx, "b-"+uuid.NewString())
	must(err)
	f.siteA1, err = st.CreateSite(ctx, clientA.ID, "a1")
	must(err)
	f.siteA2, err = st.CreateSite(ctx, clientA.ID, "a2")
	must(err)
	f.deptA2, err = st.CreateGroup(ctx, "finance", &f.siteA2.ID)
	must(err)
	f.siteB, err = st.CreateSite(ctx, clientB.ID, "b")
	must(err)
	agent := func(siteID string) string {
		a, err := st.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &siteID, nil)
		must(err)
		return a.ID
	}
	f.a1, f.a2, f.b1 = agent(f.siteA1.ID), agent(f.siteA1.ID), agent(f.siteB.ID)

	session := func(u *models.User) string {
		token := auth.NewToken()
		must(st.CreateSession(ctx, u.ID, auth.Hash(token), time.Now().Add(time.Hour)))
		return token
	}
	admin, err := st.CreateUser(ctx, "admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	scoped, err := st.CreateUserWithScopes(ctx, "scoped-"+uuid.NewString(), "hash", models.RoleAdmin,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: clientA.ID}})
	must(err)
	tech, err := st.CreateUser(ctx, "tech-"+uuid.NewString(), "hash", models.RoleTech)
	must(err)
	f.admin, f.scoped, f.tech = session(admin), session(scoped), session(tech)

	f.handler = New(&config.Config{APIRateLimit: 10000}, st, hub.New(), nil, nil, remote.NewRelay(), nil, nil, nil).Router()
	return f
}

func (f *bulkFixture) do(token, method, target string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var payload string
	if body != nil {
		b, _ := json.Marshal(body)
		payload = string(b)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *bulkFixture) siteOf(id string) string {
	f.t.Helper()
	a, err := f.st.GetAgent(f.ctx, id)
	if err != nil {
		f.t.Fatalf("device %s: %v", id, err)
	}
	return deref(a.SiteID)
}

func (f *bulkFixture) exists(id string) bool {
	_, err := f.st.GetAgent(f.ctx, id)
	return err == nil
}

func (f *bulkFixture) audits(action, target string) []models.AuditEntry {
	f.t.Helper()
	entries, _, err := f.st.ListAudit(f.ctx, store.AuditFilter{Limit: 500})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []models.AuditEntry
	for _, e := range entries {
		if e.Action == action && (target == "" || e.Target == target) {
			out = append(out, e)
		}
	}
	return out
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body.String())
	}
}

func TestBulkMoveDevicesIntegration(t *testing.T) {
	f := newBulkFixture(t)
	rec := f.do(f.admin, http.MethodPost, "/api/devices/bulk/move",
		map[string]any{"agent_ids": []string{f.a1, f.a2, f.a1}, "site_id": f.siteA2.ID, "group_id": f.deptA2.ID})
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"moved":2`) {
		t.Fatalf("a repeated id counted twice: %s", rec.Body.String())
	}
	for _, id := range []string{f.a1, f.a2} {
		if f.siteOf(id) != f.siteA2.ID {
			t.Fatalf("%s was not moved", id)
		}
		if len(f.audits("device.move", id)) != 1 {
			t.Fatalf("%s: want one device.move audit entry", id)
		}
	}
}

func TestBulkMoveStaysInsideTheCallersScopeIntegration(t *testing.T) {
	f := newBulkFixture(t)
	// One device of another company in the list: nothing moves.
	rec := f.do(f.scoped, http.MethodPost, "/api/devices/bulk/move",
		map[string]any{"agent_ids": []string{f.a1, f.b1}, "site_id": f.siteA2.ID})
	wantStatus(t, rec, http.StatusForbidden)
	if f.siteOf(f.a1) != f.siteA1.ID {
		t.Fatal("a refused request still moved the device inside the scope")
	}
	// Its own devices, into another company: refused too.
	rec = f.do(f.scoped, http.MethodPost, "/api/devices/bulk/move",
		map[string]any{"agent_ids": []string{f.a1}, "site_id": f.siteB.ID})
	wantStatus(t, rec, http.StatusForbidden)
	if f.siteOf(f.a1) != f.siteA1.ID {
		t.Fatal("moved a device out of the caller's company")
	}
	// A technician without clients.manage cannot reorganise at all.
	wantStatus(t, f.do(f.tech, http.MethodPost, "/api/devices/bulk/move",
		map[string]any{"agent_ids": []string{f.a1}, "site_id": f.siteA2.ID}), http.StatusForbidden)
}

func TestBulkDeleteNeedsTheRightCountIntegration(t *testing.T) {
	f := newBulkFixture(t)
	rec := f.do(f.admin, http.MethodPost, "/api/devices/bulk/delete",
		map[string]any{"agent_ids": []string{f.a1, f.a2}, "confirm_count": 1})
	wantStatus(t, rec, http.StatusBadRequest)
	if !f.exists(f.a1) || !f.exists(f.a2) {
		t.Fatal("a mismatched confirmation deleted something")
	}
	rec = f.do(f.admin, http.MethodPost, "/api/devices/bulk/delete",
		map[string]any{"agent_ids": []string{f.a1, f.a2}, "confirm_count": 2})
	wantStatus(t, rec, http.StatusOK)
	if f.exists(f.a1) || f.exists(f.a2) || !f.exists(f.b1) {
		t.Fatal("deleted the wrong set")
	}
	if len(f.audits("device.delete", f.a1)) != 1 {
		t.Fatal("no device.delete audit entry")
	}
	wantStatus(t, f.do(f.tech, http.MethodPost, "/api/devices/bulk/delete",
		map[string]any{"agent_ids": []string{f.b1}, "confirm_count": 1}), http.StatusForbidden)
}

func TestBulkRequestValidationIntegration(t *testing.T) {
	f := newBulkFixture(t)
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/devices/bulk/delete",
		map[string]any{"agent_ids": []string{"not-a-uuid"}, "confirm_count": 1}), http.StatusBadRequest)
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/devices/bulk/delete",
		map[string]any{"agent_ids": []string{}, "confirm_count": 0}), http.StatusBadRequest)
	many := make([]string, store.MaxBulkDevices+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/devices/bulk/delete",
		map[string]any{"agent_ids": many, "confirm_count": len(many)}), http.StatusBadRequest)
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/devices/bulk/move",
		map[string]any{"agent_ids": []string{f.a1, uuid.NewString()}, "site_id": f.siteA2.ID}), http.StatusNotFound)
	if f.siteOf(f.a1) != f.siteA1.ID {
		t.Fatal("a move naming an unknown device was partially applied")
	}
}

// The reported defect: deleting a site with devices said "Internal error".
func TestDeletingASiteWithDevicesExplainsWhyIntegration(t *testing.T) {
	f := newBulkFixture(t)
	rec := f.do(f.admin, http.MethodDelete, "/api/sites/"+f.siteA1.ID, nil)
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "still has 2 devices") {
		t.Fatalf("the refusal does not say why: %s", rec.Body.String())
	}
	wantStatus(t, f.do(f.admin, http.MethodDelete, "/api/sites/"+f.siteA2.ID, nil), http.StatusOK)
	wantStatus(t, f.do(f.admin, http.MethodDelete, "/api/sites/"+uuid.NewString(), nil), http.StatusNotFound)
}

func (f *bulkFixture) propose(kind string, ids []string, siteID, groupID *string) *models.DeviceOperation {
	f.t.Helper()
	op, err := f.st.CreateDeviceOperation(f.ctx, &models.DeviceOperation{
		Kind: kind, AgentIDs: ids, SiteID: siteID, GroupID: groupID, ProposedBy: "ai:assistant", Description: "tidy up",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f *bulkFixture) opStatus(id string) string {
	f.t.Helper()
	op, err := f.st.GetDeviceOperation(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return op.Status
}

func TestApprovingAMoveProposalIntegration(t *testing.T) {
	f := newBulkFixture(t)
	op := f.propose(models.DeviceOpMove, []string{f.a1, f.a2}, &f.siteA2.ID, &f.deptA2.ID)
	if f.siteOf(f.a1) != f.siteA1.ID {
		t.Fatal("proposing moved the device")
	}

	// Without the permission the move needs: refused, and still waiting.
	wantStatus(t, f.do(f.tech, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", nil), http.StatusForbidden)
	if f.opStatus(op.ID) != "awaiting_approval" {
		t.Fatal("a refused approval changed the proposal")
	}

	rec := f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", nil)
	wantStatus(t, rec, http.StatusOK)
	if f.siteOf(f.a1) != f.siteA2.ID || f.siteOf(f.a2) != f.siteA2.ID {
		t.Fatal("the approved move did not happen")
	}
	done, _ := f.st.GetDeviceOperation(f.ctx, op.ID)
	if done.Status != "completed" || done.DecidedBy == "" || done.DecidedBy == "ai:assistant" {
		t.Fatalf("recorded as %s by %q", done.Status, done.DecidedBy)
	}
	// Each device's entry says who asked and who approved.
	moves := f.audits("device.move", f.a1)
	if len(moves) != 1 || !strings.Contains(string(moves[0].Detail), op.ID) || !strings.Contains(string(moves[0].Detail), "ai:assistant") {
		t.Fatalf("device.move audit does not carry the proposal: %+v", moves)
	}
	if len(f.audits("device_operation.approve", op.ID)) != 1 {
		t.Fatal("no approval audit entry")
	}
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", nil), http.StatusConflict)
}

// A proposal is a request, not a delegation: the approver is held to their own
// scope, whatever the proposal says.
func TestAScopedApproverCannotReleaseWhatIsOutsideTheirScopeIntegration(t *testing.T) {
	f := newBulkFixture(t)
	outside := f.propose(models.DeviceOpMove, []string{f.a1, f.b1}, &f.siteA2.ID, nil)
	inside := f.propose(models.DeviceOpMove, []string{f.a1}, &f.siteA2.ID, nil)
	intoOther := f.propose(models.DeviceOpMove, []string{f.a2}, &f.siteB.ID, nil)

	rec := f.do(f.scoped, http.MethodGet, "/api/device-operations", nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), outside.ID) {
		t.Fatal("a scoped person was shown a proposal naming another company's device")
	}
	if !strings.Contains(rec.Body.String(), inside.ID) {
		t.Fatal("a scoped person was not shown a proposal within their scope")
	}
	wantStatus(t, f.do(f.scoped, http.MethodPost, "/api/device-operations/"+outside.ID+"/approve", nil), http.StatusNotFound)
	wantStatus(t, f.do(f.scoped, http.MethodPost, "/api/device-operations/"+intoOther.ID+"/approve", nil), http.StatusForbidden)
	if f.siteOf(f.b1) != f.siteB.ID || f.siteOf(f.a2) != f.siteA1.ID {
		t.Fatal("a device moved across companies")
	}
	if f.opStatus(outside.ID) != "awaiting_approval" || f.opStatus(intoOther.ID) != "awaiting_approval" {
		t.Fatal("a refused approval consumed the proposal")
	}
	wantStatus(t, f.do(f.scoped, http.MethodPost, "/api/device-operations/"+inside.ID+"/approve", nil), http.StatusOK)
}

func TestApprovingADeleteProposalIntegration(t *testing.T) {
	f := newBulkFixture(t)
	op := f.propose(models.DeviceOpDelete, []string{f.a1, f.a2}, nil, nil)
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", nil), http.StatusBadRequest)
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", map[string]int{"confirm_count": 1}), http.StatusBadRequest)
	if f.opStatus(op.ID) != "awaiting_approval" || !f.exists(f.a1) {
		t.Fatal("an unconfirmed approval deleted something")
	}
	rec := f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", map[string]int{"confirm_count": 2})
	wantStatus(t, rec, http.StatusOK)
	if f.exists(f.a1) || f.exists(f.a2) {
		t.Fatal("the approved deletion did not happen")
	}
	if got := f.opStatus(op.ID); got != "completed" {
		t.Fatalf("status %s", got)
	}
}

func TestRejectingAProposalIntegration(t *testing.T) {
	f := newBulkFixture(t)
	op := f.propose(models.DeviceOpDelete, []string{f.a1}, nil, nil)
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/reject", nil), http.StatusOK)
	if !f.exists(f.a1) || f.opStatus(op.ID) != "rejected" {
		t.Fatal("rejecting did not leave the device alone")
	}
	wantStatus(t, f.do(f.admin, http.MethodPost, "/api/device-operations/"+op.ID+"/approve", map[string]int{"confirm_count": 1}), http.StatusConflict)
	if !f.exists(f.a1) {
		t.Fatal("a rejected proposal was carried out")
	}
	wantStatus(t, f.do(f.admin, http.MethodPost, fmt.Sprintf("/api/device-operations/%s/reject", uuid.NewString()), nil), http.StatusNotFound)
}
