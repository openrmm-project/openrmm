package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ===================== Confirmed alert recipients =====================
//
// Through the operator's shared alert service, mail goes only to addresses
// their owners confirmed (see migration 017). A confirmation link is bound to
// the address it was sent to: if the address changes before the link is used,
// the link confirms nothing.

// CreateEmailVerification issues a confirmation link for a user's address,
// superseding any outstanding one. Only the token's hash is stored.
func (s *Store) CreateEmailVerification(ctx context.Context, userID, email, tokenHash string, expires time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE email_verifications SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO email_verifications (token_hash, user_id, email, expires_at) VALUES ($1,$2,$3,$4)`,
		tokenHash, userID, strings.ToLower(strings.TrimSpace(email)), expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EmailVerificationsSince counts the confirmation mails sent to an address
// since a moment, whichever account asked: the throttle protects a mailbox,
// and a tenant administrator could otherwise aim one address from many
// accounts.
func (s *Store) EmailVerificationsSince(ctx context.Context, email string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM email_verifications WHERE email=lower($1) AND created_at > $2`,
		strings.TrimSpace(email), since).Scan(&n)
	return n, err
}

// AllEmailVerificationsSince counts every confirmation mail the instance sent
// since a moment: a cap on how many addresses one tenant can mail through the
// shared service in a day.
func (s *Store) AllEmailVerificationsSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM email_verifications WHERE created_at > $1`, since).Scan(&n)
	return n, err
}

// ConfirmEmailVerification redeems a link once and marks the user's address
// confirmed — only if it is still the address the link was sent to. It returns
// the user id. Unknown, used, expired and outdated links are one answer.
func (s *Store) ConfirmEmailVerification(ctx context.Context, tokenHash string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var userID, email string
	err = tx.QueryRow(ctx,
		`UPDATE email_verifications SET used_at=now()
		 WHERE token_hash=$1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id, email`, tokenHash).Scan(&userID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	ct, err := tx.Exec(ctx,
		`UPDATE users SET email_verified_at=now()
		 WHERE id=$1 AND lower(COALESCE(email,'')) = $2 AND disabled_at IS NULL`, userID, email)
	if err != nil {
		return "", err
	}
	if ct.RowsAffected() == 0 {
		// The address changed since the link was sent (or the account was
		// disabled): the link is spent and confirms nothing.
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", ErrNotFound
	}
	return userID, tx.Commit(ctx)
}

// RecipientConfirmed reports whether a queued message's recipient is still
// that user's confirmed address. Checked again at delivery: the address may
// have changed after the message was queued.
func (s *Store) RecipientConfirmed(ctx context.Context, userID, email string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id=$1 AND lower(COALESCE(email,'')) = lower($2)
		   AND email_verified_at IS NOT NULL AND disabled_at IS NULL)`, userID, email).Scan(&ok)
	return ok, err
}

// NotificationsSentSince counts alert mail delivered since a moment — the
// shared service's hourly budget per instance.
func (s *Store) NotificationsSentSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE status='sent' AND sent_at > $1`, since).Scan(&n)
	return n, err
}
