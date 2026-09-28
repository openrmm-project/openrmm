package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ===================== MCP OAuth 2.1 (Authorization Server) =====================
// Codes and tokens are stored HASHED (SHA-256) by the caller — never in the
// clear — the same pattern as api_tokens/sessions.

// --- clients (Dynamic Client Registration) ---

func (s *Store) CreateOAuthClient(ctx context.Context, clientID, name string, redirectURIs []string) error {
	return s.CreateOAuthClientWithStatus(ctx, clientID, name, redirectURIs, OAuthClientApproved)
}

// Application statuses. A pending application may not authorize until an
// administrator approves it.
const (
	OAuthClientApproved = "approved"
	OAuthClientPending  = "pending"
)

// CreateOAuthClientWithStatus registers an application as approved or pending.
func (s *Store) CreateOAuthClientWithStatus(ctx context.Context, clientID, name string, redirectURIs []string, status string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_clients (client_id, name, redirect_uris, status) VALUES ($1,$2,$3,$4)`,
		clientID, name, redirectURIs, status)
	return err
}

// OAuthClient is a registered MCP application as the administrator sees it.
type OAuthClient struct {
	ClientID     string     `json:"client_id"`
	Name         string     `json:"name"`
	RedirectURIs []string   `json:"redirect_uris"`
	Status       string     `json:"status"`
	ApprovedBy   string     `json:"approved_by"`
	ApprovedAt   *time.Time `json:"approved_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
	// ActiveTokens counts tokens that can still be used or refreshed.
	ActiveTokens int `json:"active_tokens"`
}

const oauthClientCols = `c.client_id, c.name, c.redirect_uris, c.status, c.approved_by, c.approved_at, c.last_used_at, c.created_at,
	(SELECT count(*) FROM oauth_tokens t WHERE t.client_id = c.client_id
	   AND (t.expires_at > now() OR t.refresh_expires_at > now()))`

