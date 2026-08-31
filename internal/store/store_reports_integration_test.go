package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// TestReportFilterWhere pins the SQL *text*. That is not the same as pinning the
// behaviour: a join that exposed the wrong alias, or a scope clause ORed instead
// of ANDed, would still contain every substring that test looks for and would
// still leak one tenant's machines into another's report.
//
// These run against a real database and assert on rows, because the failure mode
// here is silent and cross-tenant.

type reportFixture struct {
	st            *Store
	ctx           context.Context
	minePrefix    string
	mineHost      string
	theirHost     string
	mineClientID  string
	theirClientID string
	mineSiteID    string
	scopedUser    string // restricted to mineClient
	fullUser      string // no scope rows at all = unrestricted
}

func newReportFixture(t *testing.T) reportFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	// A schema of its own. The isolation assertions were written with unique
	// suffixes and so tolerated a shared schema, but a COUNT cannot: it sees
	// every other test's fixtures too, and "2 machines have nginx" quietly
	// became 4 as soon as a second test in this file ran.
	st := newIsolatedStore(t, "store_reports", url)

	suffix := uuid.NewString()
	f := reportFixture{st: st, ctx: ctx, minePrefix: suffix}

	mine, err := st.CreateClient(ctx, "mine-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreateClient(ctx, "theirs-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	mineSite, err := st.CreateSite(ctx, mine.ID, "mine-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirSite, err := st.CreateSite(ctx, theirs.ID, "their-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	f.mineClientID, f.theirClientID, f.mineSiteID = mine.ID, theirs.ID, mineSite.ID

	f.mineHost = "mine-host-" + suffix
	f.theirHost = "their-host-" + suffix
	mineAgent, err := st.CreateAgent(ctx, f.mineHost, "linux", "amd64", "1.0.0", "tok-mine-"+suffix, &mineSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	theirAgent, err := st.CreateAgent(ctx, f.theirHost, "linux", "amd64", "1.0.0", "tok-theirs-"+suffix, &theirSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Both machines report the same package, so a leak cannot hide behind a
	// difference in the data itself.
	inv := []byte(`{"software":[{"name":"nginx","version":"1.25.0","manufacturer":"nginx"}],
	                "hardware":{"manufacturer":"Dell","model":"OptiPlex","serial":"S1"},
	                "cpu_model":"Xeon","cpu_cores":4,"mem_total_bytes":8589934592}`)
	for _, id := range []string{mineAgent.ID, theirAgent.ID} {
		if err := st.UpsertInventory(ctx, id, inv); err != nil {
			t.Fatal(err)
		}
	}

	scoped, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: mine.ID}})
	if err != nil {
		t.Fatal(err)
	}
	full, err := st.CreateUserWithScopes(ctx, "full-"+suffix, "hash", models.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.scopedUser, f.fullUser = scoped.ID, full.ID
	return f
}

// hosts collapses any report to the set of hostnames it exposed, which is the
// only thing that matters for isolation.
func hostsIn(rows any) map[string]bool {
	out := map[string]bool{}
	switch v := rows.(type) {
	case []SoftwareRow:
		for _, r := range v {
			out[r.Hostname] = true
		}
	case []HardwareRow:
		for _, r := range v {
			out[r.Hostname] = true
		}
	case []MachineRow:
		for _, r := range v {
			out[r.Hostname] = true
		}
	}
	return out
}

func (f reportFixture) allReports(t *testing.T, filter ReportFilter) map[string]map[string]bool {
	t.Helper()
	sw, err := f.st.SoftwareReport(f.ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	hw, err := f.st.HardwareReport(f.ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	mach, err := f.st.MachinesReport(f.ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]map[string]bool{
		"software": hostsIn(sw),
		"hardware": hostsIn(hw),
		"machines": hostsIn(mach),
	}
}

// The base case: a technician restricted to one company sees that company's
// machines in every report, and never the other company's.
func TestReportsExcludeOutOfScopeMachinesIntegration(t *testing.T) {
	f := newReportFixture(t)

	for name, hosts := range f.allReports(t, ReportFilter{ScopeUserID: f.scopedUser}) {
		if !hosts[f.mineHost] {
			t.Errorf("%s report hid an in-scope machine (%s)", name, f.mineHost)
		}
		if hosts[f.theirHost] {
			t.Errorf("%s report leaked an out-of-scope machine (%s)", name, f.theirHost)
		}
	}

	// The control: unrestricted principals still see both, so the assertion above
	// is measuring the scope and not an empty database.
	for name, hosts := range f.allReports(t, ReportFilter{ScopeUserID: f.fullUser}) {
		if !hosts[f.mineHost] || !hosts[f.theirHost] {
			t.Errorf("%s report should show the whole fleet to an unscoped user, got %v", name, hosts)
		}
	}
}

// The attack this is really about: a restricted user naming another tenant's
// client id in scope_type/scope_id. The access scope and the requested filter
// are ANDed, so the answer is empty — not the other tenant's fleet.
func TestReportFilterCannotEscapeTheAccessScopeIntegration(t *testing.T) {
	f := newReportFixture(t)

	filter := ReportFilter{ScopeUserID: f.scopedUser, ScopeType: "client", ScopeID: f.theirClientID}
	for name, hosts := range f.allReports(t, filter) {
		if len(hosts) != 0 {
			t.Errorf("%s report answered a cross-tenant filter with %v, want nothing", name, hosts)
		}
	}

	// And the same filter is honoured — not ignored — for someone entitled to it.
	for name, hosts := range f.allReports(t, ReportFilter{ScopeUserID: f.fullUser, ScopeType: "client", ScopeID: f.theirClientID}) {
		if !hosts[f.theirHost] || hosts[f.mineHost] {
			t.Errorf("%s report did not apply the client filter for an unscoped user, got %v", name, hosts)
		}
	}
}

// Narrowing to one's own scope must still work: a filter is a filter, not a
// second access check that cancels the first.
func TestReportFilterWithinScopeStillReturnsRowsIntegration(t *testing.T) {
	f := newReportFixture(t)

	filter := ReportFilter{ScopeUserID: f.scopedUser, ScopeType: "site", ScopeID: f.mineSiteID}
	for name, hosts := range f.allReports(t, filter) {
		if !hosts[f.mineHost] {
			t.Errorf("%s report dropped an in-scope machine when filtered to its own site", name)
		}
		if hosts[f.theirHost] {
			t.Errorf("%s report leaked across tenants under a site filter: %v", name, hosts)
		}
	}
}

// A restricted user's rows must name their own company, never carry a joined
// label from the neighbouring tenant. This catches a join that reaches the wrong
// client through the site chain.
func TestReportRowsCarryTheirOwnTenantLabelsIntegration(t *testing.T) {
	f := newReportFixture(t)

	rows, err := f.st.SoftwareReport(f.ctx, ReportFilter{ScopeUserID: f.scopedUser})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows: the fixture did not produce a software report")
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Client, "mine-") {
			t.Errorf("row for %s is labelled with client %q", r.Hostname, r.Client)
		}
	}
}

// Counting in the database is the point: "how many machines have nginx" costs
// one query and is exact, where a model reading a thousand rows to count them
// costs a context window and produces a number that only looks right.
func TestInventorySummaryCountsMachinesNotRowsIntegration(t *testing.T) {
	f := newReportFixture(t)

	// Both machines report the same package, so a summary must say two machines
	// and not two unrelated rows.
	groups, _, err := f.st.InventorySummary(f.ctx, ReportFilter{ScopeUserID: f.fullUser}, "software", "name", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("match=nginx returned %d groups, want 1: %+v", len(groups), groups)
	}
	if groups[0].Machines != 2 {
		t.Errorf("machines = %d, want 2 — the count must be distinct devices", groups[0].Machines)
	}
}

// The summary is filtered by the caller's access scope like everything else. A
// count that quietly included another tenant would be worse than a leaked row:
// it looks authoritative and nothing in it reveals the extra data.
func TestInventorySummaryRespectsScopeIntegration(t *testing.T) {
	f := newReportFixture(t)

	scoped, _, err := f.st.InventorySummary(f.ctx, ReportFilter{ScopeUserID: f.scopedUser}, "software", "name", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].Machines != 1 {
		t.Fatalf("a user scoped to one company counted %+v; want a single machine", scoped)
	}

	// And an out-of-scope filter cannot widen it.
	cross, _, err := f.st.InventorySummary(f.ctx,
		ReportFilter{ScopeUserID: f.scopedUser, ScopeType: "client", ScopeID: f.theirClientID},
		"software", "name", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cross) != 0 {
		t.Errorf("a cross-tenant summary returned %+v, want nothing", cross)
	}
}

// group_by comes from the caller, so it must select from a whitelist rather than
// reach SQL as text — a report parameter that becomes a column name is how an
// endpoint turns into an injection point.
func TestInventorySummaryRejectsUnknownGroupIntegration(t *testing.T) {
	f := newReportFixture(t)

	_, _, err := f.st.InventorySummary(f.ctx, ReportFilter{}, "software", "name; DROP TABLE agents", "")
	if err == nil {
		t.Fatal("an arbitrary group_by was accepted")
	}
	// The error should say what IS allowed, not only that this was not.
	if !strings.Contains(err.Error(), "try one of:") {
		t.Errorf("the refusal should list the options: %v", err)
	}
	// The table is obviously still there, but assert it rather than assume.
	var n int
	if err := f.st.pool.QueryRow(f.ctx, `SELECT count(*) FROM agents`).Scan(&n); err != nil {
		t.Fatalf("agents table is gone: %v", err)
	}
}

// Machines are counted from the agent list, not from reported inventory: a
// device that has never checked in is still part of the fleet, and leaving it
// out would under-report exactly the machines someone is looking for.
func TestMachinesSummaryIncludesDevicesWithoutInventoryIntegration(t *testing.T) {
	f := newReportFixture(t)
	site, err := f.st.CreateSite(f.ctx, f.mineClientID, "quiet-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateAgent(f.ctx, "never-reported-"+uuid.NewString(),
		"linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &site.ID, nil); err != nil {
		t.Fatal(err)
	}

	groups, _, err := f.st.InventorySummary(f.ctx, ReportFilter{ScopeUserID: f.fullUser}, "machines", "os", "")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, g := range groups {
		total += g.Machines
	}
	if total < 3 {
		t.Errorf("machines summary counted %d; the device that never reported inventory is missing", total)
	}
}
