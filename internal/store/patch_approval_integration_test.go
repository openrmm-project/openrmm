package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// patchApprovalFixture is a schema of its own (the catalog is fleet-wide, so
// another test's patches would change its counts) with two clients, one site
// each.
type patchApprovalFixture struct {
	st           *Store
	ctx          context.Context
	siteA, siteB string
	clientB      string
}

func newPatchApprovalFixture(t *testing.T) patchApprovalFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st := newIsolatedStore(t, "patch_approval", url)
	f := patchApprovalFixture{st: st, ctx: ctx}
	clientA, err := st.CreateClient(ctx, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := st.CreateClient(ctx, "client-b")
	if err != nil {
		t.Fatal(err)
	}
	siteA, err := st.CreateSite(ctx, clientA.ID, "site-a")
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := st.CreateSite(ctx, clientB.ID, "site-b")
	if err != nil {
		t.Fatal(err)
	}
	f.siteA, f.siteB, f.clientB = siteA.ID, siteB.ID, clientB.ID
	return f
}

func (f patchApprovalFixture) agent(t *testing.T, host, siteID string) string {
	t.Helper()
	a, err := f.st.CreateAgent(f.ctx, host, "linux", "amd64", "1.0.0", "hash-"+host, &siteID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// report is one scan: the agent says these packages are missing.
func (f patchApprovalFixture) report(t *testing.T, agentID string, identifiers ...string) {
	t.Helper()
	rep := &protocol.PatchReportMsg{OS: "linux"}
	for _, id := range identifiers {
		rep.Patches = append(rep.Patches, protocol.PatchItem{Identifier: id, Title: id, Classification: protocol.PatchSecurity})
	}
	if err := f.st.UpsertPatchReport(f.ctx, agentID, rep); err != nil {
		t.Fatal(err)
	}
}

func (f patchApprovalFixture) status(t *testing.T, agentID, identifier string) string {
	t.Helper()
	rows, err := f.st.ListAgentPatches(f.ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Identifier == identifier {
			return r.Status
		}
	}
	t.Fatalf("%s is not reported on %s", identifier, agentID)
	return ""
}

func (f patchApprovalFixture) catalog(t *testing.T, approval string) map[string]PatchCatalogRow {
	t.Helper()
	rows, err := f.st.PatchCatalog(f.ctx, ReportFilter{}, approval)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]PatchCatalogRow{}
	for _, r := range rows {
		out[r.Identifier] = r
	}
	return out
}

func (f patchApprovalFixture) patchID(t *testing.T, identifier string) string {
	t.Helper()
	r, ok := f.catalog(t, "")[identifier]
	if !ok {
		t.Fatalf("%s is not in the catalog", identifier)
	}
	return r.ID
}

func (f patchApprovalFixture) decide(t *testing.T, identifier, scopeType, scopeID, decision string) int {
	t.Helper()
	n, err := f.st.SetApproval(f.ctx, f.patchID(t, identifier), scopeType, scopeID, decision, "manual", "operator")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func wantStatus(t *testing.T, f patchApprovalFixture, agentID, identifier, want string) {
	t.Helper()
	if got := f.status(t, agentID, identifier); got != want {
		t.Fatalf("%s on %s is %q, want %q", identifier, agentID, got, want)
	}
}

func wantApproval(t *testing.T, f patchApprovalFixture, identifier, want string) PatchCatalogRow {
	t.Helper()
	r, ok := f.catalog(t, "")[identifier]
	if !ok {
		t.Fatalf("%s is not in the catalog", identifier)
	}
	if r.Approval != want {
		t.Fatalf("%s reads %q in the catalog, want %q (%+v)", identifier, r.Approval, want, r)
	}
	return r
}

// The reported bug: an approved patch stopped showing as approved once its
// install was claimed or failed, and approving it again changed nothing,
// because failed rows were outside what an approval touches.
func TestApprovedPatchStaysApprovedThroughItsInstallIntegration(t *testing.T) {
	f := newPatchApprovalFixture(t)
	a1 := f.agent(t, "a1", f.siteA)
	f.report(t, a1, "openssl")

	if n := f.decide(t, "openssl", "", "", "approve"); n != 1 {
		t.Fatalf("approval changed %d devices, want 1", n)
	}
	wantApproval(t, f, "openssl", PatchApprovalApproved)

	claimed, err := f.st.ClaimApprovedPatches(f.ctx, a1, nil)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if r := wantApproval(t, f, "openssl", PatchApprovalApproved); r.InstallingCount != 1 {
		t.Fatalf("installing count = %d, want 1", r.InstallingCount)
	}

	if err := f.st.ApplyPatchResult(f.ctx, a1, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{
		{Identifier: "openssl", Success: false, Error: "dpkg was interrupted"},
	}}); err != nil {
		t.Fatal(err)
	}
	if r := wantApproval(t, f, "openssl", PatchApprovalApproved); r.FailedCount != 1 {
		t.Fatalf("failed count = %d, want 1", r.FailedCount)
	}

	// Approving again is how an operator retries a failed install.
	if n := f.decide(t, "openssl", "", "", "approve"); n != 1 {
		t.Fatalf("re-approving a failed install changed %d devices, want 1", n)
	}
	wantStatus(t, f, a1, "openssl", models.PatchStatusApproved)
	rows, _ := f.st.ListAgentPatches(f.ctx, a1)
	if rows[0].Error != "" {
		t.Fatalf("the old failure %q is still on the retried patch", rows[0].Error)
	}
	// Approving what is already approved changes nothing, and says so.
	if n := f.decide(t, "openssl", "", "", "approve"); n != 0 {
		t.Fatalf("a repeated approval changed %d devices, want 0", n)
	}
}

// A device that reports the patch after the decision takes the decision. It
// used to land in 'missing', so a fleet-wide approval read as partial (and its
// device was never installed) as soon as one more machine scanned.
func TestLateReporterTakesTheRecordedDecisionIntegration(t *testing.T) {
	f := newPatchApprovalFixture(t)
	a1 := f.agent(t, "a1", f.siteA)
	f.report(t, a1, "openssl", "vim")
	f.decide(t, "openssl", "", "", "approve")
	f.decide(t, "vim", "", "", "decline")

	a2 := f.agent(t, "a2", f.siteA)
	b1 := f.agent(t, "b1", f.siteB)
	f.report(t, a2, "openssl", "vim", "curl")
	f.report(t, b1, "openssl", "vim")

	for _, id := range []string{a2, b1} {
		wantStatus(t, f, id, "openssl", models.PatchStatusApproved)
		wantStatus(t, f, id, "vim", models.PatchStatusExcluded)
	}
	wantStatus(t, f, a2, "curl", models.PatchStatusMissing)
	if r := wantApproval(t, f, "openssl", PatchApprovalApproved); r.ApprovedCount != 3 || r.MissingCount != 3 {
		t.Fatalf("openssl counts approved=%d missing=%d, want 3 and 3", r.ApprovedCount, r.MissingCount)
	}
	if r := wantApproval(t, f, "vim", PatchApprovalDeclined); r.DeclinedCount != 3 {
		t.Fatalf("vim declined on %d devices, want 3", r.DeclinedCount)
	}
	wantApproval(t, f, "curl", PatchApprovalPending)

	// A patch the device stops reporting and reports again (a package held back
	// for one scan) comes back in its decided state too.
	f.report(t, a2, "curl")
	f.report(t, a2, "openssl", "curl")
	wantStatus(t, f, a2, "openssl", models.PatchStatusApproved)
}

// A scan that still reports a patch after its install, or while one is in
// flight, put it back to 'missing' and dropped the approval with it.
func TestRescanAfterInstallKeepsTheApprovalIntegration(t *testing.T) {
	f := newPatchApprovalFixture(t)
	a1 := f.agent(t, "a1", f.siteA)
	f.report(t, a1, "openssl", "zlib")
	f.decide(t, "openssl", "", "", "approve")
	f.decide(t, "zlib", "", "", "approve")
	if _, err := f.st.ClaimApprovedPatches(f.ctx, a1, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ApplyPatchResult(f.ctx, a1, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{
		{Identifier: "zlib", Success: true},
	}}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f, a1, "openssl", models.PatchStatusInstalling)
	wantStatus(t, f, a1, "zlib", models.PatchStatusInstalled)

	f.report(t, a1, "openssl", "zlib")
	wantStatus(t, f, a1, "openssl", models.PatchStatusApproved)
	wantStatus(t, f, a1, "zlib", models.PatchStatusApproved)

	// Without a decision a re-reported patch is still just missing; a policy's
	// auto-approval is the scheduler's to repeat, not the report's.
	f.report(t, a1, "openssl", "zlib", "curl")
	if _, err := f.st.pool.Exec(f.ctx, `UPDATE agent_patches SET status='installed'
		WHERE agent_id=$1 AND patch_id=(SELECT id FROM patches WHERE identifier='curl')`, a1); err != nil {
		t.Fatal(err)
	}
	f.report(t, a1, "openssl", "zlib", "curl")
	wantStatus(t, f, a1, "curl", models.PatchStatusMissing)
}

// The most specific decision governs a device that reports later, and the
// newest decision replaces the narrower ones inside its scope, so what the
// screen shows after a click is what later scans keep.
func TestNarrowerDecisionsAndTheNewestDecisionIntegration(t *testing.T) {
	f := newPatchApprovalFixture(t)
	a1 := f.agent(t, "a1", f.siteA)
	b1 := f.agent(t, "b1", f.siteB)
	f.report(t, a1, "openssl")
	f.report(t, b1, "openssl")

	f.decide(t, "openssl", "", "", "approve")
	if n := f.decide(t, "openssl", "site", f.siteB, "decline"); n != 1 {
		t.Fatalf("site decline changed %d devices, want 1", n)
	}
	wantStatus(t, f, a1, "openssl", models.PatchStatusApproved)
	wantStatus(t, f, b1, "openssl", models.PatchStatusExcluded)
	wantApproval(t, f, "openssl", PatchApprovalPartial)
	for filter, want := range map[string]bool{PatchApprovalApproved: true, PatchApprovalDeclined: true, PatchApprovalPending: false} {
		if _, ok := f.catalog(t, filter)["openssl"]; ok != want {
			t.Errorf("approved here and declined there: listed under %q = %v, want %v", filter, ok, want)
		}
	}

	// New machines on each site follow their own site's decision.
	a2 := f.agent(t, "a2", f.siteA)
	b2 := f.agent(t, "b2", f.siteB)
	f.report(t, a2, "openssl")
	f.report(t, b2, "openssl")
	wantStatus(t, f, a2, "openssl", models.PatchStatusApproved)
	wantStatus(t, f, b2, "openssl", models.PatchStatusExcluded)

	// A client decision outranks the global one for the client's devices.
	f.decide(t, "openssl", "client", f.clientB, "approve")
	wantStatus(t, f, b1, "openssl", models.PatchStatusApproved)
	b3 := f.agent(t, "b3", f.siteB)
	f.report(t, b3, "openssl")
	wantStatus(t, f, b3, "openssl", models.PatchStatusApproved)

	// A fleet-wide decline replaces the client approval; a machine joining the
	// client afterwards is declined too, not approved by the replaced decision.
	f.decide(t, "openssl", "", "", "decline")
	b4 := f.agent(t, "b4", f.siteB)
	f.report(t, b4, "openssl")
	for _, id := range []string{a1, a2, b1, b2, b3, b4} {
		wantStatus(t, f, id, "openssl", models.PatchStatusExcluded)
	}
	wantApproval(t, f, "openssl", PatchApprovalDeclined)
	if _, ok := f.catalog(t, PatchApprovalApproved)["openssl"]; ok {
		t.Error("a patch declined everywhere is still listed as approved")
	}
	var left int
	if err := f.st.pool.QueryRow(f.ctx, `SELECT count(*) FROM patch_approvals`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("%d decisions recorded, want only the fleet-wide one", left)
	}
}

// Decisions are scoped by id stored as text; an id in another spelling must
// still match the device it names, and a scope that is not one is refused
// rather than read as the whole fleet.
func TestApprovalScopeIsValidatedAndCanonicalIntegration(t *testing.T) {
	f := newPatchApprovalFixture(t)
	a1 := f.agent(t, "a1", f.siteA)
	b1 := f.agent(t, "b1", f.siteB)
	f.report(t, a1, "openssl")
	f.report(t, b1, "openssl")

	upper := ""
	for _, c := range f.siteB {
		if c >= 'a' && c <= 'f' {
			c -= 'a' - 'A'
		}
		upper += string(c)
	}
	f.decide(t, "openssl", "site", upper, "approve")
	b2 := f.agent(t, "b2", f.siteB)
	f.report(t, b2, "openssl")
	wantStatus(t, f, b2, "openssl", models.PatchStatusApproved)
	wantStatus(t, f, a1, "openssl", models.PatchStatusMissing)

	id := f.patchID(t, "openssl")
	if _, err := f.st.SetApproval(f.ctx, id, "group", f.siteA, "approve", "manual", "operator"); err == nil {
		t.Fatal("a group-scoped approval was accepted")
	}
	if _, err := f.st.SetApproval(f.ctx, id, "site", "not-a-uuid", "approve", "manual", "operator"); err == nil {
		t.Fatal("a malformed scope id was accepted")
	}
	wantStatus(t, f, a1, "openssl", models.PatchStatusMissing)
	if _, err := f.st.PatchCatalog(f.ctx, ReportFilter{}, "partial"); err == nil {
		t.Fatal("an unknown catalog filter was accepted")
	}
}