func scanOAuthClient(row pgx.Row) (*OAuthClient, error) {
	var c OAuthClient
	err := row.Scan(&c.ClientID, &c.Name, &c.RedirectURIs, &c.Status, &c.ApprovedBy, &c.ApprovedAt, &c.LastUsedAt, &c.CreatedAt, &c.ActiveTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetOAuthClientInfo returns one application with its status.
func (s *Store) GetOAuthClientInfo(ctx context.Context, clientID string) (*OAuthClient, error) {
	return scanOAuthClient(s.pool.QueryRow(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients c WHERE c.client_id=$1`, clientID))
}

// ListOAuthClients lists every registered application, pending ones first.
func (s *Store) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients c
		ORDER BY (c.status = 'pending') DESC, c.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthClient{}
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ApproveOAuthClient lets a pending application authorize.
func (s *Store) ApproveOAuthClient(ctx context.Context, clientID, approvedBy string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE oauth_clients SET status='approved', approved_by=$2, approved_at=now()
		WHERE client_id=$1`, clientID, approvedBy)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOAuthClient revokes an application and everything it holds: tokens
// (access and refresh), unredeemed codes and single sign-on grants. Tokens
// have no foreign key to the application, so they are deleted here, in the
// same transaction, rather than left to work until they expire.
func (s *Store) DeleteOAuthClient(ctx context.Context, clientID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		`DELETE FROM oauth_tokens WHERE client_id=$1`,
		`DELETE FROM oauth_codes WHERE client_id=$1`,
		`DELETE FROM oauth_sso_grants WHERE client_id=$1`,
	} {
		if _, err := tx.Exec(ctx, q, clientID); err != nil {
			return err
		}
	}
	ct, err := tx.Exec(ctx, `DELETE FROM oauth_clients WHERE client_id=$1`, clientID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// TouchOAuthClient records that an application just obtained a token.
func (s *Store) TouchOAuthClient(ctx context.Context, clientID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE oauth_clients SET last_used_at=now() WHERE client_id=$1`, clientID)
	return err
}

func (s *Store) GetOAuthClient(ctx context.Context, clientID string) (name string, redirectURIs []string, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT name, redirect_uris FROM oauth_clients WHERE client_id=$1`, clientID).
		Scan(&name, &redirectURIs)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return name, redirectURIs, true, nil
}

// --- authorization codes (PKCE) ---

func (s *Store) CreateOAuthCode(ctx context.Context, codeHash, clientID, userID, redirectURI, codeChallenge, scope string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_codes (code_hash, client_id, user_id, redirect_uri, code_challenge, scope, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		codeHash, clientID, userID, redirectURI, codeChallenge, scope, expiresAt)
	return err
}

// ConsumeOAuthCode atomically deletes and returns a non-expired code (single use).
func (s *Store) ConsumeOAuthCode(ctx context.Context, codeHash string) (clientID, userID, redirectURI, codeChallenge, scope string, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`DELETE FROM oauth_codes WHERE code_hash=$1 AND expires_at > now()
		 RETURNING client_id, user_id::text, redirect_uri, code_challenge, scope`, codeHash).
		Scan(&clientID, &userID, &redirectURI, &codeChallenge, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", "", "", false, err
	}
	return clientID, userID, redirectURI, codeChallenge, scope, true, nil
}

// --- access + refresh tokens ---

func (s *Store) CreateOAuthToken(ctx context.Context, tokenHash string, refreshHash *string, clientID, userID, scope string, expiresAt, refreshExpiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_tokens (token_hash, refresh_hash, client_id, user_id, scope, expires_at, refresh_expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		tokenHash, refreshHash, clientID, userID, scope, expiresAt, refreshExpiresAt)
	return err
}

// OAuthTokenPrincipal resolves a non-expired access token to its bound user and
// that user's role (for authMiddleware). Empty ok when unknown/expired.
func (s *Store) OAuthTokenPrincipal(ctx context.Context, tokenHash string) (userID, role string, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT o.user_id::text, u.role FROM oauth_tokens o JOIN users u ON u.id = o.user_id
		 JOIN oauth_clients c ON c.client_id = o.client_id AND c.status = 'approved'
		 WHERE o.token_hash=$1 AND o.expires_at > now()`, tokenHash).
		Scan(&userID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return userID, role, true, nil
}

// PurgeExpiredOAuth deletes expired authorization codes and access tokens. Codes
// are one-shot and tokens carry an expiry, so stale rows are already unusable;
// this just keeps the tables from growing. Returns the number of rows removed.
func (s *Store) PurgeExpiredOAuth(ctx context.Context) (int64, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM oauth_codes WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	n := ct.RowsAffected()
	// A row is dead only once its refresh token is also expired (or absent) — the
	// access token expires sooner but the refresh keeps the row alive until then.
	ct, err = s.pool.Exec(ctx, `DELETE FROM oauth_tokens
		WHERE (refresh_expires_at IS NULL OR refresh_expires_at <= now()) AND expires_at <= now()`)
	if err != nil {
		return n, err
	}
	return n + ct.RowsAffected(), nil
}

// ConsumeOAuthRefresh atomically deletes a refresh token (rotation) and returns
// its binding, so the caller can issue a fresh access+refresh pair.
func (s *Store) ConsumeOAuthRefresh(ctx context.Context, refreshHash string) (clientID, userID, scope string, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`DELETE FROM oauth_tokens t WHERE t.refresh_hash=$1
		 AND (t.refresh_expires_at IS NULL OR t.refresh_expires_at > now())
		 AND EXISTS (SELECT 1 FROM oauth_clients c WHERE c.client_id = t.client_id AND c.status = 'approved')
		 RETURNING t.client_id, t.user_id::text, t.scope`, refreshHash).
		Scan(&clientID, &userID, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return clientID, userID, scope, true, nil
}
