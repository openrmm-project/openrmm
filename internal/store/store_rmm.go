package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

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

// CountUsers reports how many users exist (used to bootstrap the first admin).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(1) FROM users`).Scan(&n)
	return n, err
}

// GetUserAuth returns the id, role and password hash for a username.
func (s *Store) GetUserAuth(ctx context.Context, username string) (id, role, passwordHash string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT id, role, password_hash FROM users WHERE username=$1`, username).
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

// SetUserTOTP stores the (encrypted) secret and enabled flag; pass "" to clear.
func (s *Store) SetUserTOTP(ctx context.Context, id, secret string, enabled bool) error {
	var sec *string
	if secret != "" {
		sec = &secret
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET totp_secret=$2, totp_enabled=$3 WHERE id=$1`, id, sec, enabled)
	return err
}

// GetUserByID returns a user by id (no password hash).
func (s *Store) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx, `SELECT id, username, role, created_at, totp_enabled FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &u.TOTPEnabled)
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
	rows, err := s.pool.Query(ctx, `SELECT id, username, role, created_at, totp_enabled FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &u.TOTPEnabled); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
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
		 WHERE se.token_hash=$1 AND se.expires_at > now()`, tokenHash).Scan(&userID, &role)
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
	err = s.pool.QueryRow(ctx, `SELECT role, COALESCE(user_id::text, '') FROM api_tokens WHERE token_hash=$1`, hash).Scan(&role, &userID)
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
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_recovery_codes SET used_at=now()
		 WHERE id = (SELECT id FROM user_recovery_codes
		             WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL LIMIT 1)`,
		userID, codeHash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
// client/site/agent scopes.
func (s *Store) AgentAllowedForUser(ctx context.Context, userID, agentID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT (NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1))
		    OR EXISTS (
		      SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		        (us.scope_type='agent'  AND us.scope_id=$2) OR
		        (us.scope_type='site'   AND us.scope_id=(SELECT site_id FROM agents WHERE id=$2)) OR
		        (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM agents a JOIN sites s ON s.id=a.site_id WHERE a.id=$2))
		      ))`, userID, agentID).Scan(&ok)
	return ok, err
}

// ListAgentsForUser lists the agents a user may see (all when unscoped).
func (s *Store) ListAgentsForUser(ctx context.Context, userID string) ([]models.Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentCols+` FROM agents a WHERE
		NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR EXISTS (
		  SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='agent'  AND us.scope_id=a.id) OR
		    (us.scope_type='site'   AND us.scope_id=a.site_id) OR
		    (us.scope_type='client' AND us.scope_id=(SELECT s.client_id FROM sites s WHERE s.id=a.site_id))
		  ))
		ORDER BY a.created_at DESC`, userID)
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
// unscoped) — those directly scoped, or parents of the user's site/agent scopes.
func (s *Store) ListClientsForUser(ctx context.Context, userID string) ([]models.Client, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at FROM clients c WHERE
		NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR c.id IN (
		  SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='client'
		  UNION SELECT s.client_id FROM user_scopes us JOIN sites s ON s.id=us.scope_id WHERE us.user_id=$1 AND us.scope_type='site'
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
// directly scoped sites, sites under a scoped client, or sites of a scoped agent.
func (s *Store) ListSitesForUser(ctx context.Context, userID, clientID string) ([]models.Site, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, client_id, name, created_at FROM sites si WHERE
		($2 = '' OR si.client_id = $2) AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR si.id IN (SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='site')
		  OR si.client_id IN (SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='client')
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

// DeleteClient removes a client (cascades to sites).
func (s *Store) DeleteClient(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, id)
	return err
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

// DeleteSite removes a site.
func (s *Store) DeleteSite(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sites WHERE id=$1`, id)
	return err
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

const scriptCols = `id, name, description, category, shell, body, default_args, created_at, updated_at`

