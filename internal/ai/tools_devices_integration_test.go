package ai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// What these pin: the AI can ask for devices to be moved or deleted, and that
// is all it can do. A proposal changes no device, names only what its user can
// see, and exists only for users allowed to do the thing by hand.

type deviceToolFixture struct {
	ctx                   context.Context
	st                    *store.Store
	mineSite, mineSite2   string
	mineDept, theirDept   string
	theirSite             string
	mineAgent, theirAgent string
	scopedUser            string
}

func newDeviceToolFixture(t *testing.T) deviceToolFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st, err := store.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	suffix := uuid.NewString()
	mine, err := st.CreateClient(ctx, "mine-"+suffix)
	must(err)
	theirs, err := st.CreateClient(ctx, "theirs-"+suffix)
	must(err)
	ms, err := st.CreateSite(ctx, mine.ID, "hq")
	must(err)
	ms2, err := st.CreateSite(ctx, mine.ID, "branch")
	must(err)
	ts, err := st.CreateSite(ctx, theirs.ID, "their-hq")
	must(err)
	md, err := st.CreateGroup(ctx, "finance", &ms2.ID)
	must(err)
	td, err := st.CreateGroup(ctx, "sales", &ts.ID)
	must(err)
	ma, err := st.CreateAgent(ctx, "mine-"+suffix, "linux", "amd64", "1.0.0", "t1-"+suffix, &ms.ID, nil)
	must(err)
	ta, err := st.CreateAgent(ctx, "theirs-"+suffix, "linux", "amd64", "1.0.0", "t2-"+suffix, &ts.ID, nil)
	must(err)
	u, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleAdmin,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: mine.ID}})
	must(err)
	return deviceToolFixture{ctx: ctx, st: st, mineSite: ms.ID, mineSite2: ms2.ID, mineDept: md.ID, theirDept: td.ID,
		theirSite: ts.ID, mineAgent: ma.ID, theirAgent: ta.ID, scopedUser: u.ID}
}

func (f deviceToolFixture) tc(scoped bool) ToolContext {
	tc := ToolContext{Store: f.st, Actor: "alice", Permissions: map[string]bool{
		models.PermClientsView: true, models.PermClientsManage: true, models.PermDevicesDelete: true, models.PermDevicesView: true,
	}}
	if scoped {
		tc.ScopeUserID = f.scopedUser
	}
	return tc
}

func call(t *testing.T, tc ToolContext, name string, args any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	res := Execute(context.Background(), tc, name, raw)
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("%s returned %q", name, res.Output)
	}
	return out
}

func TestProposingAMoveChangesNothingIntegration(t *testing.T) {
	f := newDeviceToolFixture(t)
	out := call(t, f.tc(true), "propose_move_devices", map[string]any{
		"agent_ids": []string{f.mineAgent}, "site_id": f.mineSite2, "group_id": f.mineDept, "description": "consolidate",
	})
	id, _ := out["operation_id"].(string)
	if id == "" || out["status"] != "awaiting_approval" {
		t.Fatalf("unexpected result %v", out)
	}
	if msg, _ := out["message"].(string); !strings.Contains(msg, "NOTHING HAS CHANGED") {
		t.Fatalf("the model is not told nothing happened: %q", msg)
	}
	a, _ := f.st.GetAgent(f.ctx, f.mineAgent)
	if *a.SiteID != f.mineSite {
		t.Fatal("proposing moved the device")
	}
	op, err := f.st.GetDeviceOperation(f.ctx, id)
	if err != nil || op.ProposedBy != "ai:alice" || op.DecidedBy != "" {
		t.Fatalf("stored %+v, %v", op, err)
	}
}

func TestProposingADeleteChangesNothingIntegration(t *testing.T) {
	f := newDeviceToolFixture(t)
	out := call(t, f.tc(false), "propose_delete_devices", map[string]any{
		"agent_ids": []string{f.mineAgent, f.theirAgent}, "description": "retired laptops",
	})
	if out["operation_id"] == nil || out["devices"] != float64(2) {
		t.Fatalf("unexpected result %v", out)
	}
	for _, id := range []string{f.mineAgent, f.theirAgent} {
		if _, err := f.st.GetAgent(f.ctx, id); err != nil {
			t.Fatalf("proposing deleted %s", id)
		}
	}
}

