package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// The audit log's job is to answer a question weeks later: who approved that
// script, who changed that scope, who moved mail delivery. Before this, the API
// could only return the most recent 200 entries — which on an active fleet is
// under a day, so the question could not be asked at all.

func auditStore(t *testing.T) (context.Context, *Store) {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx, newIsolatedStore(t, "store_audit", url)
}

func TestAuditFilterNarrowsTheTrailIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	target := uuid.NewString()

	for _, e := range []struct{ actor, action, target string }{
		{"alice", "job.approve", target},
		{"alice", "job.reject", uuid.NewString()},
		{"bob", "job.approve", uuid.NewString()},
		{"bob", "user.scope.update", uuid.NewString()},
		{"carol", "config.smtp.alerts.update", ""},
	} {
		if err := st.AddAudit(ctx, e.actor, e.action, e.target, nil); err != nil {
			t.Fatal(err)
		}
	}

	count := func(f AuditFilter) (int, int) {
		t.Helper()
		rows, total, err := st.ListAudit(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows), total
	}

	if got, _ := count(AuditFilter{}); got != 5 {
		t.Fatalf("unfiltered = %d, want all 5", got)
	}
	if got, _ := count(AuditFilter{Actor: "alice"}); got != 2 {
		t.Errorf("actor=alice = %d, want 2", got)
	}
	// Case-insensitive: an operator typing a name should not have to match the
	// capitalisation the system happened to record.
	if got, _ := count(AuditFilter{Actor: "ALICE"}); got != 2 {
		t.Errorf("actor=ALICE = %d, want 2 — the match should ignore case", got)
	}
	// Prefix on the family, which is the granularity anyone actually wants:
	// "job." finds approve and reject together.
	if got, _ := count(AuditFilter{Action: "job."}); got != 3 {
		t.Errorf("action=job. = %d, want 3", got)
	}
	if got, _ := count(AuditFilter{Action: "job.approve"}); got != 2 {
		t.Errorf("action=job.approve = %d, want 2", got)
	}
	if got, _ := count(AuditFilter{Target: target}); got != 1 {
		t.Errorf("target = %d, want 1", got)
	}
	if got, _ := count(AuditFilter{Actor: "alice", Action: "job.approve"}); got != 1 {
		t.Errorf("actor AND action = %d, want 1 — filters must combine", got)
	}
}