func scanScript(row pgx.Row) (*models.Script, error) {
	var sc models.Script
	err := row.Scan(&sc.ID, &sc.Name, &sc.Description, &sc.Category, &sc.Shell, &sc.Body, &sc.DefaultArgs, &sc.CreatedAt, &sc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

// CreateScript adds a script to the library.
func (s *Store) CreateScript(ctx context.Context, sc *models.Script) (*models.Script, error) {
	sc.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO scripts (id, name, description, category, shell, body, default_args)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		sc.ID, sc.Name, sc.Description, sc.Category, sc.Shell, sc.Body, sc.DefaultArgs)
	if err != nil {
		return nil, err
	}
	return s.GetScript(ctx, sc.ID)
}

// GetScript fetches one script.
func (s *Store) GetScript(ctx context.Context, id string) (*models.Script, error) {
	return scanScript(s.pool.QueryRow(ctx, `SELECT `+scriptCols+` FROM scripts WHERE id=$1`, id))
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

// UpdateScript edits a script.
func (s *Store) UpdateScript(ctx context.Context, sc *models.Script) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE scripts SET name=$2, description=$3, category=$4, shell=$5, body=$6, default_args=$7, updated_at=now() WHERE id=$1`,
		sc.ID, sc.Name, sc.Description, sc.Category, sc.Shell, sc.Body, sc.DefaultArgs)
	return err
}

// DeleteScript removes a script.
func (s *Store) DeleteScript(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM scripts WHERE id=$1`, id)
	return err
}

// ===================== Policies =====================

// CreatePolicy adds a policy.
func (s *Store) CreatePolicy(ctx context.Context, name, desc string) (*models.Policy, error) {
	id := newID()
	if _, err := s.pool.Exec(ctx, `INSERT INTO policies (id, name, description) VALUES ($1,$2,$3)`, id, name, desc); err != nil {
		return nil, err
	}
	return &models.Policy{ID: id, Name: name, Description: desc, CreatedAt: time.Now()}, nil
}

