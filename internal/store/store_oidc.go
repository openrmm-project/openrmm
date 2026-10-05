package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// ===================== OIDC sign-in =====================
//
// Two things live here: the short-lived record of a sign-in that is in flight,
// and the link between a console account and an identity at a provider.
//
// The account link is (issuer, subject), never the email address. Addresses are
// reassigned when people leave a company; `sub` is the one claim OIDC
// guarantees is stable and unique within an issuer. Matching on email would
// mean whoever inherits alice@ inherits Alice's console.

// OIDCLogin is an in-flight sign-in, as returned when the callback consumes it.
type OIDCLogin struct {
	Nonce        string
	CodeVerifier string
	RedirectTo   string
	// OAuthRequest is the MCP authorization request (JSON) this sign-in was
	// started for, or nil for a console sign-in.
	OAuthRequest []byte
}

// CreateOIDCState records a sign-in that has just been started.
//
// stateHash is the SHA-256 of the state parameter, so a dump of this table does
// not hand anyone a usable in-flight login — the same rule sessions and API
// tokens follow.
func (s *Store) CreateOIDCState(ctx context.Context, stateHash, nonce, codeVerifier, redirectTo string, expires time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oidc_login_states (state_hash, nonce, code_verifier, redirect_to, expires_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		stateHash, nonce, codeVerifier, redirectTo, expires)
	return err
}

// CreateOIDCStateForOAuth records a sign-in started from the MCP consent form.
// The authorization request travels with it, so the callback can finish that
// authorization instead of opening a console session.
func (s *Store) CreateOIDCStateForOAuth(ctx context.Context, stateHash, nonce, codeVerifier string, oauthRequest []byte, expires time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oidc_login_states (state_hash, nonce, code_verifier, oauth_request, expires_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		stateHash, nonce, codeVerifier, oauthRequest, expires)
	return err
}

