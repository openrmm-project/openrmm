package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

type fleet struct {
	st                        *Store
	ctx                       context.Context
	client, site              string
	winWS, winSrv, mac, lin   string
	linSrv, otherClientServer string
	otherClient, otherSite    string
}

// newFleet enrols a mixed fleet in one client, plus one server elsewhere:
// a Windows workstation and server (roles from their inventory), a Mac, a
// Linux machine without a role and a Linux machine an administrator marked as
// a server.
func newFleet(t *testing.T, prefix string) fleet {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, prefix, url)
	ctx := context.Background()
	f := fleet{st: st, ctx: ctx}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := st.CreateClient(ctx, "Acme")
	must(err)
	s, err := st.CreateSite(ctx, c.ID, "HQ")
	must(err)
	oc, err := st.CreateClient(ctx, "Other")
	must(err)
	os2, err := st.CreateSite(ctx, oc.ID, "Other HQ")
	must(err)
	f.client, f.site, f.otherClient, f.otherSite = c.ID, s.ID, oc.ID, os2.ID
	enrol := func(host, agentOS, siteID, inventory string) string {
		a, err := st.CreateAgent(ctx, host, agentOS, "amd64", "0.11.0", "tok-"+uuid.NewString(), &siteID, nil)
		must(err)
		if inventory != "" {
			must(st.UpsertInventory(ctx, a.ID, []byte(inventory)))
		}
		return a.ID
	}
	f.winWS = enrol("WS-1", "windows", s.ID, `{"os":"windows","platform":"Microsoft Windows 11 Pro","platform_family":"Standalone Workstation"}`)
	f.winSrv = enrol("SRV-1", "windows", s.ID, `{"os":"windows","platform":"Microsoft Windows Server 2022 Standard"}`)
	f.mac = enrol("MAC-1", "darwin", s.ID, `{"os":"darwin","platform":"darwin"}`)
	f.lin = enrol("LIN-1", "linux", s.ID, `{"os":"linux","platform":"ubuntu"}`)
	f.linSrv = enrol("LIN-SRV", "linux", s.ID, `{"os":"linux","platform":"rocky"}`)
	_, err = st.SetAgentRoleOverride(ctx, f.linSrv, models.RoleServer)
	must(err)
	f.otherClientServer = enrol("OTHER-SRV", "windows", os2.ID, `{"os":"windows","platform_family":"Server"}`)
	return f
}