// ListPolicies returns all policies.
func (s *Store) ListPolicies(ctx context.Context) ([]models.Policy, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, created_at FROM policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Policy
	for rows.Next() {
		var p models.Policy
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePolicy removes a policy (cascades to its checks/tasks/assignments).
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
	   OR (pa.scope_type='site'   AND pa.scope_id=$2)
	   OR (pa.scope_type='client' AND pa.scope_id=$3)
)`

// ===================== Checks =====================

const checkCols = `id, agent_id, policy_id, type, name, config, warn_threshold, err_threshold, interval_sec, enabled, created_at`

func scanCheck(row pgx.Row) (*models.Check, error) {
	var c models.Check
	err := row.Scan(&c.ID, &c.AgentID, &c.PolicyID, &c.Type, &c.Name, &c.Config,
		&c.WarnThreshold, &c.ErrThreshold, &c.IntervalSec, &c.Enabled, &c.CreatedAt)
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
	_, err := s.pool.Exec(ctx,
		`INSERT INTO checks (id, agent_id, policy_id, type, name, config, warn_threshold, err_threshold, interval_sec, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		c.ID, c.AgentID, c.PolicyID, c.Type, c.Name, []byte(c.Config), c.WarnThreshold, c.ErrThreshold, c.IntervalSec, c.Enabled)
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
		`UPDATE checks SET type=$2, name=$3, config=$4, warn_threshold=$5, err_threshold=$6, interval_sec=$7, enabled=$8 WHERE id=$1`,
		c.ID, c.Type, c.Name, []byte(c.Config), c.WarnThreshold, c.ErrThreshold, c.IntervalSec, c.Enabled)
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
		`INSERT INTO check_results (id, check_id, agent_id, status, value, output) VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, r.CheckID, r.AgentID, r.Status, r.Value, r.Output)
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
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if agentID != "" {
		rows, err = s.pool.Query(ctx,
			`SELECT id, check_id, agent_id, status, value, output, ran_at FROM check_results
			 WHERE check_id=$1 AND agent_id=$2 ORDER BY ran_at DESC LIMIT $3`, checkID, agentID, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, check_id, agent_id, status, value, output, ran_at FROM check_results
			 WHERE check_id=$1 ORDER BY ran_at DESC LIMIT $2`, checkID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CheckResult
	for rows.Next() {
		var r models.CheckResult
		if err := rows.Scan(&r.ID, &r.CheckID, &r.AgentID, &r.Status, &r.Value, &r.Output, &r.RanAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ===================== Tasks =====================

const taskCols = `id, agent_id, policy_id, name, script_id, shell, script, schedule_kind, interval_sec, run_at_minute, enabled, created_at`

func scanTask(row pgx.Row) (*models.Task, error) {
	var t models.Task
	err := row.Scan(&t.ID, &t.AgentID, &t.PolicyID, &t.Name, &t.ScriptID, &t.Shell, &t.Script,
		&t.ScheduleKind, &t.IntervalSec, &t.RunAtMinute, &t.Enabled, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTask adds a scheduled task.
func (s *Store) CreateTask(ctx context.Context, t *models.Task) (*models.Task, error) {
	t.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tasks (id, agent_id, policy_id, name, script_id, shell, script, schedule_kind, interval_sec, run_at_minute, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		t.ID, t.AgentID, t.PolicyID, t.Name, t.ScriptID, t.Shell, t.Script, t.ScheduleKind, t.IntervalSec, t.RunAtMinute, t.Enabled)
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
		`UPDATE tasks SET name=$2, script_id=$3, shell=$4, script=$5, schedule_kind=$6, interval_sec=$7, run_at_minute=$8, enabled=$9 WHERE id=$1`,
		t.ID, t.Name, t.ScriptID, t.Shell, t.Script, t.ScheduleKind, t.IntervalSec, t.RunAtMinute, t.Enabled)
	return err
}

// DeleteTask removes a task.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, id)
	return err
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

// RaiseAlert opens an alert if one is not already open for this source.
func (s *Store) RaiseAlert(ctx context.Context, agentID *string, sourceType string, sourceID *string, severity, message string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO alerts (id, agent_id, source_type, source_id, severity, message)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (source_type, source_id, agent_id) WHERE status <> 'resolved' DO NOTHING`,
		newID(), agentID, sourceType, sourceID, severity, message)
	return err
}

// ResolveAlertsForSource closes any open alert for a source.
func (s *Store) ResolveAlertsForSource(ctx context.Context, sourceType string, sourceID *string, agentID *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE alerts SET status='resolved', resolved_at=now()
		 WHERE status<>'resolved' AND source_type=$1
		   AND source_id IS NOT DISTINCT FROM $2 AND agent_id IS NOT DISTINCT FROM $3`,
		sourceType, sourceID, agentID)
	return err
}

// ListAlerts returns alerts, optionally filtered by status.
func (s *Store) ListAlerts(ctx context.Context, status string, limit int) ([]models.Alert, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var rows pgx.Rows
	var err error
	if status != "" {
		rows, err = s.pool.Query(ctx,
			`SELECT id, agent_id, source_type, source_id, severity, message, status, created_at, resolved_at
			 FROM alerts WHERE status=$1 ORDER BY created_at DESC LIMIT $2`, status, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, agent_id, source_type, source_id, severity, message, status, created_at, resolved_at
			 FROM alerts ORDER BY created_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Alert
	for rows.Next() {
		var a models.Alert
		if err := rows.Scan(&a.ID, &a.AgentID, &a.SourceType, &a.SourceID, &a.Severity, &a.Message, &a.Status, &a.CreatedAt, &a.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAlertStatus updates an alert's status by id (e.g. ack/resolve).
// AlertAgent returns the agent an alert belongs to (nil for non-device alerts).
func (s *Store) AlertAgent(ctx context.Context, id string) (*string, error) {
	var agentID *string
	err := s.pool.QueryRow(ctx, `SELECT agent_id FROM alerts WHERE id=$1`, id).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return agentID, err
}

func (s *Store) SetAlertStatus(ctx context.Context, id, status string) error {
	var resolved interface{}
	if status == models.AlertResolved {
		resolved = time.Now()
	}
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET status=$2, resolved_at=$3 WHERE id=$1`, id, status, resolved)
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
