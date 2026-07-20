// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
)

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when an operation cannot safely replace existing
	// state, such as a second credential rotation while one is pending.
	ErrConflict = errors.New("conflict")
)

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool.
func New(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping verifies database connectivity. Backs the readiness probe.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// PoolStat exposes connection-pool counters for the metrics endpoint.
func (s *Store) PoolStat() *pgxpool.Stat { return s.pool.Stat() }

func newID() string { return uuid.NewString() }

// ---------- Agents ----------

// CreateAgent inserts a freshly enrolled agent and returns its row.
func (s *Store) CreateAgent(ctx context.Context, hostname, os, arch, version, tokenHash string, siteID *string) (*models.Agent, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO agents (id, hostname, os, arch, version, token_hash, site_id, status, last_seen)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,'online',now())`,
		id, hostname, os, arch, version, tokenHash, siteID)
	if err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, id)
}

// SetAgentSite assigns an agent to a site (or clears it with nil).
func (s *Store) SetAgentSite(ctx context.Context, id string, siteID *string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE agents AS a SET site_id=$2,
		group_id=CASE
		  WHEN $2::uuid IS NULL THEN NULL
		  WHEN EXISTS (SELECT 1 FROM device_groups g WHERE g.id=a.group_id AND g.site_id=$2) THEN a.group_id
		  ELSE NULL
		END
		WHERE a.id=$1`, id, siteID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanAgent(row pgx.Row) (*models.Agent, error) {
	var a models.Agent
	err := row.Scan(&a.ID, &a.Hostname, &a.Description, &a.Note, &a.OS, &a.Arch, &a.Version, &a.Status,
		&a.SiteID, &a.GroupID, &a.Tags, &a.MeshID, &a.LastSeen, &a.RevokedAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

const agentCols = `id, hostname, description, note, os, arch, version, status, site_id, group_id, tags, mesh_id, last_seen, revoked_at, created_at`

// SetAgentMeta updates the operator-editable identification fields: description
// (shown in the device list) and note (a private observation, detail-only).
func (s *Store) SetAgentMeta(ctx context.Context, id, description, note string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET description=$2, note=$3 WHERE id=$1`, id, description, note)
	return err
}

// GetAgent fetches one agent by id.
func (s *Store) GetAgent(ctx context.Context, id string) (*models.Agent, error) {
	return scanAgent(s.pool.QueryRow(ctx, `SELECT `+agentCols+` FROM agents WHERE id=$1`, id))
}

// GetAgentTokenHash returns the stored token hash for agent auth.
func (s *Store) GetAgentTokenHash(ctx context.Context, id string) (string, error) {
	var h string
	err := s.pool.QueryRow(ctx, `SELECT token_hash FROM agents WHERE id=$1 AND revoked_at IS NULL`, id).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

// AuthenticateAgentToken accepts the current credential or a pending replacement.
// A reconnect with the replacement atomically promotes it unless cancellation has
// already been requested. The current token remains valid until that point, but it
// never clears a staged replacement: a stale holder of the old credential must
// not be able to roll back rotation.
func (s *Store) AuthenticateAgentToken(ctx context.Context, id, hash string) (bool, error) {
	var matched bool
	err := s.pool.QueryRow(ctx, `UPDATE agents SET
		token_hash=CASE WHEN pending_token_hash=$2 AND NOT token_rotation_canceling THEN pending_token_hash ELSE token_hash END,
		pending_token_hash=CASE WHEN pending_token_hash=$2 AND NOT token_rotation_canceling THEN NULL ELSE pending_token_hash END,
		token_rotation_id=CASE WHEN pending_token_hash=$2 AND NOT token_rotation_canceling THEN NULL ELSE token_rotation_id END,
		pending_token_expires_at=CASE WHEN pending_token_hash=$2 AND NOT token_rotation_canceling THEN NULL ELSE pending_token_expires_at END,
		token_rotation_canceling=CASE WHEN pending_token_hash=$2 AND NOT token_rotation_canceling THEN FALSE ELSE token_rotation_canceling END
		WHERE id=$1 AND revoked_at IS NULL AND (token_hash=$2 OR pending_token_hash=$2)
		RETURNING true`, id, hash).Scan(&matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return matched, err
}

// BeginAgentCredentialRotation stages one replacement credential. The pending
// token cannot expire after delivery: an offline agent may have durably written it
// and must always be able to reconnect and prove possession of it.
func (s *Store) BeginAgentCredentialRotation(ctx context.Context, id, rotationID, tokenHash string) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE agents SET pending_token_hash=$3, token_rotation_id=$2, pending_token_expires_at=NULL,
		token_rotation_canceling=FALSE
		WHERE id=$1 AND revoked_at IS NULL AND pending_token_hash IS NULL`, id, rotationID, tokenHash)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		if _, err := s.GetAgentTokenHash(ctx, id); err != nil {
			return err
		}
		return ErrConflict
	}
	return nil
}

// CancelAgentCredentialRotation discards a staged credential that was never
// delivered. Agent-confirmed cancellation must use ConfirmAgentCredentialRotationCancellation.
func (s *Store) CancelAgentCredentialRotation(ctx context.Context, id, rotationID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET pending_token_hash=NULL, token_rotation_id=NULL, pending_token_expires_at=NULL,
		token_rotation_canceling=FALSE
		WHERE id=$1 AND token_rotation_id=$2 AND pending_token_hash IS NOT NULL`, id, rotationID)
	return err
}

// RequestAgentCredentialRotationCancellation fences promotion before a
// cancellation frame is sent. Repeating the request is safe and returns the
// same rotation id so a reconnect can receive the request again.
func (s *Store) RequestAgentCredentialRotationCancellation(ctx context.Context, id string) (string, error) {
	var rotationID string
	err := s.pool.QueryRow(ctx, `UPDATE agents SET token_rotation_canceling=TRUE
		WHERE id=$1 AND revoked_at IS NULL AND pending_token_hash IS NOT NULL
		RETURNING token_rotation_id`, id).Scan(&rotationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return rotationID, err
}

// PendingAgentCredentialRotationCancellation reports a cancellation that a
// reconnecting agent must apply before it can promote a replacement credential.
func (s *Store) PendingAgentCredentialRotationCancellation(ctx context.Context, id string) (string, error) {
	var rotationID string
	err := s.pool.QueryRow(ctx, `SELECT token_rotation_id FROM agents
		WHERE id=$1 AND revoked_at IS NULL AND pending_token_hash IS NOT NULL AND token_rotation_canceling`, id).Scan(&rotationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return rotationID, err
}

// ConfirmAgentCredentialRotationCancellation clears a replacement only after
// the agent has durably restored its previous credential. A spontaneous agent
// acknowledgement cannot cancel a rotation the server did not request.
func (s *Store) ConfirmAgentCredentialRotationCancellation(ctx context.Context, id, rotationID string) (bool, error) {
	cmd, err := s.pool.Exec(ctx, `UPDATE agents SET pending_token_hash=NULL, token_rotation_id=NULL, pending_token_expires_at=NULL,
		token_rotation_canceling=FALSE
		WHERE id=$1 AND token_rotation_id=$2 AND pending_token_hash IS NOT NULL AND token_rotation_canceling`, id, rotationID)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() > 0, nil
}

// ListAgents returns all agents, newest first.
func (s *Store) ListAgents(ctx context.Context) ([]models.Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentCols+` FROM agents ORDER BY created_at DESC`)
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

// SearchAgents finds agents whose hostname matches a query (scope-tree search).
func (s *Store) SearchAgents(ctx context.Context, q string, limit int) ([]models.Agent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT `+agentCols+` FROM agents WHERE hostname ILIKE '%' || $1 || '%' ORDER BY hostname LIMIT $2`, q, limit)
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

// ListAgentsBySite lists the agents in one site (for the scope tree's lazy load).
func (s *Store) ListAgentsBySite(ctx context.Context, siteID string) ([]models.Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentCols+` FROM agents WHERE site_id=$1 ORDER BY hostname`, siteID)
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

// ListAgentsByGroup returns the agents in a device group.
func (s *Store) ListAgentsByGroup(ctx context.Context, groupID string) ([]models.Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentCols+` FROM agents WHERE group_id=$1 ORDER BY hostname`, groupID)
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

// SetAgentStatus updates online/offline state and last_seen.
func (s *Store) SetAgentStatus(ctx context.Context, id, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET status=$2, last_seen=now() WHERE id=$1 AND revoked_at IS NULL`, id, status)
	return err
}

// MarkAgentOffline reconciles a stale 'online' row to 'offline' without touching
// last_seen, so the displayed "last seen" stays truthful. Used by the scheduler
// sweep to catch agents whose socket died without a clean detach (or that were
// left online by a previous server run).
func (s *Store) MarkAgentOffline(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET status='offline' WHERE id=$1 AND status='online'`, id)
	return err
}

