package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserByEmail resolves a notification address to an account.
//
// Used by the password-reset request, which is why the lookup is by lower(email)
// — matching the partial unique index — rather than by the raw string.
func (s *Store) UserByEmail(ctx context.Context, email string) (id, username string, err error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return "", "", ErrNotFound
	}
	err = s.pool.QueryRow(ctx,
		`SELECT id, username FROM users WHERE lower(email)=$1 AND disabled_at IS NULL`, normalized).Scan(&id, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return id, username, err
}

// CreatePasswordReset issues a reset for a user, invalidating any earlier one
// that is still outstanding.
//
// Only the hash is stored, so a database leak cannot hand out working reset
// links — the same reasoning as the agent enrollment tokens and sessions.
// Superseding the previous reset means a user who clicks "forgot password"
// twice cannot be confused by two live links, and it bounds how many valid
// tokens can exist for one account at any moment.
func (s *Store) CreatePasswordReset(ctx context.Context, userID, tokenHash string, expires time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO password_resets (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		newID(), userID, tokenHash, expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConsumePasswordReset redeems a reset token exactly once and returns its user.
//
// Single-use is enforced by the UPDATE itself rather than by a read followed by
// a write: two concurrent requests carrying the same token both match the row,
// but only one gets it with used_at still NULL, so only one can proceed. An
// expired or already-used token is indistinguishable from an unknown one, which
// is what the caller needs to avoid leaking whether a token ever existed.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash string) (userID string, err error) {
	err = s.pool.QueryRow(ctx,
		`UPDATE password_resets SET used_at=now()
		 WHERE token_hash=$1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

// PasswordResetUser returns the user a live reset token belongs to, without
// redeeming it. The confirm step checks the new password against it first, so
// a rejected password leaves the link usable — ConsumePasswordReset still makes
// the redemption itself single-use.
func (s *Store) PasswordResetUser(ctx context.Context, tokenHash string) (userID string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT user_id FROM password_resets
		 WHERE token_hash=$1 AND used_at IS NULL AND expires_at > now()`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

// PasswordResetsSince counts the resets issued to a user since a moment, used
// and superseded ones included: it is how many reset mails were sent, which is
// what a per-account throttle has to bound.
func (s *Store) PasswordResetsSince(ctx context.Context, userID string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM password_resets WHERE user_id=$1 AND created_at > $2`, userID, since).Scan(&n)
	return n, err
}

// UserLinkedToSSO reports whether an account signs in through the identity
// provider: linked to a provider identity, or marked to be (sso_required)
// before its first single sign-on links it.
func (s *Store) UserLinkedToSSO(ctx context.Context, userID string) (bool, error) {
	var linked bool
	err := s.pool.QueryRow(ctx, `SELECT oidc_subject <> '' OR sso_required FROM users WHERE id=$1`, userID).Scan(&linked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return linked, err
}

// SetUserSSORequired marks an account as signing in through single sign-on
// only, before or after its first sign-in, or clears the mark. Clearing it does
// not unlink an identity already linked.
func (s *Store) SetUserSSORequired(ctx context.Context, userID string, required bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET sso_required=$2 WHERE id=$1`, userID, required)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
