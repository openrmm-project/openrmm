package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// ErrRemoteShareCode is returned when a guest's code does not match. The share
// stays open until too many wrong codes revoke it.
var ErrRemoteShareCode = errors.New("wrong code")

const remoteShareCols = `id, agent_id, created_by, mode, status, failed_attempts, guest_name, guest_ip,
	guest_user_agent, created_at, expires_at, requested_at, decided_at, ended_at, reusable, redeem_count`

// redeemableShare is the condition under which a link's code is checked: a
// link never used, or one the technician let the guest come back to while it
// is still live. A rejected, revoked or ended invitation is over either way.
const redeemableShare = `(status='open' OR (reusable AND status IN ('pending','accepted'))) AND expires_at > now()`

func scanRemoteShare(row pgx.Row) (*models.RemoteShare, error) {
	var s models.RemoteShare
	err := row.Scan(&s.ID, &s.AgentID, &s.CreatedBy, &s.Mode, &s.Status, &s.FailedAttempts, &s.GuestName,
		&s.GuestIP, &s.GuestUserAgent, &s.CreatedAt, &s.ExpiresAt, &s.RequestedAt, &s.DecidedAt, &s.EndedAt,
		&s.Reusable, &s.RedeemCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func scanRemoteShares(rows pgx.Rows) ([]models.RemoteShare, error) {
	defer rows.Close()
	out := []models.RemoteShare{}
	for rows.Next() {
		s, err := scanRemoteShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// CreateRemoteShare records an invitation. Only the hashes of its token and
// code are kept: the plaintext is shown to the technician once and nowhere else.
func (s *Store) CreateRemoteShare(ctx context.Context, agentID, createdBy, mode, tokenHash, codeHash string, expires time.Time, reusable bool) (*models.RemoteShare, error) {
	return scanRemoteShare(s.pool.QueryRow(ctx,
		`INSERT INTO remote_shares (id, agent_id, created_by, mode, token_hash, code_hash, expires_at, reusable)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+remoteShareCols,
		newID(), agentID, createdBy, mode, tokenHash, codeHash, expires, reusable))
}

// GetRemoteShare returns one share by id, whatever its state.
func (s *Store) GetRemoteShare(ctx context.Context, id string) (*models.RemoteShare, error) {
	return scanRemoteShare(s.pool.QueryRow(ctx, `SELECT `+remoteShareCols+` FROM remote_shares WHERE id=$1`, id))
}

// RemoteSharesForCreator lists one technician's live invitations on a device:
// open, waiting for a decision, or with the guest in.
func (s *Store) RemoteSharesForCreator(ctx context.Context, agentID, createdBy string) ([]models.RemoteShare, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+remoteShareCols+` FROM remote_shares
		WHERE agent_id=$1 AND created_by=$2 AND status IN ('open','pending','accepted') AND expires_at > now()
		ORDER BY created_at`, agentID, createdBy)
	if err != nil {
		return nil, err
	}
	return scanRemoteShares(rows)
}

// OpenRemoteShareByToken resolves an invitation link whose code may still be
// entered: unused, or reusable and live. An unknown, used, revoked or expired
// token is indistinguishable: the guest page only learns that the link does
// not work.
func (s *Store) OpenRemoteShareByToken(ctx context.Context, tokenHash string) (*models.RemoteShare, error) {
	return scanRemoteShare(s.pool.QueryRow(ctx, `SELECT `+remoteShareCols+` FROM remote_shares
		WHERE token_hash=$1 AND `+redeemableShare, tokenHash))
}

// RedeemRemoteShare checks a guest's code and, when it matches, burns the link:
// the share moves to pending and is bound to the guest's own credential, so a
// second visitor with the same link and code gets nothing. A reusable share
// accepts the code again while it is live: the new credential replaces the
// previous one, which stops working, and the guest waits for the technician
// again. A wrong code counts against the share, and the maxAttempts-th wrong
// one revokes it.
//
// The row is locked for the check, so two guesses racing each other are
// counted one after the other rather than both read the same count.
func (s *Store) RedeemRemoteShare(ctx context.Context, tokenHash, codeHash, guestSecretHash, name, ip, userAgent string, maxAttempts int) (*models.RemoteShare, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id, storedCode string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id, code_hash, failed_attempts FROM remote_shares
		WHERE token_hash=$1 AND `+redeemableShare+` FOR UPDATE`, tokenHash).Scan(&id, &storedCode, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(storedCode), []byte(codeHash)) != 1 {
		status := models.RemoteShareOpen
		if attempts+1 >= maxAttempts {
			status = models.RemoteShareRevoked
		}
		if _, err := tx.Exec(ctx, `UPDATE remote_shares SET failed_attempts=failed_attempts+1, status=$2,
			ended_at=CASE WHEN $2='revoked' THEN now() ELSE ended_at END WHERE id=$1`, id, status); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrRemoteShareCode
	}
	share, err := scanRemoteShare(tx.QueryRow(ctx, `UPDATE remote_shares SET status='pending', guest_secret_hash=$2,
		guest_name=$3, guest_ip=$4, guest_user_agent=$5, requested_at=now(), decided_at=NULL,
		redeem_count=redeem_count+1
		WHERE id=$1 RETURNING `+remoteShareCols, id, guestSecretHash, name, ip, userAgent))
	if err != nil {
		return nil, err
	}
	return share, tx.Commit(ctx)
}

// RemoteShareByGuestSecret finds the share a guest's credential belongs to, in
// whatever state it is: the guest page polls it to learn the decision.
func (s *Store) RemoteShareByGuestSecret(ctx context.Context, secretHash string) (*models.RemoteShare, error) {
	if secretHash == "" {
		return nil, ErrNotFound
	}
	return scanRemoteShare(s.pool.QueryRow(ctx, `SELECT `+remoteShareCols+` FROM remote_shares
		WHERE guest_secret_hash=$1`, secretHash))
}

// DecideRemoteShare records the technician's answer to a waiting guest. Only
// the share's creator decides, and only while the guest is still waiting.
func (s *Store) DecideRemoteShare(ctx context.Context, id, createdBy string, accept bool) (*models.RemoteShare, error) {
	status := models.RemoteShareRejected
	if accept {
		status = models.RemoteShareAccepted
	}
	return scanRemoteShare(s.pool.QueryRow(ctx, `UPDATE remote_shares SET status=$3, decided_at=now()
		WHERE id=$1 AND created_by=$2 AND status='pending' AND expires_at > now()
		RETURNING `+remoteShareCols, id, createdBy, status))
}

// CloseRemoteShare withdraws a live share: revoked when the guest never got
// in, ended when they did.
func (s *Store) CloseRemoteShare(ctx context.Context, id string) (*models.RemoteShare, error) {
	return scanRemoteShare(s.pool.QueryRow(ctx, `UPDATE remote_shares
		SET status=CASE WHEN status='accepted' THEN 'ended' ELSE 'revoked' END, ended_at=now()
		WHERE id=$1 AND status IN ('open','pending','accepted') RETURNING `+remoteShareCols, id))
}

// CloseAllRemoteShares withdraws every live share, for when guest access is
// turned off for the instance.
func (s *Store) CloseAllRemoteShares(ctx context.Context) ([]models.RemoteShare, error) {
	rows, err := s.pool.Query(ctx, `UPDATE remote_shares
		SET status=CASE WHEN status='accepted' THEN 'ended' ELSE 'revoked' END, ended_at=now()
		WHERE status IN ('open','pending','accepted') RETURNING `+remoteShareCols)
	if err != nil {
		return nil, err
	}
	return scanRemoteShares(rows)
}

// GetRemoteShareByTokenHash returns the share a link belongs to, in whatever
// state it is, for the audit line of a failed code.
func (s *Store) GetRemoteShareByTokenHash(ctx context.Context, tokenHash string) (*models.RemoteShare, error) {
	return scanRemoteShare(s.pool.QueryRow(ctx, `SELECT `+remoteShareCols+` FROM remote_shares WHERE token_hash=$1`, tokenHash))
}
