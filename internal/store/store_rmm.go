package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/p1ngu/rmm/internal/models"
)

// ===================== Users & sessions =====================

// CreateUser inserts a user with an already-hashed password.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (*models.User, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, username, password_hash, role) VALUES ($1,$2,$3,$4)`,
		id, username, passwordHash, role)
	if err != nil {
		return nil, err
	}
	return &models.User{ID: id, Username: username, Role: role, CreatedAt: time.Now()}, nil
}

// CreateUserWithScopes creates a delegated user without a transient full-access
// window if inserting one of its scope rows fails.
func (s *Store) CreateUserWithScopes(ctx context.Context, username, passwordHash, role string, scopes []models.UserScope) (*models.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	u := &models.User{ID: newID(), Username: username, Role: role, CreatedAt: time.Now()}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, username, password_hash, role) VALUES ($1,$2,$3,$4)`,
		u.ID, username, passwordHash, role); err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_scopes (id, user_id, scope_type, scope_id) VALUES ($1,$2,$3,$4)`,
			newID(), u.ID, scope.ScopeType, scope.ScopeID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// CountUsers reports how many users exist (used to bootstrap the first admin).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(1) FROM users`).Scan(&n)
	return n, err
}

// BootstrapAdmin atomically creates the first user and its initial session.
// The transaction-scoped advisory lock serializes concurrent bootstrap calls.
func (s *Store) BootstrapAdmin(ctx context.Context, username, passwordHash, tokenHash string, expires time.Time) (*models.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(724686667797)`); err != nil {
		return nil, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrConflict
	}
	u := &models.User{ID: newID(), Username: username, Role: models.RoleAdmin, CreatedAt: time.Now()}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, username, password_hash, role) VALUES ($1,$2,$3,$4)`,
		u.ID, u.Username, passwordHash, u.Role); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		newID(), u.ID, tokenHash, expires); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserAuth returns the id, role and password hash for a username.
func (s *Store) GetUserAuth(ctx context.Context, username string) (id, role, passwordHash string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT id, role, password_hash FROM users WHERE username=$1 AND disabled_at IS NULL`, username).
		Scan(&id, &role, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	return id, role, passwordHash, err
}

// GetUserPasswordHash returns the bcrypt hash for a user id (for verifying the
// current password before a self-service change).
func (s *Store) GetUserPasswordHash(ctx context.Context, id string) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// UpdateUserPassword sets a new (already-hashed) password for a user id.
func (s *Store) UpdateUserPassword(ctx context.Context, id, passwordHash string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, id, passwordHash)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetUserTOTP returns a user's (encrypted) TOTP secret and whether 2FA is on.
func (s *Store) GetUserTOTP(ctx context.Context, id string) (secret string, enabled bool, err error) {
	var sec *string
	err = s.pool.QueryRow(ctx, `SELECT totp_secret, totp_enabled FROM users WHERE id=$1`, id).Scan(&sec, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if sec != nil {
		secret = *sec
	}
	return secret, enabled, err
}

// SetPendingUserTOTP stores an encrypted candidate without changing the active
// secret or weakening an already-enabled second factor.
func (s *Store) SetPendingUserTOTP(ctx context.Context, id, secret string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE users SET totp_pending_secret=$2 WHERE id=$1`, id, secret)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetPendingUserTOTP(ctx context.Context, id string) (string, error) {
	var secret *string
	err := s.pool.QueryRow(ctx, `SELECT totp_pending_secret FROM users WHERE id=$1`, id).Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if secret == nil {
		return "", nil
	}
	return *secret, nil
}

// EnablePendingUserTOTP atomically promotes the expected pending secret and
// replaces recovery codes.
func (s *Store) EnablePendingUserTOTP(ctx context.Context, id, secret string, recoveryHashes []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE users SET totp_secret=totp_pending_secret,
		totp_pending_secret=NULL, totp_enabled=TRUE, totp_last_step=NULL
		WHERE id=$1 AND totp_pending_secret=$2`, id, secret)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, id); err != nil {
		return err
	}
	for _, hash := range recoveryHashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_recovery_codes (id, user_id, code_hash) VALUES ($1,$2,$3)`,
			newID(), id, hash); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ConsumeTOTPStep spends a two-factor time step for the user, reporting false
// when that step (or a later one) was already accepted. The comparison and the
// write are one statement, so two sign-ins racing with the same code cannot
// both pass.
func (s *Store) ConsumeTOTPStep(ctx context.Context, id string, step int64) (bool, error) {
	ct, err := s.pool.Exec(ctx, `UPDATE users SET totp_last_step=$2
		WHERE id=$1 AND (totp_last_step IS NULL OR totp_last_step < $2)`, id, step)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// DisableUserTOTP atomically clears the secret and all recovery codes.
func (s *Store) DisableUserTOTP(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE users SET totp_secret=NULL, totp_pending_secret=NULL,
		totp_enabled=FALSE, totp_last_step=NULL WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetUserByID returns a user by id (no password hash).
func (s *Store) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, role, COALESCE(email,''), created_at, totp_enabled, disabled_at, email_verified_at FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.CreatedAt, &u.TOTPEnabled, &u.DisabledAt, &u.EmailVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ListUsers returns all users (without password hashes).
func (s *Store) ListUsers(ctx context.Context) ([]models.User, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, username, role, COALESCE(email,''), created_at, totp_enabled, disabled_at, email_verified_at,
		        oidc_subject <> '' OR sso_required FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.CreatedAt, &u.TOTPEnabled, &u.DisabledAt, &u.EmailVerifiedAt, &u.SSO); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserRole changes a console operator's role.
func (s *Store) UpdateUserRole(ctx context.Context, id, role string) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`UPDATE users SET role=$2 WHERE id=$1
		 RETURNING id, username, role, COALESCE(email,''), created_at, totp_enabled, disabled_at, email_verified_at`,
		id, role).Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.CreatedAt, &u.TOTPEnabled, &u.DisabledAt, &u.EmailVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUserEmail sets or clears a user's notification address.
//
// An empty address is stored as NULL rather than an empty string: the uniqueness
// index is partial, so several address-less accounts must not collide.
func (s *Store) UpdateUserEmail(ctx context.Context, id, email string) (*models.User, error) {
	var arg *string
	if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
		arg = &e
	}
	var u models.User
	err := s.pool.QueryRow(ctx,
		// email_verified_at survives only a change of case: a new address
		// has not been confirmed by anyone. SET sees the row as it was.
		`UPDATE users SET email_verified_at = CASE WHEN lower(COALESCE(email,'')) = lower(COALESCE($2,'')) THEN email_verified_at ELSE NULL END,
		                  email=$2 WHERE id=$1
		 RETURNING id, username, role, COALESCE(email,''), created_at, totp_enabled, disabled_at, email_verified_at`,
		id, arg).Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.CreatedAt, &u.TOTPEnabled, &u.DisabledAt, &u.EmailVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUserSessions ends every live session for a user. Used when access is
// withdrawn: a disable that leaves an open browser tab working is not a disable.
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}