// ConsumeOIDCState returns a pending sign-in and deletes it in the same
// statement.
//
// Single use is the point, and doing it in one round trip is what makes it
// single use: a read followed by a delete leaves a window in which the same
// state can be redeemed twice, which is the replay this is meant to prevent.
// An expired row is not returned and not resurrected.
func (s *Store) ConsumeOIDCState(ctx context.Context, stateHash string) (*OIDCLogin, error) {
	var out OIDCLogin
	err := s.pool.QueryRow(ctx,
		`DELETE FROM oidc_login_states
		  WHERE state_hash = $1 AND expires_at > now()
		  RETURNING nonce, code_verifier, redirect_to, oauth_request`, stateHash).
		Scan(&out.Nonce, &out.CodeVerifier, &out.RedirectTo, &out.OAuthRequest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PurgeExpiredOIDCStates drops abandoned sign-ins. Someone who opens the login
// page and walks away leaves a row behind; without this they accumulate
// forever.
func (s *Store) PurgeExpiredOIDCStates(ctx context.Context) (int64, error) {
	if _, err := s.pool.Exec(ctx, `DELETE FROM oidc_console_handoffs WHERE expires_at <= now()`); err != nil {
		return 0, err
	}
	ct, err := s.pool.Exec(ctx, `DELETE FROM oidc_login_states WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

func (s *Store) OIDCStateForBegin(ctx context.Context, stateHash string) (*OIDCLogin, error) {
	var out OIDCLogin
	err := s.pool.QueryRow(ctx, `SELECT nonce, code_verifier, oauth_request FROM oidc_login_states
		WHERE state_hash=$1 AND expires_at > now() AND oauth_request IS NOT NULL`, stateHash).
		Scan(&out.Nonce, &out.CodeVerifier, &out.OAuthRequest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &out, err
}

func (s *Store) CreateOIDCHandoff(ctx context.Context, ticketHash, userID, browserHash string, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO oidc_console_handoffs (ticket_hash,user_id,browser_hash,expires_at) VALUES ($1,$2,$3,$4)`, ticketHash, userID, browserHash, expires)
	return err
}

// Consumption and session creation are atomic, including expiry, browser and
// disabled-account checks. A code cannot create two sessions concurrently.
func (s *Store) ExchangeOIDCHandoff(ctx context.Context, ticketHash, browserHash, tokenHash string, expires time.Time) (string, error) {
	var userID string
	err := s.pool.QueryRow(ctx, `WITH handoff AS (
		DELETE FROM oidc_console_handoffs WHERE ticket_hash=$1 AND browser_hash=$2 AND expires_at > now() RETURNING user_id
	) INSERT INTO sessions (id,user_id,token_hash,expires_at)
		SELECT $3, g.user_id, $4, $5 FROM handoff g JOIN users u ON u.id=g.user_id WHERE u.disabled_at IS NULL RETURNING user_id`, ticketHash, browserHash, newID(), tokenHash, expires).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

// GetUserByOIDC finds the console account linked to a provider identity.
//
// A disabled account is not returned, which is what makes disabling a user here
// enough: without that clause, someone removed from the console could sign
// straight back in through the provider.
func (s *Store) GetUserByOIDC(ctx context.Context, issuer, subject string) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, role FROM users
		  WHERE oidc_issuer = $1 AND oidc_subject = $2 AND disabled_at IS NULL`,
		issuer, subject).Scan(&u.ID, &u.Username, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// LinkOIDCIdentity attaches a provider identity to an existing account.
//
// This is how an operator's local account becomes their SSO account: they are
// created here first, and the link is made the first time they sign in through
// the provider. The WHERE clause refuses to move a link that already exists —
// relinking an account to a different identity is an account takeover, not a
// convenience.
func (s *Store) LinkOIDCIdentity(ctx context.Context, userID, issuer, subject string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE users SET oidc_issuer = $2, oidc_subject = $3
		  WHERE id = $1 AND oidc_subject = ''`,
		userID, issuer, subject)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateOIDCUser creates an account for someone the console has never seen,
// already linked to their provider identity.
//
// The password hash is deliberately empty. bcrypt cannot verify against an
// empty hash, so the account has no password rather than a guessable one, and
// local sign-in for it fails the way any wrong password does. An operator who
// wants to give it one can, through the normal reset.
func (s *Store) CreateOIDCUser(ctx context.Context, username, role, issuer, subject string) (*models.User, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, username, password_hash, role, oidc_issuer, oidc_subject)
		 VALUES ($1,$2,'',$3,$4,$5)`,
		id, username, role, issuer, subject)
	if err != nil {
		return nil, err
	}
	return &models.User{ID: id, Username: username, Role: role, CreatedAt: time.Now()}, nil
}

// ===================== MCP authorization through SSO =====================

// OAuthSSOGrant is what redeeming a grant ticket returns. The caller decides;
// the store only reports, so that every use — a match or not — spends it.
type OAuthSSOGrant struct {
	UserID, ClientID, RedirectURI, CodeChallenge string
	Live                                         bool // not expired
	UserActive                                   bool // not disabled
}

// CreateOAuthSSOGrant records that a user just signed in through the provider
// for one authorization request. Only the ticket's hash is stored.
func (s *Store) CreateOAuthSSOGrant(ctx context.Context, ticketHash, userID, clientID, redirectURI, codeChallenge string, expires time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_sso_grants (ticket_hash, user_id, client_id, redirect_uri, code_challenge, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		ticketHash, userID, clientID, redirectURI, codeChallenge, expires)
	return err
}

// ConsumeOAuthSSOGrant deletes a grant and returns what it was bound to.
//
// Deleted on any use, in the same statement: a ticket presented with the wrong
// client or challenge is spent, not left for another try, and two concurrent
// submissions cannot both redeem it.
func (s *Store) ConsumeOAuthSSOGrant(ctx context.Context, ticketHash string) (*OAuthSSOGrant, error) {
	var g OAuthSSOGrant
	err := s.pool.QueryRow(ctx,
		`WITH g AS (DELETE FROM oauth_sso_grants WHERE ticket_hash = $1 RETURNING *)
		 SELECT g.user_id, g.client_id, g.redirect_uri, g.code_challenge,
		        g.expires_at > now(), u.disabled_at IS NULL
		   FROM g JOIN users u ON u.id = g.user_id`, ticketHash).
		Scan(&g.UserID, &g.ClientID, &g.RedirectURI, &g.CodeChallenge, &g.Live, &g.UserActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// PurgeExpiredOAuthSSOGrants drops grants nobody came back for.
func (s *Store) PurgeExpiredOAuthSSOGrants(ctx context.Context) (int64, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM oauth_sso_grants WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