// TouchAgent bumps last_seen (heartbeat).
func (s *Store) TouchAgent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen=now(), status='online' WHERE id=$1 AND revoked_at IS NULL`, id)
	return err
}

// RefreshAgentInfo updates the metadata an agent reports at (re-)registration,
// so a binary upgrade is reflected (version/hostname/os/arch otherwise stay
// frozen at first enrollment).
func (s *Store) RefreshAgentInfo(ctx context.Context, id, hostname, os, arch, version string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE agents SET hostname=$2, os=$3, arch=$4, version=$5, last_seen=now() WHERE id=$1 AND revoked_at IS NULL`,
		id, hostname, os, arch, version)
	return err
}

// RevokeAgent invalidates the device credential while preserving the endpoint's
// historical data. A revoked device must be enrolled again to regain access.
func (s *Store) RevokeAgent(ctx context.Context, id string) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE agents
		SET token_hash='', pending_token_hash=NULL, token_rotation_id=NULL, pending_token_expires_at=NULL, token_rotation_canceling=FALSE,
			status='revoked', revoked_at=now()
		WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRemoteIDs records the MeshCentral node id reported for an agent.
func (s *Store) SetRemoteIDs(ctx context.Context, id string, mesh *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE agents SET mesh_id=COALESCE($2, mesh_id) WHERE id=$1`,
		id, mesh)
	return err
}

// ---------- Enroll tokens ----------

// CreateEnrollToken issues a new enrollment token, optionally bound to a site.
func (s *Store) CreateEnrollToken(ctx context.Context, token, desc string, uses int, expires *time.Time, siteID *string) (*models.EnrollToken, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO enroll_tokens (id, token, description, uses_left, expires_at, site_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, token, desc, uses, expires, siteID)
	if err != nil {
		return nil, err
	}
	return &models.EnrollToken{ID: id, Token: token, Description: desc, UsesLeft: uses, ExpiresAt: expires, CreatedAt: time.Now()}, nil
}

// ConsumeEnrollToken validates and decrements an enroll token, returning the
// site it binds the new agent to (nil if unbound).
func (s *Store) ConsumeEnrollToken(ctx context.Context, token string) (siteID *string, err error) {
	err = s.pool.QueryRow(ctx,
		`UPDATE enroll_tokens SET uses_left = uses_left - 1
		 WHERE token=$1 AND uses_left > 0 AND (expires_at IS NULL OR expires_at > now())
		 RETURNING site_id`, token).Scan(&siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return siteID, err
}

// ---------- API tokens ----------

// CreateAPIToken stores a hashed API token.
func (s *Store) CreateAPIToken(ctx context.Context, hash, name, role string, userID *string, expiresAt *time.Time) (string, error) {
	id := newID()
	_, err := s.pool.Exec(ctx, `INSERT INTO api_tokens (id, token_hash, name, role, user_id, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, id, hash, name, role, userID, expiresAt)
	return id, err
}

