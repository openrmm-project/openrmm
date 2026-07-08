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
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_clients (client_id, name, redirect_uris) VALUES ($1,$2,$3)`,
		clientID, name, redirectURIs)
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

func (s *Store) CreateOAuthToken(ctx context.Context, tokenHash string, refreshHash *string, clientID, userID, scope string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_tokens (token_hash, refresh_hash, client_id, user_id, scope, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		tokenHash, refreshHash, clientID, userID, scope, expiresAt)
	return err
}

// OAuthTokenPrincipal resolves a non-expired access token to its bound user and
// that user's role (for authMiddleware). Empty ok when unknown/expired.
func (s *Store) OAuthTokenPrincipal(ctx context.Context, tokenHash string) (userID, role string, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT o.user_id::text, u.role FROM oauth_tokens o JOIN users u ON u.id = o.user_id
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

// ConsumeOAuthRefresh atomically deletes a refresh token (rotation) and returns
// its binding, so the caller can issue a fresh access+refresh pair.
func (s *Store) ConsumeOAuthRefresh(ctx context.Context, refreshHash string) (clientID, userID, scope string, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`DELETE FROM oauth_tokens WHERE refresh_hash=$1
		 RETURNING client_id, user_id::text, scope`, refreshHash).
		Scan(&clientID, &userID, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return clientID, userID, scope, true, nil
}
