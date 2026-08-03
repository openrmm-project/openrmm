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
		`SELECT id, username FROM users WHERE lower(email)=$1`, normalized).Scan(&id, &username)
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