// SetUserDisabled withdraws or restores an account's access.
//
// Idempotent on purpose: disabling an already-disabled user keeps the original
// timestamp, so a second click does not rewrite when access was actually
// withdrawn — the value an incident review depends on.
func (s *Store) SetUserDisabled(ctx context.Context, id string, disabled bool) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`UPDATE users SET disabled_at = CASE WHEN $2 THEN COALESCE(disabled_at, now()) ELSE NULL END
		 WHERE id=$1
		 RETURNING id, username, role, COALESCE(email,''), created_at, totp_enabled, disabled_at, email_verified_at`,
		id, disabled).Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.CreatedAt, &u.TOTPEnabled, &u.DisabledAt, &u.EmailVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUser removes a user.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	return err
}

// CreateSession stores a hashed session token.
func (s *Store) CreateSession(ctx context.Context, userID, tokenHash string, expires time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		newID(), userID, tokenHash, expires)
	return err
}

// SessionPrincipal resolves a session token hash to a user id + role, if valid.
func (s *Store) SessionPrincipal(ctx context.Context, tokenHash string) (userID, role string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT u.id, u.role FROM sessions se JOIN users u ON u.id = se.user_id
		 WHERE se.token_hash=$1 AND se.expires_at > now() AND u.disabled_at IS NULL`, tokenHash).Scan(&userID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return userID, role, err
}

// DeleteSession logs out a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

// DeleteUserSessionsExcept revokes all of a user's sessions but the given one
// (used after a password change so other/stale sessions can't keep the access).
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID, keepTokenHash string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2`, userID, keepTokenHash)
	return err
}

// APITokenRole returns the role and bound user id (empty if unscoped) for an API
// token hash, if it exists.
func (s *Store) APITokenRole(ctx context.Context, hash string) (role, userID string, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT t.role, COALESCE(t.user_id::text, '') FROM api_tokens t
		LEFT JOIN users u ON u.id = t.user_id
		WHERE t.token_hash=$1 AND (t.expires_at IS NULL OR t.expires_at > now())
		  AND (t.user_id IS NULL OR u.disabled_at IS NULL)`, hash).Scan(&role, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return role, userID, true, nil
}

// ---- per-user access scope ----

// ReplaceUserScopes sets a user's access scopes (empty = full access).
func (s *Store) ReplaceUserScopes(ctx context.Context, userID string, scopes []models.UserScope) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM user_scopes WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, sc := range scopes {
		if sc.ScopeType == "" || sc.ScopeID == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_scopes (id, user_id, scope_type, scope_id) VALUES ($1,$2,$3,$4)
			 ON CONFLICT (user_id, scope_type, scope_id) DO NOTHING`,
			newID(), userID, sc.ScopeType, sc.ScopeID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReplaceRecoveryCodes swaps a user's 2FA recovery codes for a fresh set of
// hashes (any prior codes, used or not, are discarded).
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID string, hashes []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_recovery_codes (id, user_id, code_hash) VALUES ($1,$2,$3)`,
			newID(), userID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UseRecoveryCode consumes a single unused recovery code by its hash, returning
// true if a matching unused code was found and marked used.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, codeHash string) (bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `UPDATE user_recovery_codes SET used_at=now()
		WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL
		RETURNING id`, userID, codeHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UserHasScopes distinguishes a user bound to an actual restricted scope from
// an unrestricted user, for whom no user_scopes rows exist.
func (s *Store) UserHasScopes(ctx context.Context, userID string) (bool, error) {
	var restricted bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)`, userID).Scan(&restricted)
	return restricted, err
}

// CountUnusedRecoveryCodes returns how many recovery codes the user has left.
func (s *Store) CountUnusedRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM user_recovery_codes WHERE user_id=$1 AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// DeleteRecoveryCodes removes all of a user's recovery codes (on disable/reset).
func (s *Store) DeleteRecoveryCodes(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1`, userID)
	return err
}

// ListUserScopes returns a user's scopes (empty slice = full access).
func (s *Store) ListUserScopes(ctx context.Context, userID string) ([]models.UserScope, error) {
	rows, err := s.pool.Query(ctx, `SELECT scope_type, scope_id FROM user_scopes WHERE user_id=$1 ORDER BY scope_type, scope_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.UserScope
	for rows.Next() {
		var sc models.UserScope
		if err := rows.Scan(&sc.ScopeType, &sc.ScopeID); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// AgentAllowedForUser reports whether a user may access an agent. A user with no
// scopes has full access; otherwise the agent must match one of the user's
// client/site/group/agent scopes.
func (s *Store) AgentAllowedForUser(ctx context.Context, userID, agentID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM agents WHERE id=$2) AND ((NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1))
		    OR EXISTS (
		      SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		        (us.scope_type='agent'  AND us.scope_id=$2) OR
		        (us.scope_type='group'  AND us.scope_id=(SELECT group_id FROM agents WHERE id=$2)) OR
		        (us.scope_type='site'   AND us.scope_id=(SELECT site_id FROM agents WHERE id=$2)) OR
		        (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM agents a JOIN sites s ON s.id=a.site_id WHERE a.id=$2))
		      )))`, userID, agentID).Scan(&ok)
	return ok, err
}

// ScopeAllowedForUser reports whether a user controls the complete referenced
// scope. Narrow child scopes do not authorize operations affecting their parent.
func (s *Store) ScopeExists(ctx context.Context, scopeType, scopeID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT CASE $1
		WHEN 'client' THEN EXISTS (SELECT 1 FROM clients WHERE id=$2)
		WHEN 'site' THEN EXISTS (SELECT 1 FROM sites WHERE id=$2)
		WHEN 'group' THEN EXISTS (SELECT 1 FROM device_groups WHERE id=$2)
		WHEN 'agent' THEN EXISTS (SELECT 1 FROM agents WHERE id=$2)
		ELSE FALSE END`, scopeType, scopeID).Scan(&ok)
	return ok, err
}