// APITokenExists reports whether a token hash is known.
func (s *Store) APITokenExists(ctx context.Context, hash string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(1) FROM api_tokens WHERE token_hash=$1`, hash).Scan(&n)
	return n > 0, err
}

// ---------- Jobs ----------

const jobCols = `id, agent_id, type, description, shell, script, status, requires_approval,
	created_by, approved_by, task_id, batch_id, exit_code, stdout, stderr, created_at, updated_at`

func scanJob(row pgx.Row) (*models.Job, error) {
	var j models.Job
	err := row.Scan(&j.ID, &j.AgentID, &j.Type, &j.Description, &j.Shell, &j.Script, &j.Status,
		&j.RequiresApproval, &j.CreatedBy, &j.ApprovedBy, &j.TaskID, &j.BatchID, &j.ExitCode, &j.Stdout, &j.Stderr,
		&j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// CreateJob inserts a new job.
func (s *Store) CreateJob(ctx context.Context, j *models.Job) (*models.Job, error) {
	j.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (id, agent_id, type, description, shell, script, status, requires_approval, created_by, task_id, batch_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		j.ID, j.AgentID, j.Type, j.Description, j.Shell, j.Script, j.Status, j.RequiresApproval, j.CreatedBy, j.TaskID, j.BatchID)
	if err != nil {
		return nil, err
	}
	return s.GetJob(ctx, j.ID)
}

// GetJob fetches one job.
func (s *Store) GetJob(ctx context.Context, id string) (*models.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id=$1`, id))
}

