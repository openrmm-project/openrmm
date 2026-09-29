package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// A confirmation link confirms the address it was sent to, once, before it
// expires — and a new address is unconfirmed until its own link is used.
func TestEmailVerificationIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "store_emailverify", url)
	user, err := st.CreateUser(ctx, "recipient", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, user.ID, "Ana@Example.Test"); err != nil {
		t.Fatal(err)
	}
	verified := func() bool {
		t.Helper()
		u, err := st.GetUserByID(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		return u.EmailVerifiedAt != nil
	}
	if verified() {
		t.Fatal("a new address starts confirmed")
	}

	if err := st.CreateEmailVerification(ctx, user.ID, "ana@example.test", "h1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if id, err := st.ConfirmEmailVerification(ctx, "h1"); err != nil || id != user.ID || !verified() {
		t.Fatalf("confirm: id=%q err=%v verified=%v", id, err, verified())
	}
	if _, err := st.ConfirmEmailVerification(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a replayed link: %v, want ErrNotFound", err)
	}
	if ok, _ := st.RecipientConfirmed(ctx, user.ID, "ANA@example.test"); !ok {
		t.Error("the confirmed address should be a confirmed recipient, whatever its case")
	}

	// A change of case keeps the confirmation; a new address loses it.
	if _, err := st.UpdateUserEmail(ctx, user.ID, "ANA@EXAMPLE.TEST"); err != nil || !verified() {
		t.Errorf("a change of case dropped the confirmation (err=%v)", err)
	}
	if _, err := st.UpdateUserEmail(ctx, user.ID, "other@example.test"); err != nil || verified() {
		t.Fatalf("a new address kept the old confirmation (err=%v)", err)
	}
	if ok, _ := st.RecipientConfirmed(ctx, user.ID, "ana@example.test"); ok {
		t.Error("the old address is still a confirmed recipient")
	}

	// A link sent to the old address confirms nothing once the address changed.
	if err := st.CreateEmailVerification(ctx, user.ID, "other@example.test", "h2", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, user.ID, "third@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmEmailVerification(ctx, "h2"); !errors.Is(err, ErrNotFound) || verified() {
		t.Errorf("a link for a previous address confirmed the new one (err=%v)", err)
	}

	// Expired, and superseded by a newer link.
	if err := st.CreateEmailVerification(ctx, user.ID, "third@example.test", "h3", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmEmailVerification(ctx, "h3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired link: %v", err)
	}
	if err := st.CreateEmailVerification(ctx, user.ID, "third@example.test", "h4", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEmailVerification(ctx, user.ID, "third@example.test", "h5", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConfirmEmailVerification(ctx, "h4"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a superseded link still worked: %v", err)
	}
	// Counted per address, whichever account asked, and per instance.
	if n, err := st.EmailVerificationsSince(ctx, "THIRD@example.test", time.Now().Add(-time.Hour)); err != nil || n != 3 {
		t.Errorf("links to third@: %d (%v), want 3", n, err)
	}
	other, err := st.CreateUser(ctx, "someone-else", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEmailVerification(ctx, other.ID, "third@example.test", "h6", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.EmailVerificationsSince(ctx, "third@example.test", time.Now().Add(-time.Hour)); n != 4 {
		t.Errorf("a second account's link to the same address is not counted: %d, want 4", n)
	}
	if n, _ := st.AllEmailVerificationsSince(ctx, time.Now().Add(-time.Hour)); n != 6 {
		t.Errorf("instance total = %d, want 6", n)
	}
}