// Proposals are confined to what the user can see, so the AI cannot put a
// request in front of an approver that its own user could never have made.
func TestProposalsStayInsideTheUsersScopeIntegration(t *testing.T) {
	f := newDeviceToolFixture(t)
	scoped := f.tc(true)
	cases := map[string]struct {
		tool string
		args map[string]any
	}{
		"another company's device":     {"propose_delete_devices", map[string]any{"agent_ids": []string{f.theirAgent}, "description": "x"}},
		"into another company":         {"propose_move_devices", map[string]any{"agent_ids": []string{f.mineAgent}, "site_id": f.theirSite, "description": "x"}},
		"another company's department": {"propose_move_devices", map[string]any{"agent_ids": []string{f.mineAgent}, "site_id": f.mineSite2, "group_id": f.theirDept, "description": "x"}},
	}
	for name, c := range cases {
		out := call(t, scoped, c.tool, c.args)
		if out["error"] == nil || out["operation_id"] != nil {
			t.Errorf("%s: proposal accepted: %v", name, out)
		}
	}
}

func TestProposalValidationIntegration(t *testing.T) {
	f := newDeviceToolFixture(t)
	tc := f.tc(false)
	bad := map[string]map[string]any{
		"a department of another site": {"agent_ids": []string{f.mineAgent}, "site_id": f.mineSite, "group_id": f.mineDept, "description": "x"},
		"no destination":               {"agent_ids": []string{f.mineAgent}, "site_id": "", "description": "x"},
		"an unknown device":            {"agent_ids": []string{uuid.NewString()}, "site_id": f.mineSite2, "description": "x"},
		"a made-up id":                 {"agent_ids": []string{"laptop-01"}, "site_id": f.mineSite2, "description": "x"},
		"no devices":                   {"agent_ids": []string{}, "site_id": f.mineSite2, "description": "x"},
	}
	for name, args := range bad {
		if out := call(t, tc, "propose_move_devices", args); out["error"] == nil {
			t.Errorf("%s: accepted: %v", name, out)
		}
	}
}

// The tools are offered only to users who could do the same thing by hand.
func TestDeviceToolsFollowPermissionsIntegration(t *testing.T) {
	f := newDeviceToolFixture(t)
	names := func(perms map[string]bool) map[string]bool {
		out := map[string]bool{}
		for _, t := range ToolsForPermissions(perms) {
			out[t.Function.Name] = true
		}
		return out
	}
	viewer := names(map[string]bool{models.PermDevicesView: true, models.PermClientsView: true})
	if viewer["propose_move_devices"] || viewer["propose_delete_devices"] || !viewer["list_org"] {
		t.Fatalf("a read-only user was offered %v", viewer)
	}
	mover := names(map[string]bool{models.PermClientsManage: true})
	if !mover["propose_move_devices"] || mover["propose_delete_devices"] {
		t.Fatalf("clients.manage alone offered %v", mover)
	}
	// Called anyway, without the permission: refused before anything is stored.
	tc := f.tc(false)
	tc.Permissions = map[string]bool{models.PermClientsManage: true}
	out := call(t, tc, "propose_delete_devices", map[string]any{"agent_ids": []string{f.mineAgent}, "description": "x"})
	if out["error"] == nil {
		t.Fatalf("delete proposed without devices.delete: %v", out)
	}
}

func TestListOrgIsScopedIntegration(t *testing.T) {
	f := newDeviceToolFixture(t)
	raw, _ := json.Marshal(call(t, f.tc(true), "list_org", map[string]any{}))
	body := string(raw)
	if !strings.Contains(body, f.mineSite2) || !strings.Contains(body, f.mineDept) {
		t.Fatalf("own organisation missing: %s", body)
	}
	if strings.Contains(body, f.theirSite) || strings.Contains(body, f.theirDept) {
		t.Fatalf("another company leaked into a scoped listing: %s", body)
	}
}