func (f fleet) reached(t *testing.T, policyID, checkID string) []string {
	t.Helper()
	ids, err := f.st.AgentIDsForPolicy(f.ctx, policyID)
	if err != nil {
		t.Fatal(err)
	}
	covered, err := f.st.CheckCoveredAgents(f.ctx, checkID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	sort.Strings(covered)
	if !slices.Equal(ids, covered) {
		t.Fatalf("run-now targets %v and check coverage %v disagree", ids, covered)
	}
	for _, id := range ids {
		tasks, err := f.st.TasksForAgent(f.ctx, id, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		applies, err := f.st.CheckAppliesToAgent(f.ctx, checkID, id)
		if err != nil || !applies || !slices.ContainsFunc(tasks, func(task models.Task) bool { return task.PolicyID != nil && *task.PolicyID == policyID }) {
			t.Fatalf("%s is reached but does not get the policy's task and check: %v", id, err)
		}
	}
	return ids
}

func sorted(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// Roles come from the inventory and the override; assignments narrowed by OS
// and role reach exactly the matching devices of their scope; a global
// assignment reaches every matching device; exclusions take devices out;
// several assignments add up.
func TestPolicyTargetingReachIntegration(t *testing.T) {
	f := newFleet(t, "policy_target")
	for id, want := range map[string]string{f.winWS: models.RoleWorkstation, f.winSrv: models.RoleServer, f.mac: models.RoleWorkstation, f.lin: "", f.linSrv: models.RoleServer} {
		a, err := f.st.GetAgent(f.ctx, id)
		if err != nil || a.Role != want {
			t.Fatalf("%s role = %q (detected %q, override %q), want %q", a.Hostname, a.Role, a.OSRole, a.RoleOverride, want)
		}
	}
	pol, err := f.st.CreatePolicy(f.ctx, "Servers", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateTask(f.ctx, &models.Task{PolicyID: &pol.ID, Name: "Backup", Shell: "sh", Script: "true",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	check, err := f.st.CreateCheck(f.ctx, memoryCheck(pol.ID, true, ""))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.st.AssignPolicy(f.ctx, pol.ID, models.ScopeClient, f.client, models.TargetOSWindows, models.RoleServer); err != nil {
		t.Fatal(err)
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, sorted(f.winSrv)) {
		t.Fatalf("client · Windows · servers reached %v", got)
	}
	// An administrator's correction takes effect at once.
	if _, err := f.st.SetAgentRoleOverride(f.ctx, f.winWS, models.RoleServer); err != nil {
		t.Fatal(err)
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, sorted(f.winSrv, f.winWS)) {
		t.Fatalf("after overriding WS-1 as a server: %v", got)
	}
	if _, err := f.st.SetAgentRoleOverride(f.ctx, f.winWS, ""); err != nil {
		t.Fatal(err)
	}
	// A second assignment adds up: every Linux device of the fleet, any role.
	if _, err := f.st.AssignPolicy(f.ctx, pol.ID, models.ScopeGlobal, models.GlobalScopeID, models.TargetOSLinux, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, sorted(f.winSrv, f.lin, f.linSrv)) {
		t.Fatalf("plus global · Linux: %v", got)
	}
	// An exception takes one device out, whichever assignment reaches it.
	if err := f.st.AddExclusion(f.ctx, false, pol.ID, f.linSrv, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddExclusion(f.ctx, false, pol.ID, f.linSrv, "admin"); err != nil {
		t.Fatal("excluding twice must not fail:", err)
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, sorted(f.winSrv, f.lin)) {
		t.Fatalf("with LIN-SRV excluded: %v", got)
	}
	// A global server role filter reaches the other client's server too.
	if _, err := f.st.AssignPolicy(f.ctx, pol.ID, models.ScopeGlobal, models.GlobalScopeID, "", models.RoleServer); err != nil {
		t.Fatal(err)
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, sorted(f.winSrv, f.lin, f.otherClientServer)) {
		t.Fatalf("plus global · servers: %v", got)
	}
	if removed, err := f.st.RemoveExclusion(f.ctx, false, pol.ID, f.linSrv); err != nil || !removed {
		t.Fatalf("remove exclusion: %v %v", removed, err)
	}
	excl, err := f.st.ListExclusions(f.ctx, false, pol.ID)
	if err != nil || len(excl) != 0 {
		t.Fatalf("exclusions after removal: %+v %v", excl, err)
	}
	if _, err := f.st.AssignPolicy(f.ctx, pol.ID, models.ScopeClient, f.client, "beos", ""); err == nil {
		t.Fatal("an unknown OS filter was stored")
	}
	if _, err := f.st.pool.Exec(f.ctx, `INSERT INTO policy_assignments (id, policy_id, scope_type, scope_id, os) VALUES ($1,$2,'client',$3,'Windows')`,
		newID(), pol.ID, f.client); err == nil {
		t.Fatal("the database accepted an unknown OS filter")
	}
	// Switched off, it reaches nothing, filters or not.
	if _, err := f.st.DeactivatePolicy(f.ctx, pol.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.reached(t, pol.ID, check.ID); len(got) != 0 {
		t.Fatalf("a switched-off policy reached %v", got)
	}
}

// Server alert rules keep the broadest-scope precedence; at the same level an
// assignment aimed at the device's kind wins over one that is not; an
// exception hands the device back to the next rule.
func TestGoverningRuleTargetingPrecedenceIntegration(t *testing.T) {
	f := newFleet(t, "rule_target")
	rule := func(name, afterMin string) string {
		pol, err := f.st.CreatePolicy(f.ctx, name, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.CreateCheck(f.ctx, &models.Check{PolicyID: &pol.ID, Name: name, Type: models.CheckConnectivity,
			Config: json.RawMessage(`{"after_min":` + afterMin + `}`), IntervalSec: 300, TimeoutSec: 30, FailureCount: 1, RecoveryCount: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		return pol.ID
	}
	broad := rule("client all", "60")
	servers := rule("client servers", "120")
	if _, err := f.st.AssignPolicy(f.ctx, broad, models.ScopeClient, f.client); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AssignPolicy(f.ctx, servers, models.ScopeClient, f.client, "", models.RoleServer); err != nil {
		t.Fatal(err)
	}
	governing := func() map[string]models.GoverningRule {
		g, err := f.st.GoverningRules(f.ctx, models.CheckConnectivity)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	g := governing()
	if g[f.winSrv].PolicyID != servers || g[f.linSrv].PolicyID != servers {
		t.Fatalf("servers should be governed by the filtered rule: %+v / %+v", g[f.winSrv], g[f.linSrv])
	}
	if g[f.winWS].PolicyID != broad || g[f.mac].PolicyID != broad || g[f.lin].PolicyID != broad {
		t.Fatalf("other devices keep the broad rule: %+v", g[f.winWS])
	}
	if err := f.st.AddExclusion(f.ctx, false, servers, f.winSrv, "admin"); err != nil {
		t.Fatal(err)
	}
	if g = governing(); g[f.winSrv].PolicyID != broad {
		t.Fatalf("an excluded server falls back to the broad rule: %+v", g[f.winSrv])
	}
	// The broadest scope still decides: a fleet-wide rule governs everyone.
	fleetWide := rule("fleet", "30")
	if _, err := f.st.AssignPolicy(f.ctx, fleetWide, models.ScopeGlobal, models.GlobalScopeID); err != nil {
		t.Fatal(err)
	}
	if g = governing(); g[f.linSrv].PolicyID != fleetWide || g[f.otherClientServer].PolicyID != fleetWide || g[f.linSrv].ScopeType != models.ScopeGlobal {
		t.Fatalf("a global rule governs every device: %+v", g[f.linSrv])
	}
}

// Patch policies follow the same rules, and the inventory keeps the detected
// role current while an override stays put.
func TestPatchPolicyTargetingAndRoleDetectionIntegration(t *testing.T) {
	f := newFleet(t, "patch_target")
	pp, err := f.st.CreatePatchPolicy(f.ctx, &models.PatchPolicy{Name: "Workstations", Enabled: true, WindowEndMin: 1439})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddPatchPolicyAssignment(f.ctx, pp.ID, models.ScopeSite, f.site, "", models.RoleWorkstation); err != nil {
		t.Fatal(err)
	}
	covers := func(agentID string) bool {
		pols, err := f.st.PatchPoliciesForAgent(f.ctx, agentID)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(pols, func(p models.PatchPolicy) bool { return p.ID == pp.ID })
	}
	if !covers(f.winWS) || !covers(f.mac) || covers(f.winSrv) || covers(f.lin) {
		t.Fatal("site · workstations covers the wrong devices")
	}
	if err := f.st.AddExclusion(f.ctx, true, pp.ID, f.mac, "admin"); err != nil {
		t.Fatal(err)
	}
	if covers(f.mac) {
		t.Fatal("an excluded Mac is still covered")
	}
	// A workstation an administrator marks as a server leaves the workstation
	// policy (any OS can be overridden either way, a Mac or Linux included).
	if _, err := f.st.SetAgentRoleOverride(f.ctx, f.winWS, models.RoleServer); err != nil {
		t.Fatal(err)
	}
	if covers(f.winWS) {
		t.Fatal("an overridden server is still covered as a workstation")
	}
	// A later snapshot claiming another product type does not change the
	// detected role, and the override stays.
	if err := f.st.UpsertInventory(f.ctx, f.winWS, []byte(`{"os":"windows","platform":"Microsoft Windows Server 2025 Standard","platform_family":"Server"}`)); err != nil {
		t.Fatal(err)
	}
	a, err := f.st.GetAgent(f.ctx, f.winWS)
	if err != nil || a.OSRole != models.RoleWorkstation || a.RoleOverride != models.RoleServer {
		t.Fatalf("after a server inventory: %+v %v", a, err)
	}
	if _, err := f.st.SetAgentRoleOverride(f.ctx, f.winWS, "kiosk"); err == nil {
		t.Fatal("an unknown role override was accepted")
	}
	if _, err := f.st.SetAgentRoleOverride(f.ctx, uuid.NewString(), models.RoleServer); err == nil {
		t.Fatal("a missing device was accepted")
	}
}

// Migration 033 keeps what applied applying and detects the roles the existing
// inventory implies.
func TestPolicyTargetingMigrationIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newMigrationTestStore(t, url)
	ctx := context.Background()
	if err := st.migrate(ctx, migrations[:32]); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for host, row := range map[string][2]string{
		"srv":     {"windows", `{"os":"windows","platform":"Microsoft Windows Server 2019 Standard"}`},
		"dc":      {"windows", `{"os":"windows","platform":"X","platform_family":"Server (Domain Controller)"}`},
		"ws":      {"windows", `{"os":"windows","platform":"Microsoft Windows 10 Pro"}`},
		"noinv":   {"windows", ""},
		"mac":     {"darwin", ""},
		"penguin": {"linux", `{"os":"linux","platform":"debian"}`},
	} {
		id := newID()
		ids[host] = id
		if _, err := st.pool.Exec(ctx, `INSERT INTO agents (id, hostname, os, token_hash) VALUES ($1,$2,$3,$4)`, id, host, row[0], "tok-"+id); err != nil {
			t.Fatal(err)
		}
		if row[1] != "" {
			if _, err := st.pool.Exec(ctx, `INSERT INTO inventory (agent_id, data) VALUES ($1,$2)`, id, row[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	policy := newID()
	if _, err := st.pool.Exec(ctx, `INSERT INTO policies (id, name) VALUES ($1,'legacy')`, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO policy_assignments (id, policy_id, scope_type, scope_id) VALUES ($1,$2,'agent',$3)`, newID(), policy, ids["ws"]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for host, want := range map[string]string{"srv": "server", "dc": "server", "ws": "workstation", "noinv": "", "mac": "workstation", "penguin": ""} {
		a, err := st.GetAgent(ctx, ids[host])
		if err != nil || a.OSRole != want || a.RoleOverride != "" {
			t.Fatalf("%s: %+v %v, want role %q", host, a, err, want)
		}
	}
	as, err := st.ListPolicyAssignments(ctx, policy)
	if err != nil || len(as) != 1 || as[0].OS != "" || as[0].Role != "" {
		t.Fatalf("legacy assignment: %+v %v", as, err)
	}
	reached, err := st.AgentIDsForPolicy(ctx, policy)
	if err != nil || !slices.Equal(reached, []string{ids["ws"]}) {
		t.Fatalf("the legacy assignment reaches %v", reached)
	}
	assertCurrentMigrationState(t, st)
}

// Assignments filter on a device's OS and role, so a device must not be able
// to change them by what it reports: re-registering with another OS keeps the
// enrolled one, and a later inventory claiming another product type (or
// another OS) leaves the detected role alone. Only an administrator's override
// moves a device between roles.
func TestDeviceCannotRetargetItselfIntegration(t *testing.T) {
	f := newFleet(t, "device_trust")
	pol, err := f.st.CreatePolicy(f.ctx, "Windows servers", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateTask(f.ctx, &models.Task{PolicyID: &pol.ID, Name: "Backup with a global secret", Shell: "powershell", Script: "exit 0",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	check, err := f.st.CreateCheck(f.ctx, memoryCheck(pol.ID, true, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AssignPolicy(f.ctx, pol.ID, models.ScopeGlobal, models.GlobalScopeID, models.TargetOSWindows, models.RoleServer); err != nil {
		t.Fatal(err)
	}
	want := sorted(f.winSrv, f.otherClientServer)
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, want) {
		t.Fatalf("every Windows server: %v, want %v", got, want)
	}

	// A Linux machine re-registers as Windows and reports a Windows Server
	// inventory; a Windows workstation reports itself a server.
	if err := f.st.RefreshAgentInfo(f.ctx, f.lin, "LIN-1", "windows", "amd64", "0.11.1"); !errors.Is(err, ErrAgentOSChanged) {
		t.Fatalf("an OS change was not reported: %v", err)
	}
	server := []byte(`{"os":"windows","platform":"Microsoft Windows Server 2022 Standard","platform_family":"Server"}`)
	for _, id := range []string{f.lin, f.winWS} {
		if err := f.st.UpsertInventory(f.ctx, id, server); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, want) {
		t.Fatalf("a device retargeted itself: %v, want %v", got, want)
	}
	lin, err := f.st.GetAgent(f.ctx, f.lin)
	if err != nil || lin.OS != "linux" || lin.Version != "0.11.1" || lin.Role != "" {
		t.Fatalf("Linux device after the claims: os=%q version=%q role=%q %v", lin.OS, lin.Version, lin.Role, err)
	}
	ws, err := f.st.GetAgent(f.ctx, f.winWS)
	if err != nil || ws.OSRole != models.RoleWorkstation {
		t.Fatalf("workstation after claiming server: %+v %v", ws, err)
	}

	// The same OS is not an error, and an OS never recorded is filled in.
	if err := f.st.RefreshAgentInfo(f.ctx, f.winWS, "WS-1", "windows", "amd64", "0.11.1"); err != nil {
		t.Fatalf("same OS: %v", err)
	}
	blank, err := f.st.CreateAgent(f.ctx, "BLANK", "", "amd64", "0.11.0", "tok-"+uuid.NewString(), &f.site, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.RefreshAgentInfo(f.ctx, blank.ID, "BLANK", "windows", "amd64", "0.11.0"); err != nil {
		t.Fatalf("filling in an OS: %v", err)
	}
	// A role is detected once, from the enrolled OS, and only an override moves it.
	if err := f.st.UpsertInventory(f.ctx, blank.ID, []byte(`{"os":"windows","platform":"Microsoft Windows 11 Pro"}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertInventory(f.ctx, blank.ID, server); err != nil {
		t.Fatal(err)
	}
	if a, err := f.st.GetAgent(f.ctx, blank.ID); err != nil || a.OS != "windows" || a.Role != models.RoleWorkstation {
		t.Fatalf("filled-in device: os=%q role=%q %v", a.OS, a.Role, err)
	}
	if _, err := f.st.SetAgentRoleOverride(f.ctx, f.winWS, models.RoleServer); err != nil {
		t.Fatal(err)
	}
	if got := f.reached(t, pol.ID, check.ID); !slices.Equal(got, sorted(f.winSrv, f.otherClientServer, f.winWS)) {
		t.Fatalf("the administrator's override did not apply: %v", got)
	}
	// A revoked device's registration changes nothing and is not an error.
	if err := f.st.RevokeAgent(f.ctx, f.mac); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RefreshAgentInfo(f.ctx, f.mac, "MAC-1", "windows", "amd64", "0.11.1"); err != nil {
		t.Fatalf("revoked device: %v", err)
	}
}