func (s *Store) ScopeAllowedForUser(ctx context.Context, userID, scopeType, scopeID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT CASE $2
		WHEN 'client' THEN EXISTS (SELECT 1 FROM clients c WHERE c.id=$3) AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1 AND scope_type='client' AND scope_id=$3))
		WHEN 'site' THEN EXISTS (SELECT 1 FROM sites si WHERE si.id=$3) AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='site' AND us.scope_id=$3)
		    OR (us.scope_type='client' AND us.scope_id=(SELECT client_id FROM sites WHERE id=$3)))))
		WHEN 'group' THEN EXISTS (SELECT 1 FROM device_groups g WHERE g.id=$3) AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='group' AND us.scope_id=$3)
		    OR (us.scope_type='site' AND us.scope_id=(SELECT site_id FROM device_groups WHERE id=$3))
		    OR (us.scope_type='client' AND us.scope_id=(SELECT si.client_id FROM device_groups g JOIN sites si ON si.id=g.site_id WHERE g.id=$3)))))
		WHEN 'agent' THEN EXISTS (SELECT 1 FROM agents a WHERE a.id=$3) AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='agent' AND us.scope_id=$3)
		    OR (us.scope_type='group' AND us.scope_id=(SELECT group_id FROM agents WHERE id=$3))
		    OR (us.scope_type='site' AND us.scope_id=(SELECT site_id FROM agents WHERE id=$3))
		    OR (us.scope_type='client' AND us.scope_id=(SELECT si.client_id FROM agents a JOIN sites si ON si.id=a.site_id WHERE a.id=$3)))))
		ELSE FALSE END`, userID, scopeType, scopeID).Scan(&ok)
	return ok, err
}

// ScopeIntersectsUser reports whether a referenced scope contains at least one
// device visible to the user. It is suitable for filtering assignment reads.
func (s *Store) ScopeIntersectsUser(ctx context.Context, userID, scopeType, scopeID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT
		NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR EXISTS (SELECT 1 FROM agents a LEFT JOIN sites si ON si.id=a.site_id
		  WHERE (($2='agent' AND a.id=$3)
		    OR ($2='group' AND a.group_id=$3)
		    OR ($2='site' AND a.site_id=$3)
		    OR ($2='client' AND si.client_id=$3))
		  AND EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='agent' AND us.scope_id=a.id)
		    OR (us.scope_type='group' AND us.scope_id=a.group_id)
		    OR (us.scope_type='site' AND us.scope_id=a.site_id)
		    OR (us.scope_type='client' AND us.scope_id=si.client_id))))`,
		userID, scopeType, scopeID).Scan(&ok)
	return ok, err
}

