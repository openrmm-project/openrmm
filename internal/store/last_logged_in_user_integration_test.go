package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Runs in the PostgreSQL CI job. The last logged-in user is the last non-empty
// user a heartbeat reported: logging off keeps it (the hardware report still
// says who uses the machine), an agent that does not report a user leaves it,
// the next user replaces it, and a revoked device is not touched. Migration
// 035 fills it in for devices that had a user before it existed.
func TestLastLoggedInUserOutlivesLogoffIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st := newIsolatedStore(t, "last_logged_in_user", url)

	agent := func(host string) string {
		t.Helper()
		a, err := st.CreateAgent(ctx, host, "windows", "amd64", "0.11.0", "hash-"+uuid.NewString(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertInventory(ctx, a.ID, []byte(`{"hostname":"`+host+`","cpu_model":"Test CPU"}`)); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	report := func(user *string, id string) {
		t.Helper()
		if err := st.TouchAgentSession(ctx, id, user); err != nil {
			t.Fatal(err)
		}
	}
	last := func(id string) (string, *time.Time, string) {
		t.Helper()
		a, err := st.GetAgent(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return a.LastLoggedInUser, a.LastLoggedInUserAt, a.LoggedInUser
	}
	hardware := func(host string) (string, *time.Time) {
		t.Helper()
		rows, err := st.HardwareReport(ctx, ReportFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Hostname == host {
				return r.LastLoggedInUser, r.LastLoggedInUserAt
			}
		}
		t.Fatalf("%s is not in the hardware report", host)
		return "", nil
	}
	str := func(s string) *string { return &s }

	host := "PC-" + uuid.NewString()[:8]
	pc := agent(host)
	if u, at, _ := last(pc); u != "" || at != nil {
		t.Fatalf("fresh device: %q %v", u, at)
	}
	if u, at := hardware(host); u != "" || at != nil {
		t.Fatalf("fresh hardware row: %q %v", u, at)
	}

	report(str(`ACME\maria`), pc)
	u, at, now := last(pc)
	if u != `ACME\maria` || at == nil || now != `ACME\maria` {
		t.Fatalf("logged in: last %q %v, now %q", u, at, now)
	}
	seen := *at

	// Logging off clears who is logged in now, not who was last.
	time.Sleep(20 * time.Millisecond)
	report(str(""), pc)
	if u, at, now := last(pc); u != `ACME\maria` || at == nil || !at.Equal(seen) || now != "" {
		t.Fatalf("logged off: last %q %v (seen %v), now %q", u, at, seen, now)
	}
	if u, at := hardware(host); u != `ACME\maria` || at == nil || !at.Equal(seen) {
		t.Fatalf("hardware row after logoff: %q %v", u, at)
	}

	// An agent older than 0.11 reports no user: nothing changes.
	report(nil, pc)
	if u, _, _ := last(pc); u != `ACME\maria` {
		t.Fatalf("old agent heartbeat: last %q", u)
	}

	// The next user replaces it and moves the time.
	time.Sleep(20 * time.Millisecond)
	report(str(`AzureAD\JoaoSilva`), pc)
	if u, at, _ := last(pc); u != `AzureAD\JoaoSilva` || at == nil || !at.After(seen) {
		t.Fatalf("next user: last %q %v (was %v)", u, at, seen)
	}

	// A revoked device's record is not touched.
	gone := agent("GONE-" + uuid.NewString()[:8])
	if err := st.RevokeAgent(ctx, gone); err != nil {
		t.Fatal(err)
	}
	report(str(`ACME\ghost`), gone)
	if u, at, _ := last(gone); u != "" || at != nil {
		t.Fatalf("revoked device: last %q %v", u, at)
	}

	// Migration 035 fills the last user in from the user logged in before it
	// existed (when they logged in, else when the device was last seen), and
	// leaves devices without one, or with a last user already, alone.
	before := agent("OLD-" + uuid.NewString()[:8])
	empty := agent("EMPTY-" + uuid.NewString()[:8])
	since := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if _, err := st.pool.Exec(ctx, `UPDATE agents SET logged_in_user=$2, logged_in_user_since=$3, last_logged_in_user='', last_logged_in_user_at=NULL WHERE id=$1`,
		before, `ACME\legacy`, since); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, migrationV35SQL); err != nil {
		t.Fatalf("migration 035 again: %v", err)
	}
	if u, at, _ := last(before); u != `ACME\legacy` || at == nil || !at.Equal(since) {
		t.Fatalf("backfilled: %q %v, want ACME\\legacy at %v", u, at, since)
	}
	if u, at, _ := last(empty); u != "" || at != nil {
		t.Fatalf("no user to backfill: %q %v", u, at)
	}
	if u, _, _ := last(pc); u != `AzureAD\JoaoSilva` {
		t.Fatalf("the backfill overwrote a last user: %q", u)
	}
}
