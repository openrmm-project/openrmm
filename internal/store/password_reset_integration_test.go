package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestPasswordResetIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "store_pwreset", url)

	user, err := st.CreateUser(ctx, "operator", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, user.ID, "Ops@Example.Test"); err != nil {
		t.Fatal(err)
	}

	t.Run("lookup is case-insensitive", func(t *testing.T) {
		id, username, err := st.UserByEmail(ctx, "  OPS@EXAMPLE.TEST ")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if id != user.ID || username != "operator" {
			t.Errorf("got (%s, %s), want (%s, operator)", id, username, user.ID)
		}
		if _, _, err := st.UserByEmail(ctx, "nobody@example.test"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown address: %v, want ErrNotFound", err)
		}
		if _, _, err := st.UserByEmail(ctx, "   "); !errors.Is(err, ErrNotFound) {
			t.Errorf("blank address: %v, want ErrNotFound", err)
		}
	})

	t.Run("a new request supersedes the outstanding one", func(t *testing.T) {
		if err := st.CreatePasswordReset(ctx, user.ID, "hash-old", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := st.CreatePasswordReset(ctx, user.ID, "hash-new", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConsumePasswordReset(ctx, "hash-old"); !errors.Is(err, ErrNotFound) {
			t.Errorf("superseded token: %v, want ErrNotFound", err)
		}
		if got, err := st.ConsumePasswordReset(ctx, "hash-new"); err != nil || got != user.ID {
			t.Errorf("newest token: got (%s, %v), want (%s, nil)", got, err, user.ID)
		}
	})

	t.Run("a consumed token cannot be reused", func(t *testing.T) {
		if err := st.CreatePasswordReset(ctx, user.ID, "hash-once", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConsumePasswordReset(ctx, "hash-once"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConsumePasswordReset(ctx, "hash-once"); !errors.Is(err, ErrNotFound) {
			t.Errorf("replayed token: %v, want ErrNotFound", err)
		}
	})

	t.Run("an expired token is rejected", func(t *testing.T) {
		if err := st.CreatePasswordReset(ctx, user.ID, "hash-stale", time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConsumePasswordReset(ctx, "hash-stale"); !errors.Is(err, ErrNotFound) {
			t.Errorf("expired token: %v, want ErrNotFound", err)
		}
	})

	t.Run("an unknown token is indistinguishable from a spent one", func(t *testing.T) {
		if _, err := st.ConsumePasswordReset(ctx, "hash-never-issued"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown token: %v, want ErrNotFound", err)
		}
	})

	// Two clicks on the same link, or a link shared with an attacker who races
	// the owner: the UPDATE must hand the token to exactly one caller.
	t.Run("concurrent redemption yields a single winner", func(t *testing.T) {
		if err := st.CreatePasswordReset(ctx, user.ID, "hash-race", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		var winners, losers atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				switch _, err := st.ConsumePasswordReset(ctx, "hash-race"); {
				case err == nil:
					winners.Add(1)
				case errors.Is(err, ErrNotFound):
					losers.Add(1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		wg.Wait()
		if winners.Load() != 1 || losers.Load() != 7 {
			t.Fatalf("winners=%d losers=%d, want exactly one winner", winners.Load(), losers.Load())
		}
	})
}