// ListJobsByBatch returns all jobs that share a batch_id (a bulk deployment).
func (s *Store) ListJobsByBatch(ctx context.Context, batchID string) ([]models.Job, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE batch_id=$1 ORDER BY created_at`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// ListJobs returns jobs, optionally filtered by agent, newest first.
func (s *Store) ListJobs(ctx context.Context, agentID string, limit int) ([]models.Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows pgx.Rows
	var err error
	if agentID != "" {
		rows, err = s.pool.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE agent_id=$1 ORDER BY created_at DESC LIMIT $2`, agentID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT `+jobCols+` FROM jobs ORDER BY created_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// ListDispatchableJobs returns approved-but-not-finished jobs for an agent.
func (s *Store) ListDispatchableJobs(ctx context.Context, agentID string) ([]models.Job, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+jobCols+` FROM jobs WHERE agent_id=$1 AND status IN ('pending','sent')
		 AND EXISTS (SELECT 1 FROM agents a WHERE a.id=jobs.agent_id AND a.revoked_at IS NULL)
		 ORDER BY created_at`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// SetJobStatus updates the status, optionally recording the approver.
func (s *Store) SetJobStatus(ctx context.Context, id, status string, approvedBy *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status=$2, approved_by=COALESCE($3, approved_by), updated_at=now() WHERE id=$1`,
		id, status, approvedBy)
	return err
}

// SetJobResult records the outcome of a finished job.
func (s *Store) SetJobResult(ctx context.Context, id, status string, exitCode int, stdout, stderr string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status=$2, exit_code=$3, stdout=$4, stderr=$5, updated_at=now() WHERE id=$1`,
		id, status, exitCode, stdout, stderr)
	return err
}

// ---------- Inventory ----------

// UpsertInventory stores the latest inventory snapshot for an agent.
func (s *Store) UpsertInventory(ctx context.Context, agentID string, data []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO inventory (agent_id, data, collected_at) VALUES ($1,$2,now())
		 ON CONFLICT (agent_id) DO UPDATE SET data=EXCLUDED.data, collected_at=now()`,
		agentID, data)
	return err
}

// GetInventory returns the latest inventory for an agent.
func (s *Store) GetInventory(ctx context.Context, agentID string) (*models.Inventory, error) {
	var inv models.Inventory
	err := s.pool.QueryRow(ctx, `SELECT agent_id, data, collected_at FROM inventory WHERE agent_id=$1`, agentID).
		Scan(&inv.AgentID, &inv.Data, &inv.CollectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// ---------- Remote sessions ----------

// CreateRemoteSession persists a brokered remote-access session.
func (s *Store) CreateRemoteSession(ctx context.Context, rs *models.RemoteSession) error {
	rs.ID = newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO remote_sessions (id, agent_id, provider, status, token, connect_url, created_by, expires_at, viewer_ticket_hash, agent_ticket_hash)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		rs.ID, rs.AgentID, rs.Provider, rs.Status, rs.Token, rs.ConnectURL, rs.CreatedBy, rs.ExpiresAt,
		hashIfSet(rs.ViewerTicket), hashIfSet(rs.AgentTicket))
	return err
}

func hashIfSet(token string) *string {
	if token == "" {
		return nil
	}
	h := auth.Hash(token)
	return &h
}

func scanRemoteSession(row pgx.Row) (*models.RemoteSession, error) {
	var rs models.RemoteSession
	err := row.Scan(&rs.ID, &rs.AgentID, &rs.Provider, &rs.Status, &rs.Token, &rs.ConnectURL,
		&rs.CreatedBy, &rs.CreatedAt, &rs.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rs, nil
}

const remoteSessionCols = `id, agent_id, provider, status, token, connect_url, created_by, created_at, expires_at`

// GetRemoteSessionForAuthorization returns non-secret session metadata used to
// revalidate a viewer's current permission and device scope before ticket use.
func (s *Store) GetRemoteSessionForAuthorization(ctx context.Context, sessionID string) (*models.RemoteSession, error) {
	return scanRemoteSession(s.pool.QueryRow(ctx,
		`SELECT `+remoteSessionCols+` FROM remote_sessions WHERE id=$1`, sessionID))
}

// GetRemoteSessionByToken looks up a session by its ephemeral token.
func (s *Store) GetRemoteSessionByToken(ctx context.Context, token string) (*models.RemoteSession, error) {
	return scanRemoteSession(s.pool.QueryRow(ctx,
		`SELECT `+remoteSessionCols+` FROM remote_sessions WHERE token=$1`, token))
}

// ConsumeRemoteSessionTicket atomically redeems one peer's short-lived relay
// ticket. A ticket is never stored in plaintext and cannot be replayed.
func (s *Store) ConsumeRemoteSessionTicket(ctx context.Context, sessionID, role, ticket, viewerSubject string) (*models.RemoteSession, error) {
	var hashCol, usedCol string
	switch role {
	case "viewer":
		if viewerSubject == "" {
			return nil, ErrNotFound
		}
		hashCol, usedCol = "viewer_ticket_hash", "viewer_used_at"
	case "agent":
		hashCol, usedCol = "agent_ticket_hash", "agent_used_at"
	default:
		return nil, ErrNotFound
	}
	q := `UPDATE remote_sessions AS rs SET ` + usedCol + `=now(), status='active'
		WHERE rs.id=$1 AND rs.` + hashCol + `=$2 AND rs.` + usedCol + ` IS NULL
		  AND rs.status IN ('pending','active') AND rs.expires_at > now()
		  AND EXISTS (SELECT 1 FROM agents a WHERE a.id=rs.agent_id AND a.revoked_at IS NULL)`
	if role == "viewer" {
		q += ` AND rs.created_by=$3`
	}
	q += ` RETURNING ` + remoteSessionCols
	if role == "viewer" {
		return scanRemoteSession(s.pool.QueryRow(ctx, q, sessionID, auth.Hash(ticket), viewerSubject))
	}
	return scanRemoteSession(s.pool.QueryRow(ctx, q, sessionID, auth.Hash(ticket)))
}

// ConsumeLegacyAgentTicket admits a pre-ticket-handshake sidecar during the
// agent upgrade window. Only the agent role may use this path; its one-time
// ticket is still hashed, expires normally, and cannot authenticate a viewer.
func (s *Store) ConsumeLegacyAgentTicket(ctx context.Context, ticket string) (*models.RemoteSession, error) {
	q := `UPDATE remote_sessions AS rs SET agent_used_at=now(), status='active'
		WHERE rs.agent_ticket_hash=$1 AND rs.agent_used_at IS NULL
		  AND rs.status IN ('pending','active') AND rs.expires_at > now()
		  AND EXISTS (SELECT 1 FROM agents a WHERE a.id=rs.agent_id AND a.revoked_at IS NULL)
		RETURNING ` + remoteSessionCols
	return scanRemoteSession(s.pool.QueryRow(ctx, q, auth.Hash(ticket)))
}

// EndRemoteSessionsForAgent invalidates unpaired and active native sessions when
// an endpoint is revoked. External-provider sessions remain provider-owned.
func (s *Store) EndRemoteSessionsForAgent(ctx context.Context, agentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE remote_sessions SET status='ended'
		WHERE agent_id=$1 AND provider IN ('native','files','terminal') AND status IN ('pending','active')`, agentID)
	return err
}

