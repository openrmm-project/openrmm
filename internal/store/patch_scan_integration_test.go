package store_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

type scanFixture struct {
	st    *store.Store
	db    *pgxpool.Pool
	ctx   context.Context
	agent string
}

func newScanFixture(t *testing.T) scanFixture {
	t.Helper()
	dsn := os.Getenv("RMM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	prefix := "scan_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	st := storetest.NewSchema(t, prefix, dsn)
	ctx := context.Background()
	// Resolve only the unique schema created above to age server timestamps in tests.
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var schema string
	if err = admin.QueryRow(ctx, `SELECT nspname FROM pg_namespace WHERE nspname LIKE $1`, prefix+"_%").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	a, err := st.CreateAgent(ctx, "scan-test", "linux", "amd64", "test", "token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return scanFixture{st, db, ctx, a.ID}
}
func (f scanFixture) reserve(t *testing.T, refresh bool) string {
	t.Helper()
	id := uuid.NewString()
	if err := f.st.ReservePatchScan(f.ctx, f.agent, id, refresh); err != nil {
		t.Fatal(err)
	}
	return id
}
func (f scanFixture) report(t *testing.T, id, mode, code string, items ...protocol.PatchItem) {
	t.Helper()
	if err := f.st.UpsertPatchReport(f.ctx, f.agent, &protocol.PatchReportMsg{OS: "linux", RequestID: id, ScanMode: mode, ErrorCode: code, Patches: items}); err != nil {
		t.Fatal(err)
	}
}
func (f scanFixture) state(t *testing.T) *models.PatchScanState {
	t.Helper()
	s, err := f.st.GetPatchScanState(f.ctx, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f scanFixture) inventory(t *testing.T) []models.AgentPatch {
	t.Helper()
	p, err := f.st.ListAgentPatches(f.ctx, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f scanFixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}
func (f scanFixture) compliance(t *testing.T) models.ComplianceRow {
	t.Helper()
	rows, err := f.st.ComplianceReport(f.ctx, store.ReportFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("compliance: %+v %v", rows, err)
	}
	return rows[0]
}

func TestPatchScanFirstFailureAndRecovery(t *testing.T) {
	f := newScanFixture(t)
	id := f.reserve(t, true)
	f.report(t, id, protocol.PatchScanCached, "https://user:secret@repo.invalid")
	state := f.state(t)
	if state.Status != "failed" || state.ErrorCode != "scan_failed" || state.LastSuccess != nil || state.LastAttempt == nil || state.LastMetadataRefresh != nil {
		t.Fatalf("%+v", state)
	}
	if f.compliance(t).Status != "unknown" {
		t.Fatal("failed clean scan became compliant")
	}
	due, err := f.st.PatchScanDue(f.ctx, 24*time.Hour)
	if err != nil || len(due) != 0 {
		t.Fatalf("immediate retry: %v %v", due, err)
	}
	id = f.reserve(t, true)
	f.report(t, id, protocol.PatchScanRefreshed, "")
	state = f.state(t)
	if state.Freshness != "verified" || state.ConsecutiveFailures != 0 || state.ErrorCode != "" || !state.LastSuccess.Equal(*state.LastMetadataRefresh) {
		t.Fatalf("recovery: %+v", state)
	}
	if f.compliance(t).Status != "compliant" {
		t.Fatal("verified clean report not compliant")
	}
}

func TestPatchFailurePreservesEveryRowAndApproval(t *testing.T) {
	f := newScanFixture(t)
	statuses := []string{"missing", "approved", "excluded", "installing", "failed"}
	items := []protocol.PatchItem{}
	for _, s := range statuses {
		items = append(items, protocol.PatchItem{Identifier: s, Title: s, Classification: protocol.PatchSecurity})
	}
	f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", items...)
	for _, s := range statuses {
		f.sql(t, `UPDATE agent_patches SET status=$2,error='prior diagnostic' WHERE agent_id=$1 AND patch_id=(SELECT id FROM patches WHERE identifier=$2)`, f.agent, s)
	}
	before := f.inventory(t)
	last := f.state(t)
	for _, p := range before {
		if p.Identifier == "approved" {
			if _, err := f.st.SetApproval(f.ctx, p.PatchID, "agent", f.agent, "approve", "manual", "operator"); err != nil {
				t.Fatal(err)
			}
		}
	}
	before = f.inventory(t)
	var approvalsBefore string
	if err := f.db.QueryRow(f.ctx, `SELECT coalesce(jsonb_agg(to_jsonb(p))::text,'[]') FROM patch_approvals p`).Scan(&approvalsBefore); err != nil {
		t.Fatal(err)
	}
	f.report(t, f.reserve(t, true), protocol.PatchScanCached, "refresh_failed")
	if after := f.inventory(t); !reflect.DeepEqual(after, before) {
		t.Fatalf("failure changed inventory: before=%+v after=%+v", before, after)
	}
	var approvalsAfter string
	if err := f.db.QueryRow(f.ctx, `SELECT coalesce(jsonb_agg(to_jsonb(p))::text,'[]') FROM patch_approvals p`).Scan(&approvalsAfter); err != nil {
		t.Fatal(err)
	}
	if approvalsBefore != approvalsAfter {
		t.Fatal("failure changed approvals")
	}
	state := f.state(t)
	if !state.LastSuccess.Equal(*last.LastSuccess) || !state.LastMetadataRefresh.Equal(*last.LastMetadataRefresh) {
		t.Fatal("failure rewrote success timestamps")
	}
	if f.compliance(t).Status != "critical" {
		t.Fatal("freshness warning hid security finding")
	}
}

func TestPatchScanIgnoresStaleDuplicateAndLegacyReports(t *testing.T) {
	f := newScanFixture(t)
	old := f.reserve(t, true)
	f.report(t, old, protocol.PatchScanRefreshed, "", protocol.PatchItem{Identifier: "security", Classification: protocol.PatchSecurity})
	before := f.inventory(t)
	state := f.state(t)
	f.report(t, old, protocol.PatchScanRefreshed, "")
	if !reflect.DeepEqual(before, f.inventory(t)) || !reflect.DeepEqual(state, f.state(t)) {
		t.Fatal("duplicate report changed state")
	}
	current := f.reserve(t, true)
	pending := f.state(t)
	f.report(t, old, protocol.PatchScanRefreshed, "timeout")
	f.report(t, "", protocol.PatchScanRefreshed, "", protocol.PatchItem{Identifier: "legacy"})
	if !reflect.DeepEqual(before, f.inventory(t)) || !reflect.DeepEqual(pending, f.state(t)) {
		t.Fatal("old or legacy report consumed current request")
	}
	f.report(t, current, protocol.PatchScanRefreshed, "")
	if len(f.inventory(t)) != 0 || f.state(t).Status != "succeeded" {
		t.Fatal("matching authoritative report not applied")
	}
}

func TestCachedAfterFreshCannotInheritFreshnessOrEraseSecurity(t *testing.T) {
	f := newScanFixture(t)
	f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", protocol.PatchItem{Identifier: "openssl", Classification: protocol.PatchSecurity})
	refreshed := f.state(t).LastMetadataRefresh
	f.report(t, f.reserve(t, false), protocol.PatchScanCached, "")
	if f.state(t).Freshness != "unverified" || !f.state(t).LastMetadataRefresh.Equal(*refreshed) || len(f.inventory(t)) != 1 || f.compliance(t).Status != "critical" {
		t.Fatal("cached report lost provenance or security")
	}
	f.report(t, f.reserve(t, false), protocol.PatchScanCached, "", protocol.PatchItem{Identifier: "openssl", Classification: protocol.PatchOther})
	if f.inventory(t)[0].Classification != protocol.PatchSecurity {
		t.Fatal("cached catalog downgraded known security")
	}
	f.report(t, "", protocol.PatchScanRefreshed, "")
	if f.state(t).ScanMode != protocol.PatchScanLegacy || len(f.inventory(t)) != 1 {
		t.Fatal("legacy freshness spoof or lost finding")
	}
	f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "")
	if len(f.inventory(t)) != 0 || f.compliance(t).Status != "compliant" {
		t.Fatal("fresh scan did not reconcile")
	}
	f.report(t, f.reserve(t, false), protocol.PatchScanCached, "")
	if f.compliance(t).Status != "unknown" {
		t.Fatal("cached clean inventory reused old compliance")
	}
}

func TestPatchScanReservationTimeoutAndBackoff(t *testing.T) {
	f := newScanFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.st.ReservePatchScan(f.ctx, f.agent, uuid.NewString(), true) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, store.ErrPatchScanBusy) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("reserved %d requests", successes)
	}
	old := f.state(t).RequestID
	f.sql(t, `UPDATE agent_patch_scan_state SET deadline=now()-interval '1 second',last_attempt=now()-interval '7 minutes' WHERE agent_id=$1`, f.agent)
	state := f.state(t)
	if state.Status != "timed_out" || state.ErrorCode != "timeout" || state.ConsecutiveFailures != 1 {
		t.Fatalf("timeout: %+v", state)
	}
	due, err := f.st.PatchScanDue(f.ctx, 24*time.Hour)
	if err != nil || len(due) != 0 {
		t.Fatalf("backoff: %v %v", due, err)
	}
	f.report(t, old, protocol.PatchScanRefreshed, "")
	if f.state(t).Status != "timed_out" {
		t.Fatal("expired report accepted")
	}
	f.sql(t, `UPDATE agent_patch_scan_state SET last_attempt=now()-interval '16 minutes' WHERE agent_id=$1`, f.agent)
	due, err = f.st.PatchScanDue(f.ctx, 24*time.Hour)
	if err != nil || len(due) != 1 {
		t.Fatalf("retry due: %v %v", due, err)
	}
	current := f.reserve(t, true)
	if err = f.st.FailPatchScan(f.ctx, f.agent, old, "offline"); err != nil {
		t.Fatal(err)
	}
	if f.state(t).RequestID != current || f.state(t).Status != "pending" {
		t.Fatal("old failure released newer reservation")
	}
	f.report(t, current, protocol.PatchScanCached, "")
	if f.state(t).ErrorCode != "refresh_unverified" {
		t.Fatal("unverified report claimed fresh request")
	}
}

func TestPatchScanStaleAndNativeCompliance(t *testing.T) {
	f := newScanFixture(t)
	f.report(t, f.reserve(t, false), protocol.PatchScanNative, "")
	if f.compliance(t).Status != "compliant" || f.state(t).LastMetadataRefresh != nil {
		t.Fatal("native scan misrepresented catalog refresh")
	}
	f.sql(t, `UPDATE agent_patch_scan_state SET last_success=now()-interval '25 hours' WHERE agent_id=$1`, f.agent)
	if f.compliance(t).Status != "unknown" || f.state(t).Freshness != "stale" {
		t.Fatal("stale compliance")
	}
}

func TestUnverifiedReportsPreserveVersionsMetadataAndInstallResults(t *testing.T) {
	f := newScanFixture(t)
	current := protocol.PatchItem{Identifier: "openssl", Title: "Security update v2", Version: "2.0", Classification: protocol.PatchSecurity, KB: "KB-new", CVEs: []string{"CVE-2026-12345"}, RebootRequired: true}
	f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", current)
	other, err := f.st.CreateAgent(f.ctx, "second-device", "linux", "amd64", "test", "other-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A cached report from another device may add findings without changing the
	// fleet-wide trusted catalog entry already displayed for this device.
	stale := protocol.PatchItem{Identifier: "openssl", Title: "Old v1", Version: "1.0", Classification: protocol.PatchOther}
	if err = f.st.UpsertPatchReport(f.ctx, other.ID, &protocol.PatchReportMsg{OS: "linux", Patches: []protocol.PatchItem{stale}}); err != nil {
		t.Fatal(err)
	}
	assertMetadata := func() {
		t.Helper()
		rows := f.inventory(t)
		if len(rows) != 1 {
			t.Fatal(rows)
		}
		p := rows[0]
		if p.AvailableVer != "2.0" || p.Title != current.Title || p.KB != current.KB || !p.RebootRequired || !reflect.DeepEqual(p.CVEs, current.CVEs) || p.Classification != protocol.PatchSecurity {
			t.Fatalf("known metadata overwritten: %+v", p)
		}
	}
	assertMetadata()
	f.report(t, f.reserve(t, false), protocol.PatchScanCached, "", stale)
	assertMetadata()
	f.report(t, "", protocol.PatchScanLegacy, "", stale)
	assertMetadata()
	p := f.inventory(t)[0]
	if _, err = f.st.SetApproval(f.ctx, p.PatchID, "agent", f.agent, "approve", "manual", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.ClaimApprovedPatches(f.ctx, f.agent, nil); err != nil {
		t.Fatal(err)
	}
	f.report(t, "", protocol.PatchScanLegacy, "", stale)
	if f.inventory(t)[0].Status != models.PatchStatusInstalling {
		t.Fatal("legacy scan reopened an in-flight claim")
	}
	if err = f.st.ApplyPatchResult(f.ctx, f.agent, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{{Identifier: "openssl", Success: true}}}); err != nil {
		t.Fatal(err)
	}
	f.report(t, "", protocol.PatchScanLegacy, "", stale)
	if f.inventory(t)[0].Status != models.PatchStatusInstalled {
		t.Fatal("legacy scan reopened confirmed installation")
	}
	// A successful authoritative report is still allowed to report a remaining
	// patch after installation and retains the original approval semantics.
	f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", current)
	if f.inventory(t)[0].Status != models.PatchStatusApproved {
		t.Fatal("authoritative scan lost existing approval semantics")
	}
}

func TestScanStartedBeforeInstallCannotReopenOrEraseClaim(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		name := "reported"
		if omitted {
			name = "omitted"
		}
		t.Run(name, func(t *testing.T) {
			f := newScanFixture(t)
			item := protocol.PatchItem{Identifier: "openssl", Title: "OpenSSL", Classification: protocol.PatchSecurity}
			f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", item)
			p := f.inventory(t)[0]
			if _, err := f.st.SetApproval(f.ctx, p.PatchID, "agent", f.agent, "approve", "manual", "operator"); err != nil {
				t.Fatal(err)
			}
			pending := f.reserve(t, true)
			if _, err := f.st.ClaimApprovedPatches(f.ctx, f.agent, nil); err != nil {
				t.Fatal(err)
			}
			if omitted {
				f.report(t, pending, protocol.PatchScanRefreshed, "")
			} else {
				f.report(t, pending, protocol.PatchScanRefreshed, "", item)
			}
			rows := f.inventory(t)
			if len(rows) != 1 || rows[0].Status != models.PatchStatusInstalling {
				t.Fatalf("scan reopened/erased later claim: %+v", rows)
			}
			// A report from before the install result also cannot reopen a completed install.
			pending = f.reserve(t, true)
			if err := f.st.ApplyPatchResult(f.ctx, f.agent, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{{Identifier: item.Identifier, Success: true}}}); err != nil {
				t.Fatal(err)
			}
			if omitted {
				f.report(t, pending, protocol.PatchScanRefreshed, "")
			} else {
				f.report(t, pending, protocol.PatchScanRefreshed, "", item)
			}
			rows = f.inventory(t)
			if len(rows) != 1 || rows[0].Status != models.PatchStatusInstalled {
				t.Fatalf("scan reopened/erased later confirmed result: %+v", rows)
			}
			f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", item)
			if f.inventory(t)[0].Status != models.PatchStatusApproved {
				t.Fatal("later authoritative report did not preserve approval")
			}
		})
	}
}