// ListAgentsForUser lists the agents a user may see (all when unscoped).
func (s *Store) ListAgentsForUser(ctx context.Context, userID string) ([]models.Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentCols+` FROM agents a WHERE `+agentVisible+`
		ORDER BY a.created_at DESC`, scopeArg(userID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ===================== Clients & sites =====================

// CreateClient inserts a client.
func (s *Store) CreateClient(ctx context.Context, name string) (*models.Client, error) {
	id := newID()
	if _, err := s.pool.Exec(ctx, `INSERT INTO clients (id, name) VALUES ($1,$2)`, id, name); err != nil {
		return nil, err
	}
	return &models.Client{ID: id, Name: name, CreatedAt: time.Now()}, nil
}

// ListClients returns all clients.
func (s *Store) ListClients(ctx context.Context) ([]models.Client, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at FROM clients ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Client
	for rows.Next() {
		var c models.Client
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListClientsForUser returns the clients a scoped user may see (all when
// unscoped) — those directly scoped, or parents of the user's site/group/agent scopes.
func (s *Store) ListClientsForUser(ctx context.Context, userID string) ([]models.Client, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at FROM clients c WHERE
		NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR c.id IN (
		  SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='client'
		  UNION SELECT s.client_id FROM user_scopes us JOIN sites s ON s.id=us.scope_id WHERE us.user_id=$1 AND us.scope_type='site'
		  UNION SELECT s.client_id FROM user_scopes us JOIN device_groups g ON g.id=us.scope_id JOIN sites s ON s.id=g.site_id WHERE us.user_id=$1 AND us.scope_type='group'
		  UNION SELECT s.client_id FROM user_scopes us JOIN agents a ON a.id=us.scope_id JOIN sites s ON s.id=a.site_id WHERE us.user_id=$1 AND us.scope_type='agent'
		) ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Client
	for rows.Next() {
		var c models.Client
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListSitesForUser returns the sites a scoped user may see (all when unscoped):
// directly scoped sites, sites under a scoped client, or sites of a scoped group/agent.
func (s *Store) ListSitesForUser(ctx context.Context, userID, clientID string) ([]models.Site, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, client_id, name, created_at FROM sites si WHERE
		($2 = '' OR si.client_id = NULLIF($2, '')::uuid) AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR si.id IN (SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='site')
		  OR si.client_id IN (SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='client')
		  OR si.id IN (SELECT g.site_id FROM user_scopes us JOIN device_groups g ON g.id=us.scope_id WHERE us.user_id=$1 AND us.scope_type='group')
		  OR si.id IN (SELECT a.site_id FROM user_scopes us JOIN agents a ON a.id=us.scope_id WHERE us.user_id=$1 AND us.scope_type='agent')
		) ORDER BY name`, userID, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Site
	for rows.Next() {
		var st models.Site
		if err := rows.Scan(&st.ID, &st.ClientID, &st.Name, &st.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SiteEnrollmentAllowedForUser reports whether a scoped user may mint an
// enrollment token for a site. Agent/group scope deliberately does not qualify:
// a site-bound token would otherwise create a new endpoint outside that narrow
// scope. Users with no scopes retain full access.
func (s *Store) SiteEnrollmentAllowedForUser(ctx context.Context, userID, siteID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sites WHERE id=$2) AND (NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR EXISTS (
		  SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='site' AND us.scope_id=$2)
		    OR (us.scope_type='client' AND us.scope_id=(SELECT client_id FROM sites WHERE id=$2))
		  )
		))`, userID, siteID).Scan(&ok)
	return ok, err
}

// RenameClient changes a client's name.
func (s *Store) RenameClient(ctx context.Context, id, name string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE clients SET name=$2 WHERE id=$1`, id, name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSite inserts a site under a client.
func (s *Store) CreateSite(ctx context.Context, clientID, name string) (*models.Site, error) {
	id := newID()
	if _, err := s.pool.Exec(ctx, `INSERT INTO sites (id, client_id, name) VALUES ($1,$2,$3)`, id, clientID, name); err != nil {
		return nil, err
	}
	return &models.Site{ID: id, ClientID: clientID, Name: name, CreatedAt: time.Now()}, nil
}

// RenameSite changes a site's name.
func (s *Store) RenameSite(ctx context.Context, id, name string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE sites SET name=$2 WHERE id=$1`, id, name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSites returns sites, optionally filtered by client.
func (s *Store) ListSites(ctx context.Context, clientID string) ([]models.Site, error) {
	var rows pgx.Rows
	var err error
	if clientID != "" {
		rows, err = s.pool.Query(ctx, `SELECT id, client_id, name, created_at FROM sites WHERE client_id=$1 ORDER BY name`, clientID)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT id, client_id, name, created_at FROM sites ORDER BY name`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Site
	for rows.Next() {
		var st models.Site
		if err := rows.Scan(&st.ID, &st.ClientID, &st.Name, &st.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// AgentScope returns an agent's site and client ids (either may be nil).
func (s *Store) AgentScope(ctx context.Context, agentID string) (siteID, clientID *string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT a.site_id, si.client_id FROM agents a
		 LEFT JOIN sites si ON si.id = a.site_id WHERE a.id=$1`, agentID).Scan(&siteID, &clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	return siteID, clientID, err
}

// ===================== Scripts =====================

const scriptCols = `id, name, description, category, shell, body, default_args, created_at, updated_at,
	params, ai_released_by, ai_released_at, ai_released_hash`

func scanScript(row pgx.Row) (*models.Script, error) {
	var sc models.Script
	var params []byte
	err := row.Scan(&sc.ID, &sc.Name, &sc.Description, &sc.Category, &sc.Shell, &sc.Body, &sc.DefaultArgs, &sc.CreatedAt, &sc.UpdatedAt,
		&params, &sc.AIReleasedBy, &sc.AIReleasedAt, &sc.AIReleasedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &sc.Params); err != nil {
			return nil, err
		}
	}
	if sc.Params == nil {
		sc.Params = []models.ScriptParam{}
	}
	sc.AIReleased = sc.AIReleaseCurrent()
	return &sc, nil
}

func scriptParamsJSON(params []models.ScriptParam) []byte {
	if params == nil {
		params = []models.ScriptParam{}
	}
	raw, _ := json.Marshal(params)
	return raw
}

// CreateScript adds a script to the library.
func (s *Store) CreateScript(ctx context.Context, sc *models.Script) (*models.Script, error) {
	sc.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO scripts (id, name, description, category, shell, body, default_args, params)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		sc.ID, sc.Name, sc.Description, sc.Category, sc.Shell, sc.Body, sc.DefaultArgs, scriptParamsJSON(sc.Params))
	if err != nil {
		return nil, err
	}
	return s.GetScript(ctx, sc.ID)
}

// GetScript fetches one script.
func (s *Store) GetScript(ctx context.Context, id string) (*models.Script, error) {
	return scanScript(s.pool.QueryRow(ctx, `SELECT `+scriptCols+` FROM scripts WHERE id=$1`, id))
}

// ListScriptCategories returns the categories in use, with how many scripts
// carry each.
//
// A category is not a row anywhere — it exists exactly as long as some script
// names it. That is why there is nothing to "create": a category comes into
// being when a script is saved under it, and renaming is an update across the
// scripts that share the name.
func (s *Store) ListScriptCategories(ctx context.Context) ([]models.ScriptCategory, error) {
	// The union matters: the registry holds categories nobody has filed a script
	// under yet, and a script written straight through the API can carry a name
	// nobody registered. Listing only one side would hide one of them.
	rows, err := s.pool.Query(ctx, `
		WITH names AS (
			SELECT name FROM script_categories
			UNION
			SELECT DISTINCT category FROM scripts WHERE category <> ''
		)
		SELECT n.name, (SELECT count(*) FROM scripts s WHERE s.category = n.name)
		  FROM names n ORDER BY n.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ScriptCategory{}
	for rows.Next() {
		var c models.ScriptCategory
		if err := rows.Scan(&c.Name, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// isUniqueViolation reports whether err is Postgres' 23505, which every unique
// index reports and which callers turn into ErrConflict.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateScriptCategory registers a category so it can exist before any script is
// filed under it. ErrConflict when the name is taken, compared without case
// because "Networking" and "networking" are the same category to everyone except
// a byte comparison.
func (s *Store) CreateScriptCategory(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO script_categories (id, name) VALUES ($1,$2)`, newID(), name)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// ScriptCategoryExists reports whether a category is registered or in use.
func (s *Store) ScriptCategoryExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	// Both arms compare without case. The registry's unique index already does,
	// and a script carrying "Networking" has to block registering "networking" —
	// otherwise the catalogue itself becomes the source of the duplicate.
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM script_categories WHERE lower(name)=lower($1)
		UNION ALL
		SELECT 1 FROM scripts WHERE lower(category)=lower($1))`, name).Scan(&exists)
	return exists, err
}

// RenameScriptCategory renames the registry entry and every script filed under
// it, reporting how many scripts moved. When `to` is already registered the two
// merge — the only way to undo a typo that has already split one category in
// half — so the old entry is dropped rather than colliding with the new one.
func (s *Store) RenameScriptCategory(ctx context.Context, from, to string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var taken bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM script_categories WHERE lower(name)=lower($1))`, to).Scan(&taken); err != nil {
		return 0, err
	}
	if taken {
		if _, err := tx.Exec(ctx, `DELETE FROM script_categories WHERE lower(name)=lower($1)`, from); err != nil {
			return 0, err
		}
	} else if _, err := tx.Exec(ctx,
		`UPDATE script_categories SET name=$2 WHERE lower(name)=lower($1)`, from, to); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE scripts SET category=$2, updated_at=now() WHERE category=$1`, from, to)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// DeleteScriptCategory removes the registry entry and returns every script filed
// under it to uncategorized, reporting how many. The scripts themselves survive:
// deleting a category is a filing decision, not a way to delete work.
func (s *Store) DeleteScriptCategory(ctx context.Context, name string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM script_categories WHERE lower(name)=lower($1)`, name); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE scripts SET category='', updated_at=now() WHERE category=$1`, name)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// RegisterScriptCategory adopts a name a script was just saved under, so a
// category named in the script editor shows up in the catalogue too. Best
// effort: the listing unions both sides, so a failure here loses nothing.
func (s *Store) RegisterScriptCategory(ctx context.Context, name string) {
	if name == "" {
		return
	}
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO script_categories (id, name) VALUES ($1,$2) ON CONFLICT DO NOTHING`, newID(), name)
}

// ListScripts returns the library.
func (s *Store) ListScripts(ctx context.Context) ([]models.Script, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+scriptCols+` FROM scripts ORDER BY category, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Script
	for rows.Next() {
		sc, err := scanScript(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

// UpdateScript edits a script. An edit to what runs — the shell, the body or
// the declared parameters — revokes a release for the AI in the same statement,
// so there is no moment at which the new content is released under the old
// one's approval. revoked reports whether a release was withdrawn.
func (s *Store) UpdateScript(ctx context.Context, sc *models.Script) (revoked bool, err error) {
	err = s.pool.QueryRow(ctx,
		`WITH old AS (SELECT shell, body, params, ai_released_hash FROM scripts WHERE id=$1 FOR UPDATE)
		 UPDATE scripts AS s SET name=$2, description=$3, category=$4, shell=$5, body=$6, default_args=$7, params=$8,
		   ai_released_by   = CASE WHEN changed.yes THEN NULL ELSE s.ai_released_by END,
		   ai_released_at   = CASE WHEN changed.yes THEN NULL ELSE s.ai_released_at END,
		   ai_released_hash = CASE WHEN changed.yes THEN '' ELSE s.ai_released_hash END,
		   updated_at=now()
		 FROM old, LATERAL (SELECT (old.shell <> $5 OR old.body <> $6 OR old.params <> $8::jsonb) AS yes) AS changed
		 WHERE s.id=$1
		 RETURNING (changed.yes AND old.ai_released_hash <> '')`,
		sc.ID, sc.Name, sc.Description, sc.Category, sc.Shell, sc.Body, sc.DefaultArgs, scriptParamsJSON(sc.Params)).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return revoked, err
}

// SetScriptAIRelease releases a script for the AI, bound to contentHash — the
// hash of the content the releasing admin reviewed, read at readAt. If the
// script was edited since, nothing is released and ErrConflict says so: the
// release would otherwise approve content nobody looked at.
func (s *Store) SetScriptAIRelease(ctx context.Context, id, releasedBy, contentHash string, readAt time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE scripts SET ai_released_by=$2, ai_released_at=now(), ai_released_hash=$3, ai_failure_streak=0
		 WHERE id=$1 AND updated_at=$4`, id, releasedBy, contentHash, readAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetScript(ctx, id); err != nil {
			return err
		}
		return ErrConflict
	}
	return nil
}