// HasLiveNativeRemoteSession reports whether restarting an endpoint would cut an
// operator's native desktop, terminal, or file-transfer session.
func (s *Store) HasLiveNativeRemoteSession(ctx context.Context, agentID string) (bool, error) {
	var live bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM remote_sessions
		WHERE agent_id=$1 AND provider IN ('native','files','terminal')
		  AND status IN ('pending','active') AND expires_at > now())`, agentID).Scan(&live)
	return live, err
}

// SetRemoteSessionStatus updates a session's lifecycle state.
func (s *Store) SetRemoteSessionStatus(ctx context.Context, id, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE remote_sessions SET status=$2 WHERE id=$1`, id, status)
	return err
}

// ---------- Audit ----------

// AddAudit appends an immutable audit record.
func (s *Store) AddAudit(ctx context.Context, actor, action, target string, detail []byte) error {
	if detail == nil {
		detail = []byte("{}")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (id, actor, action, target, detail) VALUES ($1,$2,$3,$4,$5)`,
		newID(), actor, action, target, detail)
	return err
}

// ListAudit returns recent audit entries.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]models.AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, actor, action, target, detail, created_at FROM audit_log ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AuditEntry
	for rows.Next() {
		var e models.AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------- Chat ----------

// CreateChatSession starts a new conversation.
func (s *Store) CreateChatSession(ctx context.Context, title, owner string) (*models.ChatSession, error) {
	if title == "" {
		title = "New chat"
	}
	id := newID()
	_, err := s.pool.Exec(ctx, `INSERT INTO chat_sessions (id, title, created_by) VALUES ($1,$2,$3)`, id, title, owner)
	if err != nil {
		return nil, err
	}
	return &models.ChatSession{ID: id, Title: title, CreatedAt: time.Now()}, nil
}

// ChatSessionOwner returns the actor that owns a chat session ("" if unowned).
func (s *Store) ChatSessionOwner(ctx context.Context, id string) (string, error) {
	var owner *string
	err := s.pool.QueryRow(ctx, `SELECT created_by FROM chat_sessions WHERE id=$1`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if owner == nil {
		return "", err
	}
	return *owner, err
}

// ListChatSessions returns a user's own conversations, newest first.
func (s *Store) ListChatSessions(ctx context.Context, owner string) ([]models.ChatSession, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, title, created_at FROM chat_sessions WHERE created_by=$1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ChatSession
	for rows.Next() {
		var c models.ChatSession
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChatSession removes a conversation and its messages (FK cascade).
func (s *Store) DeleteChatSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM chat_sessions WHERE id=$1`, id)
	return err
}

