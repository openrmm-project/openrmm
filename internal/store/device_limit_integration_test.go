package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/devicelimit"
)

// limitStore is a migrated schema of its own: these tests count every agent,
// so they cannot share one with other packages running in parallel.
func limitStore(t *testing.T, prefix string) *Store {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	return newIsolatedStore(t, prefix, url)
}

// A plan's device limit is enforced where the agent row is created: counted
// and inserted in one transaction, under a lock, and a refused enrolment does
// not spend one of the token's uses.
func TestEnrollAgentRespectsTheDeviceLimitIntegration(t *testing.T) {
	st := limitStore(t, "store_devlimit")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tok, err := st.CreateEnrollToken(ctx, "devlimit-token", "limit", 10, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"pc-1", "pc-2"} {
		if _, err := st.EnrollAgent(ctx, tok.Token, 2, name, "windows", "amd64", "0.8.0", "hash-"+name); err != nil {
			t.Fatalf("enrolment %d within the limit: %v", i+1, err)
		}
	}
	if _, err := st.EnrollAgent(ctx, tok.Token, 2, "pc-3", "windows", "amd64", "0.8.0", "hash-pc-3"); !errors.Is(err, devicelimit.ErrLimitReached) {
		t.Fatalf("the third enrolment must be refused, got %v", err)
	}
	// Without a valid token the console says nothing about its limit.
	if _, err := st.EnrollAgent(ctx, "not-a-token", 2, "pc-x", "windows", "amd64", "0.8.0", "hash-pc-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an invalid token on a full console must read as an invalid token, got %v", err)
	}
	var uses int
	if err := st.pool.QueryRow(ctx, `SELECT uses_left FROM enroll_tokens WHERE token=$1`, tok.Token).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if uses != 8 {
		t.Fatalf("a refused enrolment spent a token use: %d left, want 8", uses)
	}
	if n, _ := st.CountAgents(ctx); n != 2 {
		t.Fatalf("count %d, want 2", n)
	}

	// Deleting a device frees its place.
	agents, err := st.ListAgents(ctx)
	if err != nil || len(agents) != 2 {
		t.Fatalf("list: %v %d", err, len(agents))
	}
	if err := st.DeleteAgent(ctx, agents[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnrollAgent(ctx, tok.Token, 2, "pc-3", "windows", "amd64", "0.8.0", "hash-pc-3"); err != nil {
		t.Fatalf("a freed place can be taken: %v", err)
	}

	// No limit: anything goes, and an exhausted or unknown token is still refused.
	if _, err := st.EnrollAgent(ctx, tok.Token, 0, "pc-4", "linux", "amd64", "0.8.0", "hash-pc-4"); err != nil {
		t.Fatalf("no limit: %v", err)
	}
	if _, err := st.EnrollAgent(ctx, "no-such-token", 0, "pc-5", "linux", "amd64", "0.8.0", "hash-pc-5"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
}

// Two installs racing for the last place: exactly one gets it.
func TestEnrollAgentLastPlaceIsNotDoubleBookedIntegration(t *testing.T) {
	st := limitStore(t, "store_devlimitrace")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tok, err := st.CreateEnrollToken(ctx, "devlimit-race", "race", 50, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "race-" + string(rune('a'+i))
			_, err := st.EnrollAgent(ctx, tok.Token, limit, name, "linux", "amd64", "0.8.0", "hash-"+name)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, devicelimit.ErrLimitReached):
				refused++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok != limit || refused != 12-limit {
		t.Fatalf("%d enrolled and %d refused, want %d and %d", ok, refused, limit, 12-limit)
	}
	if n, _ := st.CountAgents(ctx); n != limit {
		t.Fatalf("count %d, want %d", n, limit)
	}
}

// The portal's answer survives a restart in app_settings.
func TestDeviceLimitStateRoundTripIntegration(t *testing.T) {
	st := limitStore(t, "store_devlimitstate")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	empty, err := st.DeviceLimitState(ctx)
	if err != nil || empty.Limit != 0 {
		t.Fatalf("no answer yet means no limit: %+v %v", empty, err)
	}
	want := devicelimit.State{Limit: 100, EnforceFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		UpgradeURL: "https://openrmm.io/portal", FetchedAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	if err := st.SaveDeviceLimitState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeviceLimitState(ctx)
	if err != nil || got.Limit != want.Limit || !got.EnforceFrom.Equal(want.EnforceFrom) || got.UpgradeURL != want.UpgradeURL {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}