func TestScanCannotEraseMoreRecentInstallFailure(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		name := "reported"
		if omitted {
			name = "omitted"
		}
		t.Run(name, func(t *testing.T) {
			f := newScanFixture(t)
			item := protocol.PatchItem{Identifier: "openssl", Classification: protocol.PatchSecurity}
			f.report(t, f.reserve(t, true), protocol.PatchScanRefreshed, "", item)
			p := f.inventory(t)[0]
			if _, err := f.st.SetApproval(f.ctx, p.PatchID, "agent", f.agent, "approve", "manual", "operator"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.ClaimApprovedPatches(f.ctx, f.agent, nil); err != nil {
				t.Fatal(err)
			}
			pending := f.reserve(t, true)
			if err := f.st.ApplyPatchResult(f.ctx, f.agent, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{{Identifier: item.Identifier, Success: false, Error: "installation failed diagnostic"}}}); err != nil {
				t.Fatal(err)
			}
			if omitted {
				f.report(t, pending, protocol.PatchScanRefreshed, "")
			} else {
				f.report(t, pending, protocol.PatchScanRefreshed, "", item)
			}
			rows := f.inventory(t)
			if len(rows) != 1 || rows[0].Status != models.PatchStatusFailed || rows[0].Error != "installation failed diagnostic" || f.compliance(t).Status != "critical" {
				t.Fatalf("later failure overwritten: %+v", rows)
			}
		})
	}
}