// MaybeTitleChatSession sets the title only while it is still a placeholder, so
// the first user message names the conversation without overwriting a real title.
func (s *Store) MaybeTitleChatSession(ctx context.Context, id, title string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE chat_sessions SET title=$2 WHERE id=$1 AND (title='' OR title='New chat' OR title='New conversation')`,
		id, title)
	return err
}

// ChatSessionExists reports whether a session id is valid.
func (s *Store) ChatSessionExists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(1) FROM chat_sessions WHERE id=$1`, id).Scan(&n)
	return n > 0, err
}

// AddChatMessage appends a message to a session.
func (s *Store) AddChatMessage(ctx context.Context, m *models.ChatMessage) error {
	m.ID = newID()
	var toolCalls interface{}
	if len(m.ToolCalls) > 0 {
		toolCalls = []byte(m.ToolCalls)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO chat_messages (id, session_id, role, content, tool_calls, tool_call_id, name)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		m.ID, m.SessionID, m.Role, m.Content, toolCalls, m.ToolCallID, m.Name)
	return err
}

// ListChatMessages returns the full ordered history of a session.
func (s *Store) ListChatMessages(ctx context.Context, sessionID string) ([]models.ChatMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, role, content, tool_calls, tool_call_id, name, created_at
		 FROM chat_messages WHERE session_id=$1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ChatMessage
	for rows.Next() {
		var m models.ChatMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID, &m.Name, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