// ClearScriptAIRelease withdraws a release. It reports whether there was one.
func (s *Store) ClearScriptAIRelease(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE scripts SET ai_released_by=NULL, ai_released_at=NULL, ai_released_hash='', ai_failure_streak=0
		 WHERE id=$1 AND ai_released_hash <> ''`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RecordAIScriptOutcome counts consecutive failed autonomous runs of a
// released script. At limit failures in a row the release is withdrawn in the
// same statement, and revoked reports it: a script that keeps failing on device
// after device is doing something nobody released it to do.
func (s *Store) RecordAIScriptOutcome(ctx context.Context, id string, success bool, limit int) (revoked bool, err error) {
	// One UPDATE, deciding from the row as it was: two data-modifying
	// statements on the same row in one query would leave which one wins to
	// Postgres.
	err = s.pool.QueryRow(ctx,
		`WITH old AS (SELECT ai_failure_streak, ai_released_hash FROM scripts WHERE id=$1 FOR UPDATE),
		 trip AS (SELECT (NOT $2::boolean AND old.ai_failure_streak + 1 >= $3::int AND old.ai_released_hash <> '') AS yes FROM old)
		 UPDATE scripts AS s SET
		   ai_failure_streak = CASE WHEN $2::boolean OR trip.yes THEN 0 ELSE s.ai_failure_streak + 1 END,
		   ai_released_by    = CASE WHEN trip.yes THEN NULL ELSE s.ai_released_by END,
		   ai_released_at    = CASE WHEN trip.yes THEN NULL ELSE s.ai_released_at END,
		   ai_released_hash  = CASE WHEN trip.yes THEN '' ELSE s.ai_released_hash END
		 FROM trip WHERE s.id=$1
		 RETURNING trip.yes`, id, success, limit).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return revoked, err
}

// DeleteScript removes a script.
func (s *Store) DeleteScript(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM scripts WHERE id=$1`, id)
	return err
}

// ===================== Policies =====================

// CreatePolicy adds a policy.
func (s *Store) CreatePolicy(ctx context.Context, name, desc, proposedBy string) (*models.Policy, error) {
	id := newID()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO policies (id, name, description, proposed_by) VALUES ($1,$2,$3,$4)`,
		id, name, desc, proposedBy); err != nil {
		return nil, err
	}
	return &models.Policy{ID: id, Name: name, Description: desc, ProposedBy: proposedBy, CreatedAt: time.Now()}, nil
}

// ListPolicies returns all policies.
func (s *Store) ListPolicies(ctx context.Context) ([]models.Policy, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, proposed_by, approved_by, created_at FROM policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Policy
	for rows.Next() {
		var p models.Policy
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.ProposedBy, &p.ApprovedBy, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPoliciesForUser returns policies that apply to at least one agent in the
// user's access scope. Unscoped users see all policies.
func (s *Store) ListPoliciesForUser(ctx context.Context, userID string) ([]models.Policy, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT p.id, p.name, p.description, p.proposed_by, p.approved_by, p.created_at
		FROM policies p
		WHERE NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR EXISTS (
		  SELECT 1
		  FROM policy_assignments pa
		  JOIN agents a ON (
		    (pa.scope_type='agent'  AND pa.scope_id=a.id) OR
		    (pa.scope_type='group'  AND pa.scope_id=a.group_id) OR
		    (pa.scope_type='site'   AND pa.scope_id=a.site_id) OR
		    (pa.scope_type='client' AND pa.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		  )
		  WHERE pa.policy_id=p.id AND EXISTS (
		    SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		      (us.scope_type='agent'  AND us.scope_id=a.id) OR
		      (us.scope_type='group'  AND us.scope_id=a.group_id) OR
		      (us.scope_type='site'   AND us.scope_id=a.site_id) OR
		      (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		    )
		  )
		)
		ORDER BY p.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Policy
	for rows.Next() {
		var p models.Policy
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.ProposedBy, &p.ApprovedBy, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PolicyAllowedForUser reports whether a policy applies to at least one agent in
// the user's access scope. Users with no scope rows can see any policy.
func (s *Store) PolicyExists(ctx context.Context, policyID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM policies WHERE id=$1)`, policyID).Scan(&ok)
	return ok, err
}

