package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

func TestRemoteSharesIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "store_rshare", url)

	agent, err := st.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tech, err := st.CreateUser(ctx, "tech", "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	newShare := func(token string, expires time.Time) *models.RemoteShare {
		t.Helper()
		sh, err := st.CreateRemoteShare(ctx, agent.ID, tech.ID, models.RemoteShareView, token, "code-"+token, expires)
		if err != nil {
			t.Fatal(err)
		}
		return sh
	}

	t.Run("two guests racing with the right code: one gets in", func(t *testing.T) {
		newShare("race", time.Now().Add(time.Hour))
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := st.RedeemRemoteShare(ctx, "race", "code-race", "secret-"+uuid.NewString(), "guest", "ip", "ua", 5)
				if err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("%d guests got in on one link", wins)
		}
	})

	t.Run("wrong codes count, and the last allowed one revokes", func(t *testing.T) {
		sh := newShare("guess", time.Now().Add(time.Hour))
		for i := 0; i < 3; i++ {
			if _, err := st.RedeemRemoteShare(ctx, "guess", "nope", "s", "g", "ip", "ua", 3); !errors.Is(err, ErrRemoteShareCode) {
				t.Fatalf("wrong code %d: %v", i+1, err)
			}
		}
		got, err := st.GetRemoteShare(ctx, sh.ID)
		if err != nil || got.Status != models.RemoteShareRevoked || got.FailedAttempts != 3 {
			t.Fatalf("after three wrong codes: %+v %v", got, err)
		}
		if _, err := st.RedeemRemoteShare(ctx, "guess", "code-guess", "s2", "g", "ip", "ua", 3); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the right code opened a revoked link: %v", err)
		}
	})

	t.Run("an expired link opens nothing", func(t *testing.T) {
		newShare("stale", time.Now().Add(-time.Minute))
		if _, err := st.OpenRemoteShareByToken(ctx, "stale"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("lookup of an expired link: %v", err)
		}
		if _, err := st.RedeemRemoteShare(ctx, "stale", "code-stale", "s3", "g", "ip", "ua", 5); !errors.Is(err, ErrNotFound) {
			t.Fatalf("redeem of an expired link: %v", err)
		}
	})

	t.Run("only the creator decides, once", func(t *testing.T) {
		sh := newShare("decide", time.Now().Add(time.Hour))
		if _, err := st.RedeemRemoteShare(ctx, "decide", "code-decide", "s4", "g", "ip", "ua", 5); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DecideRemoteShare(ctx, sh.ID, uuid.NewString(), true); !errors.Is(err, ErrNotFound) {
			t.Fatalf("someone else decided: %v", err)
		}
		if got, err := st.DecideRemoteShare(ctx, sh.ID, tech.ID, true); err != nil || got.Status != models.RemoteShareAccepted {
			t.Fatalf("accept: %+v %v", got, err)
		}
		if _, err := st.DecideRemoteShare(ctx, sh.ID, tech.ID, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a decided guest was decided again: %v", err)
		}
		if got, err := st.CloseRemoteShare(ctx, sh.ID); err != nil || got.Status != models.RemoteShareEnded {
			t.Fatalf("removing an accepted guest: %+v %v", got, err)
		}
	})

	t.Run("closing all withdraws only the live ones", func(t *testing.T) {
		live := newShare("live", time.Now().Add(time.Hour))
		closed, err := st.CloseAllRemoteShares(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, sh := range closed {
			if sh.ID == live.ID {
				found = sh.Status == models.RemoteShareRevoked
			}
			if sh.Status != models.RemoteShareRevoked && sh.Status != models.RemoteShareEnded {
				t.Errorf("closed share left as %s", sh.Status)
			}
		}
		if !found {
			t.Fatal("the open invitation was not withdrawn")
		}
	})
}