// A filter value containing % or _ must match literally. Otherwise a search for
// a target id that happens to contain an underscore quietly matches more than
// it should — and an audit search that over-matches is worse than one that
// finds nothing, because the extra rows look like evidence.
func TestAuditFilterTreatsWildcardsLiterallyIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	for _, action := range []string{"job.approve", "jobXapprove", "job_approve"} {
		if err := st.AddAudit(ctx, "alice", action, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := st.ListAudit(ctx, AuditFilter{Action: "job_"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Action != "job_approve" {
		t.Fatalf("action=job_ matched %d rows (%v); '_' must be literal, not a wildcard",
			len(rows), actionsOf(rows))
	}
}

// Paging has to report the total, or the console cannot tell "200 results" from
// "the first 200 of nine thousand" — and an investigator who stops at the first
// page because it looked complete has drawn the wrong conclusion.
func TestAuditPagingReportsTheTotalIntegration(t *testing.T) {
	ctx, st := auditStore(t)

	// Written with an IDENTICAL created_at, on purpose. Separate AddAudit calls
	// get distinct microsecond timestamps, which hides the bug this is here to
	// catch: with `ORDER BY created_at DESC` alone, tied rows have no defined
	// order and Postgres is free to return the same row on two pages — or skip
	// one entirely. An audit page that silently drops an entry is exactly the
	// failure nobody would notice.
	stamp := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 25; i++ {
		if _, err := st.pool.Exec(ctx,
			`INSERT INTO audit_log (id, actor, action, target, detail, created_at)
			 VALUES ($1, 'alice', 'job.approve', $2, '{}', $3)`,
			uuid.NewString(), uuid.NewString(), stamp); err != nil {
			t.Fatal(err)
		}
	}

	first, total, err := st.ListAudit(ctx, AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 10 || total != 25 {
		t.Fatalf("page 1: %d rows, total %d; want 10 and 25", len(first), total)
	}
	second, _, err := st.ListAudit(ctx, AuditFilter{Limit: 10, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 10 {
		t.Fatalf("page 2: %d rows, want 10", len(second))
	}
	// Pages must not overlap.
	//
	// Honest caveat: this does not prove the `id DESC` tiebreak is load-bearing.
	// LIMIT/OFFSET over tied values is undefined in SQL, but Postgres happens to
	// return a stable order for a table this small, so removing the tiebreak
	// still passes here. The tiebreak stays because the guarantee is not ours to
	// assume — it just is not what this assertion catches.
	seen := map[string]bool{}
	for _, e := range append(first, second...) {
		if seen[e.ID] {
			t.Fatalf("entry %s appeared on both pages: the ordering is not stable", e.ID)
		}
		seen[e.ID] = true
	}

	last, _, err := st.ListAudit(ctx, AuditFilter{Limit: 10, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 5 {
		t.Errorf("final page: %d rows, want 5", len(last))
	}
}

// The time window is the one an investigation actually starts from.
func TestAuditTimeWindowIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	if err := st.AddAudit(ctx, "alice", "job.approve", "", nil); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	if _, total, _ := st.ListAudit(ctx, AuditFilter{Since: past}); total != 1 {
		t.Errorf("since an hour ago should find the entry, got %d", total)
	}
	if _, total, _ := st.ListAudit(ctx, AuditFilter{Since: future}); total != 0 {
		t.Errorf("since the future should find nothing, got %d", total)
	}
	if _, total, _ := st.ListAudit(ctx, AuditFilter{Until: past}); total != 0 {
		t.Errorf("until an hour ago should find nothing, got %d", total)
	}
}

// The facets exist so the console can build its filter from what happened,
// rather than offering a text box the operator has to guess into.
func TestAuditFacetsIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	for _, e := range [][2]string{
		{"alice", "job.approve"}, {"alice", "job.reject"},
		{"bob", "user.scope.update"}, {"bob", "user.disable"},
	} {
		if err := st.AddAudit(ctx, e[0], e[1], "", nil); err != nil {
			t.Fatal(err)
		}
	}
	actors, actions, err := st.AuditFacets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 2 {
		t.Errorf("actors = %v, want alice and bob", actors)
	}
	// Families, not full actions: two job.* events offer "job" once.
	if len(actions) != 2 {
		t.Errorf("action families = %v, want job and user", actions)
	}
}

// A failed write must be counted. It is the only signal that the trail has a
// hole, because the trail cannot record its own absence.
func TestAuditWriteFailureIsCountedIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	before := AuditWriteFailures()

	// Invalid JSON in a JSONB column: the insert is rejected by Postgres.
	if err := st.AddAudit(ctx, "alice", "job.approve", "", []byte("not json")); err == nil {
		t.Fatal("a malformed detail should have been rejected")
	}
	if AuditWriteFailures() != before+1 {
		t.Errorf("failures = %d, want %d — a lost entry must be counted",
			AuditWriteFailures(), before+1)
	}
}

func actionsOf(entries []models.AuditEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}

// Retention here is about what an entry IS, not how old it is. The human record
// is why the log exists and is small — measured at 550 bytes a row — so it is
// kept indefinitely. The machine-written entries are telemetry sharing the
// table, and they scale with the fleet: one row per scheduled task, per device,
// per run.
func TestAuditRetentionKeepsTheHumanRecordIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	old := time.Now().AddDate(0, 0, -200)

	write := func(actor, action string, at time.Time) {
		t.Helper()
		if _, err := st.pool.Exec(ctx,
			`INSERT INTO audit_log (id, actor, action, target, detail, created_at)
			 VALUES ($1, $2, $3, '', '{}', $4)`,
			uuid.NewString(), actor, action, at); err != nil {
			t.Fatal(err)
		}
	}

	// Machine-written, and old.
	write("scheduler", "job.automated", old)
	write("agent", "job.result", old)
	// Human, equally old. These must survive: the whole point is that age does
	// not decide, the kind of entry does.
	write("alice", "job.approve", old)
	write("alice", "user.scope.update", old)
	write("bob", "config.smtp.alerts.update", old)
	// Machine-written but recent: inside the window, so it stays.
	write("scheduler", "job.automated", time.Now())

	deleted, err := st.PurgeAuditLog(ctx, time.Now(), 90)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want exactly the 2 old machine entries", deleted)
	}

	survivors := map[string]int{}
	rows, _, err := st.ListAudit(ctx, AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rows {
		survivors[e.Action]++
	}
	for _, action := range []string{"job.approve", "user.scope.update", "config.smtp.alerts.update"} {
		if survivors[action] != 1 {
			t.Errorf("%s was purged; a 200-day-old human action must be kept", action)
		}
	}
	if survivors["job.automated"] != 1 {
		t.Errorf("job.automated survivors = %d, want the recent one only", survivors["job.automated"])
	}
	if survivors["job.result"] != 0 {
		t.Errorf("the old job.result should have gone")
	}
}

// A gap in the trail must be explained by the trail. Without this entry, purged
// rows are indistinguishable from edited ones.
func TestAuditPurgeRecordsItselfIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO audit_log (id, actor, action, target, detail, created_at)
		 VALUES ($1, 'scheduler', 'job.automated', '', '{}', $2)`,
		uuid.NewString(), time.Now().AddDate(0, 0, -200)); err != nil {
		t.Fatal(err)
	}

	if _, err := st.PurgeAuditLog(ctx, time.Now(), 90); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListAudit(ctx, AuditFilter{Action: "audit.purge"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the purge left no record of itself (%d entries)", len(rows))
	}
	// Parsed rather than substring-matched: Postgres normalises JSONB, so
	// `{"deleted":1}` comes back as `{"deleted": 1}` and a literal comparison
	// fails for a reason that has nothing to do with the behaviour.
	var recorded struct {
		Deleted int    `json:"deleted"`
		Older   string `json:"older_than"`
	}
	if err := json.Unmarshal(rows[0].Detail, &recorded); err != nil {
		t.Fatalf("the purge record is not readable JSON: %v (%s)", err, rows[0].Detail)
	}
	if recorded.Deleted != 1 {
		t.Errorf("recorded deleted = %d, want 1", recorded.Deleted)
	}
	if recorded.Older == "" {
		t.Error("the record should say what cutoff was applied")
	}
}

// Zero disables the purge. Unlike the monitoring sweep — which replaces zero
// with a default — an audit log that deletes nothing is a legitimate choice and
// has to be reachable.
func TestAuditRetentionZeroKeepsEverythingIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO audit_log (id, actor, action, target, detail, created_at)
		 VALUES ($1, 'scheduler', 'job.automated', '', '{}', $2)`,
		uuid.NewString(), time.Now().AddDate(0, 0, -3650)); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.PurgeAuditLog(ctx, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d with retention disabled; a ten-year-old entry should survive", deleted)
	}
}

// A window below the floor is far more likely to be a mistake — or someone
// shortening the trail deliberately — than a real requirement.
func TestAuditRetentionEnforcesAFloorIntegration(t *testing.T) {
	ctx, st := auditStore(t)
	// 40 days old: inside the 30-day floor's window, outside a literal 1 day.
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO audit_log (id, actor, action, target, detail, created_at)
		 VALUES ($1, 'scheduler', 'job.automated', '', '{}', $2)`,
		uuid.NewString(), time.Now().AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.PurgeAuditLog(ctx, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d; a 40-day-old entry is outside the 30-day floor", deleted)
	}

	// And an entry inside the floor survives even when 1 day was asked for.
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO audit_log (id, actor, action, target, detail, created_at)
		 VALUES ($1, 'scheduler', 'job.automated', '', '{}', $2)`,
		uuid.NewString(), time.Now().AddDate(0, 0, -10)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := st.PurgeAuditLog(ctx, time.Now(), 1); err != nil || deleted != 0 {
		t.Errorf("deleted = %d (err %v); the floor should have protected a 10-day-old entry", deleted, err)
	}
}