func (s *Store) PolicyAllowedForUser(ctx context.Context, userID, policyID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM policies WHERE id=$2) AND (
		NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR EXISTS (
		  SELECT 1
		  FROM policy_assignments pa
		  JOIN agents a ON (
		    (pa.scope_type='agent'  AND pa.scope_id=a.id) OR
		    (pa.scope_type='group'  AND pa.scope_id=a.group_id) OR
		    (pa.scope_type='site'   AND pa.scope_id=a.site_id) OR
		    (pa.scope_type='client' AND pa.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		  )
		  WHERE pa.policy_id=$2 AND EXISTS (
		    SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		      (us.scope_type='agent'  AND us.scope_id=a.id) OR
		      (us.scope_type='group'  AND us.scope_id=a.group_id) OR
		      (us.scope_type='site'   AND us.scope_id=a.site_id) OR
		      (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		    )
		  )
		))`, userID, policyID).Scan(&ok)
	return ok, err
}

// PolicyManageAllowedForUser requires every assignment to be wholly controlled
// by the user. Unassigned global policies are not manageable by scoped users.
func (s *Store) PolicyManageAllowedForUser(ctx context.Context, userID, policyID string) (bool, error) {
	assignments, err := s.ListPolicyAssignments(ctx, policyID)
	if err != nil {
		return false, err
	}
	if len(assignments) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM policies WHERE id=$1)`, policyID).Scan(&exists); err != nil {
			return false, err
		}
		return false, nil
	}
	for _, a := range assignments {
		ok, err := s.ScopeAllowedForUser(ctx, userID, a.ScopeType, a.ScopeID)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// DeletePolicy removes a policy (cascades to its checks/tasks/assignments).
// UpdatePolicy renames a policy and rewrites its description. The checks, tasks
// and assignments hanging off it are untouched — this is the label, not the
// contents.
func (s *Store) UpdatePolicy(ctx context.Context, id, name, desc string) (*models.Policy, error) {
	var p models.Policy
	err := s.pool.QueryRow(ctx,
		`UPDATE policies SET name=$2, description=$3 WHERE id=$1
		 RETURNING id, name, description, proposed_by, approved_by, created_at`,
		id, name, desc).Scan(&p.ID, &p.Name, &p.Description, &p.ProposedBy, &p.ApprovedBy, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) DeletePolicy(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM policies WHERE id=$1`, id)
	return err
}

// AssignPolicy binds a policy to a scope.
func (s *Store) AssignPolicy(ctx context.Context, policyID, scopeType, scopeID string) (*models.PolicyAssignment, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO policy_assignments (id, policy_id, scope_type, scope_id) VALUES ($1,$2,$3,$4)`,
		id, policyID, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	return &models.PolicyAssignment{ID: id, PolicyID: policyID, ScopeType: scopeType, ScopeID: scopeID, CreatedAt: time.Now()}, nil
}

// ListPolicyAssignments returns assignments for a policy.
func (s *Store) ListPolicyAssignments(ctx context.Context, policyID string) ([]models.PolicyAssignment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, policy_id, scope_type, scope_id, created_at FROM policy_assignments WHERE policy_id=$1`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PolicyAssignment
	for rows.Next() {
		var pa models.PolicyAssignment
		if err := rows.Scan(&pa.ID, &pa.PolicyID, &pa.ScopeType, &pa.ScopeID, &pa.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, pa)
	}
	return out, rows.Err()
}

// GetPolicyAssignment fetches one assignment for authorization before deletion.
func (s *Store) GetPolicyAssignment(ctx context.Context, id string) (*models.PolicyAssignment, error) {
	var a models.PolicyAssignment
	err := s.pool.QueryRow(ctx,
		`SELECT id, policy_id, scope_type, scope_id, created_at FROM policy_assignments WHERE id=$1`, id).
		Scan(&a.ID, &a.PolicyID, &a.ScopeType, &a.ScopeID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListPolicyAssignmentsForUser returns only assignments that intersect the
// user's access scope. Unscoped users see all assignments for the policy.
func (s *Store) ListPolicyAssignmentsForUser(ctx context.Context, userID, policyID string) ([]models.PolicyAssignment, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT pa.id, pa.policy_id, pa.scope_type, pa.scope_id, pa.created_at
		FROM policy_assignments pa
		WHERE pa.policy_id=$2 AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (
		    SELECT 1
		    FROM agents a
		    WHERE (
		      (pa.scope_type='agent'  AND pa.scope_id=a.id) OR
		      (pa.scope_type='group'  AND pa.scope_id=a.group_id) OR
		      (pa.scope_type='site'   AND pa.scope_id=a.site_id) OR
		      (pa.scope_type='client' AND pa.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		    )
		    AND EXISTS (
		      SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		        (us.scope_type='agent'  AND us.scope_id=a.id) OR
		        (us.scope_type='group'  AND us.scope_id=a.group_id) OR
		        (us.scope_type='site'   AND us.scope_id=a.site_id) OR
		        (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		      )
		    )
		  )
		)
		ORDER BY pa.created_at`, userID, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PolicyAssignment
	for rows.Next() {
		var pa models.PolicyAssignment
		if err := rows.Scan(&pa.ID, &pa.PolicyID, &pa.ScopeType, &pa.ScopeID, &pa.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, pa)
	}
	return out, rows.Err()
}

// DeleteAssignment removes a policy assignment.
func (s *Store) DeleteAssignment(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM policy_assignments WHERE id=$1`, id)
	return err
}

// policyFilter is the SQL fragment selecting policies that apply to an agent,
// given $1=agentID, $2=siteID, $3=clientID.
const policyFilter = `policy_id IN (
	SELECT pa.policy_id FROM policy_assignments pa
	WHERE (pa.scope_type='agent'  AND pa.scope_id=$1)
	   OR (pa.scope_type='group'  AND pa.scope_id=(SELECT group_id FROM agents WHERE id=$1))
	   OR (pa.scope_type='site'   AND pa.scope_id=$2)
	   OR (pa.scope_type='client' AND pa.scope_id=$3)
)`

// ===================== Checks =====================

const checkCols = `id, agent_id, policy_id, type, name, config, warn_threshold, err_threshold,
	interval_sec, failure_count, recovery_count, hysteresis, cooldown_sec, timeout_sec, enabled, proposed_by, approved_by, created_at`

func scanCheck(row pgx.Row) (*models.Check, error) {
	var c models.Check
	err := row.Scan(&c.ID, &c.AgentID, &c.PolicyID, &c.Type, &c.Name, &c.Config,
		&c.WarnThreshold, &c.ErrThreshold, &c.IntervalSec, &c.FailureCount,
		&c.RecoveryCount, &c.Hysteresis, &c.CooldownSec, &c.TimeoutSec,
		&c.Enabled, &c.ProposedBy, &c.ApprovedBy, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateCheck adds a check.
func (s *Store) CreateCheck(ctx context.Context, c *models.Check) (*models.Check, error) {
	c.ID = newID()
	if len(c.Config) == 0 {
		c.Config = []byte("{}")
	}
	if c.IntervalSec <= 0 {
		c.IntervalSec = 300
	}
	if c.FailureCount <= 0 {
		c.FailureCount = 2
	}
	if c.RecoveryCount <= 0 {
		c.RecoveryCount = 2
	}
	if c.Hysteresis < 0 {
		c.Hysteresis = 0
	}
	if c.CooldownSec < 0 {
		c.CooldownSec = 0
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 180
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO checks (id, agent_id, policy_id, type, name, config, warn_threshold, err_threshold,
		                      interval_sec, failure_count, recovery_count, hysteresis, cooldown_sec, timeout_sec, enabled, proposed_by, approved_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		c.ID, c.AgentID, c.PolicyID, c.Type, c.Name, []byte(c.Config), c.WarnThreshold,
		c.ErrThreshold, c.IntervalSec, c.FailureCount, c.RecoveryCount, c.Hysteresis,
		c.CooldownSec, c.TimeoutSec, c.Enabled, c.ProposedBy, c.ApprovedBy)
	if err != nil {
		return nil, err
	}
	return s.GetCheck(ctx, c.ID)
}

// GetCheck fetches one check.
func (s *Store) GetCheck(ctx context.Context, id string) (*models.Check, error) {
	return scanCheck(s.pool.QueryRow(ctx, `SELECT `+checkCols+` FROM checks WHERE id=$1`, id))
}

func scanChecks(rows pgx.Rows) ([]models.Check, error) {
	defer rows.Close()
	var out []models.Check
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListChecks returns checks filtered by agent or policy (empty = all).
func (s *Store) ListChecks(ctx context.Context, agentID, policyID string) ([]models.Check, error) {
	switch {
	case agentID != "":
		rows, err := s.pool.Query(ctx, `SELECT `+checkCols+` FROM checks WHERE agent_id=$1 ORDER BY created_at`, agentID)
		if err != nil {
			return nil, err
		}
		return scanChecks(rows)
	case policyID != "":
		rows, err := s.pool.Query(ctx, `SELECT `+checkCols+` FROM checks WHERE policy_id=$1 ORDER BY created_at`, policyID)
		if err != nil {
			return nil, err
		}
		return scanChecks(rows)
	default:
		rows, err := s.pool.Query(ctx, `SELECT `+checkCols+` FROM checks ORDER BY created_at`)
		if err != nil {
			return nil, err
		}
		return scanChecks(rows)
	}
}

// ChecksForAgent resolves the effective checks for an agent: those assigned
// directly plus those from any policy in scope.
func (s *Store) ChecksForAgent(ctx context.Context, agentID string, siteID, clientID *string) ([]models.Check, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+checkCols+` FROM checks WHERE enabled AND (agent_id=$1 OR `+policyFilter+`)`,
		agentID, siteID, clientID)
	if err != nil {
		return nil, err
	}
	return scanChecks(rows)
}

// UpdateCheck edits a check.
func (s *Store) UpdateCheck(ctx context.Context, c *models.Check) error {
	if len(c.Config) == 0 {
		c.Config = []byte("{}")
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE checks SET type=$2, name=$3, config=$4, warn_threshold=$5, err_threshold=$6,
		                   interval_sec=$7, enabled=$8, failure_count=$9, recovery_count=$10,
		                   hysteresis=$11, cooldown_sec=$12, timeout_sec=$13, approved_by=$14
		 WHERE id=$1`,
		c.ID, c.Type, c.Name, []byte(c.Config), c.WarnThreshold, c.ErrThreshold,
		c.IntervalSec, c.Enabled, c.FailureCount, c.RecoveryCount, c.Hysteresis,
		c.CooldownSec, c.TimeoutSec, c.ApprovedBy)
	return err
}

// DeleteCheck removes a check.
func (s *Store) DeleteCheck(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM checks WHERE id=$1`, id)
	return err
}

// AddCheckResult records one execution and returns it.
func (s *Store) AddCheckResult(ctx context.Context, r *models.CheckResult) error {
	r.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO check_results (id, check_id, agent_id, execution_id, status, value, output)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		r.ID, r.CheckID, r.AgentID, r.ExecutionID, r.Status, r.Value, r.Output)
	return err
}

// LastCheckResultTime returns when a check last ran on an agent (zero if never).
func (s *Store) LastCheckResultTime(ctx context.Context, checkID, agentID string) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT max(ran_at) FROM check_results WHERE check_id=$1 AND agent_id=$2`, checkID, agentID).Scan(&t)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// ListCheckResults returns recent results for a check. If agentID is empty, it
// returns results across all agents (used for the latest-status pill).
func (s *Store) ListCheckResults(ctx context.Context, checkID, agentID string, limit int) ([]models.CheckResult, error) {
	return s.ListCheckResultsForUser(ctx, checkID, agentID, "", false, limit)
}

// ListCheckResultsForUser applies access scope before ORDER BY/LIMIT. Filtering
// an already-limited fleet result in the API could return an empty page even
// when the restricted user's devices had newer history just beyond that limit.
func (s *Store) ListCheckResultsForUser(ctx context.Context, checkID, agentID, userID string, scoped bool, limit int) ([]models.CheckResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT cr.id, cr.check_id, cr.agent_id, cr.execution_id,
		       cr.status, cr.value, cr.output, cr.ran_at
		FROM check_results cr
		JOIN agents a ON a.id=cr.agent_id
		LEFT JOIN sites si ON si.id=a.site_id
		WHERE cr.check_id=$1
		  AND (NULLIF($2,'')::uuid IS NULL OR cr.agent_id=NULLIF($2,'')::uuid)
		  AND (NOT $4 OR EXISTS (
			SELECT 1 FROM user_scopes us WHERE us.user_id=NULLIF($3,'')::uuid AND (
				(us.scope_type='agent' AND us.scope_id=a.id) OR
				(us.scope_type='group' AND us.scope_id=a.group_id) OR
				(us.scope_type='site' AND us.scope_id=a.site_id) OR
				(us.scope_type='client' AND us.scope_id=si.client_id)
			)
		  ))
		ORDER BY cr.ran_at DESC LIMIT $5`, checkID, agentID, userID, scoped, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CheckResult
	for rows.Next() {
		var r models.CheckResult
		if err := rows.Scan(&r.ID, &r.CheckID, &r.AgentID, &r.ExecutionID,
			&r.Status, &r.Value, &r.Output, &r.RanAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ===================== Tasks =====================

const taskCols = `id, agent_id, policy_id, name, script_id, shell, script, schedule_kind, interval_sec, run_at_minute, days_of_week, run_at_minutes, enabled, proposed_by, approved_by, created_at`

// int32s / ints bridge the INTEGER[] column (pgx wants []int32) and the model's
// []int field, so callers never deal with the width mismatch.
func int32s(in []int) []int32 {
	out := make([]int32, len(in))
	for i, v := range in {
		out[i] = int32(v)
	}
	return out
}

func ints(in []int32) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}

func scanTask(row pgx.Row) (*models.Task, error) {
	var t models.Task
	var mins []int32
	err := row.Scan(&t.ID, &t.AgentID, &t.PolicyID, &t.Name, &t.ScriptID, &t.Shell, &t.Script,
		&t.ScheduleKind, &t.IntervalSec, &t.RunAtMinute, &t.DaysOfWeek, &mins, &t.Enabled, &t.ProposedBy, &t.ApprovedBy, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.RunAtMinutes = ints(mins)
	return &t, nil
}

// CreateTask adds a scheduled task.
func (s *Store) CreateTask(ctx context.Context, t *models.Task) (*models.Task, error) {
	t.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tasks (id, agent_id, policy_id, name, script_id, shell, script, schedule_kind, interval_sec, run_at_minute, days_of_week, run_at_minutes, enabled, proposed_by, approved_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		t.ID, t.AgentID, t.PolicyID, t.Name, t.ScriptID, t.Shell, t.Script, t.ScheduleKind, t.IntervalSec, t.RunAtMinute, t.DaysOfWeek, int32s(t.RunAtMinutes), t.Enabled, t.ProposedBy, t.ApprovedBy)
	if err != nil {
		return nil, err
	}
	return s.GetTask(ctx, t.ID)
}

// GetTask fetches one task.
func (s *Store) GetTask(ctx context.Context, id string) (*models.Task, error) {
	return scanTask(s.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=$1`, id))
}

func scanTasks(rows pgx.Rows) ([]models.Task, error) {
	defer rows.Close()
	var out []models.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListTasks returns tasks filtered by agent or policy (empty = all).
func (s *Store) ListTasks(ctx context.Context, agentID, policyID string) ([]models.Task, error) {
	switch {
	case agentID != "":
		rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE agent_id=$1 ORDER BY created_at`, agentID)
		if err != nil {
			return nil, err
		}
		return scanTasks(rows)
	case policyID != "":
		rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE policy_id=$1 ORDER BY created_at`, policyID)
		if err != nil {
			return nil, err
		}
		return scanTasks(rows)
	default:
		rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY created_at`)
		if err != nil {
			return nil, err
		}
		return scanTasks(rows)
	}
}

// TasksForAgent resolves the effective tasks for an agent (direct + via policy).
func (s *Store) TasksForAgent(ctx context.Context, agentID string, siteID, clientID *string) ([]models.Task, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+taskCols+` FROM tasks WHERE enabled AND (agent_id=$1 OR `+policyFilter+`)`,
		agentID, siteID, clientID)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

// UpdateTask edits a task.
func (s *Store) UpdateTask(ctx context.Context, t *models.Task) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tasks SET name=$2, script_id=$3, shell=$4, script=$5, schedule_kind=$6, interval_sec=$7, run_at_minute=$8, days_of_week=$9, run_at_minutes=$10, enabled=$11, approved_by=$12 WHERE id=$1`,
		t.ID, t.Name, t.ScriptID, t.Shell, t.Script, t.ScheduleKind, t.IntervalSec, t.RunAtMinute, t.DaysOfWeek, int32s(t.RunAtMinutes), t.Enabled, t.ApprovedBy)
	return err
}

// DeleteTask removes a task.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, id)
	return err
}

// AgentIDsForPolicy resolves every agent a policy applies to, expanding its
// assignments the same way policyFilter does (agent / group / site / client scope). Used
// by "run now" to fan a policy task out to its current targets.
func (s *Store) AgentIDsForPolicy(ctx context.Context, policyID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id FROM agents a
		 LEFT JOIN sites si ON si.id = a.site_id
		 WHERE a.revoked_at IS NULL AND EXISTS (
		   SELECT 1 FROM policy_assignments pa
		   WHERE pa.policy_id=$1 AND (
		        (pa.scope_type='agent'  AND pa.scope_id = a.id)
		     OR (pa.scope_type='group'  AND pa.scope_id = a.group_id)
		     OR (pa.scope_type='site'   AND pa.scope_id = a.site_id)
		     OR (pa.scope_type='client' AND pa.scope_id = si.client_id)))`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LastTaskRunTime returns when a task last produced a job on an agent.
func (s *Store) LastTaskRunTime(ctx context.Context, taskID, agentID string) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT max(created_at) FROM jobs WHERE task_id=$1 AND agent_id=$2`, taskID, agentID).Scan(&t)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// ===================== Alerts =====================

// RaiseAlert opens an alert if one is not already open for this source, and
// reports whether this call is the one that opened it.
//
// The boolean is what makes notifications sane: the ON CONFLICT means a check
// failing every 30s inserts exactly once, so "inserted" marks the healthy ->
// failing transition. Notifying on the row count instead of on every evaluation
// is the difference between one email per incident and one every half minute.
func (s *Store) RaiseAlert(ctx context.Context, agentID *string, sourceType string, sourceID *string, severity, message string) (opened bool, alertID string, err error) {
	transition, err := s.ObserveAlert(ctx, AlertObservation{
		AgentID: agentID, SourceType: sourceType, SourceID: sourceID,
		ConditionKey: "default", Severity: severity, Message: message,
	})
	if err != nil {
		return false, "", err
	}
	return transition.Opened, transition.Alert.ID, nil
}

// ResolveAlertsForSource closes any open alert for a source and reports how many
// it closed. A non-zero count is the failing -> healthy transition, which is what
// a recovery email should be tied to — on-call cares more about "it is back" than
// about the original failure.
func (s *Store) ResolveAlertsForSource(ctx context.Context, sourceType string, sourceID *string, agentID *string) (int64, error) {
	resolved, err := s.ResolveAlertSource(ctx, sourceType, sourceID, agentID,
		"system", "automatic", "Condition recovered.", time.Now())
	return int64(len(resolved)), err
}

// ListAlerts returns alerts, optionally filtered by status.
func (s *Store) ListAlerts(ctx context.Context, status string, limit int) ([]models.Alert, error) {
	return s.ListAlertsPage(ctx, AlertListOptions{Status: status, Limit: limit})
}

// SetAlertStatus updates an alert's status by id (e.g. ack/resolve).
// AlertAgent returns the agent an alert belongs to (nil for non-device alerts).
func (s *Store) AlertAgent(ctx context.Context, id string) (*string, error) {
	a, err := s.GetAlert(ctx, id)
	if err != nil {
		return nil, err
	}
	return a.AgentSnapshotID, nil
}

func (s *Store) SetAlertStatus(ctx context.Context, id, status string) error {
	action := ""
	switch status {
	case models.AlertAcked:
		action = "ack"
	case models.AlertResolved:
		action = "resolve"
	default:
		return ErrConflict
	}
	_, err := s.TransitionAlert(ctx, id, AlertAction{Action: action, Actor: "system"})
	return err
}

// ListOnlineAgentIDs returns ids of all agents (used by the scheduler).
func (s *Store) ListAllAgentIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
