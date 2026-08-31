// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
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
func (s *Store) CreateAgent(ctx context.Context, hostname, os, arch, version, tokenHash string, siteID, groupID *string) (*models.Agent, error) {
	id := newID()
	// The department is only honoured when it belongs to the site being set, the
	// same rule SetAgentSite applies when a device moves. The enrol token cannot
	// hold an inconsistent pair (migration 010 refuses it), so this is the second
	// line of defence rather than the first.
	_, err := s.pool.Exec(ctx,
		`INSERT INTO agents (id, hostname, os, arch, version, token_hash, site_id, group_id, status, last_seen)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,
		         (SELECT g.id FROM device_groups g WHERE g.id=$8 AND g.site_id=$7),
		         'online',now())`,
		id, hostname, os, arch, version, tokenHash, siteID, groupID)
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
func (s *Store) CreateEnrollToken(ctx context.Context, token, desc string, uses int, expires *time.Time, siteID, groupID *string) (*models.EnrollToken, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO enroll_tokens (id, token, description, uses_left, expires_at, site_id, group_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, token, desc, uses, expires, siteID, groupID)
	if err != nil {
		return nil, err
	}
	return &models.EnrollToken{ID: id, Token: token, Description: desc, UsesLeft: uses,
		ExpiresAt: expires, SiteID: siteID, GroupID: groupID, CreatedAt: time.Now()}, nil
}

// ConsumeEnrollToken validates and decrements an enroll token, returning where
// it places the new agent: the site, and the department within it when the token
// names one. Both nil when the token is unbound.
func (s *Store) ConsumeEnrollToken(ctx context.Context, token string) (siteID, groupID *string, err error) {
	err = s.pool.QueryRow(ctx,
		`UPDATE enroll_tokens SET uses_left = uses_left - 1
		 WHERE token=$1 AND uses_left > 0 AND (expires_at IS NULL OR expires_at > now())
		 RETURNING site_id, group_id`, token).Scan(&siteID, &groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	return siteID, groupID, err
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
// auditWriteFailures counts audit entries that could not be persisted. It is
// exported through /metrics because this is the one failure the audit log cannot
// report about itself: if the insert fails, the trail simply has a hole, and the
// hole is discovered later by someone who needed it.
var auditWriteFailures atomic.Uint64

// AuditWriteFailures returns how many audit writes have been lost since start.
func AuditWriteFailures() uint64 { return auditWriteFailures.Load() }

func (s *Store) AddAudit(ctx context.Context, actor, action, target string, detail []byte) error {
	if detail == nil {
		detail = []byte("{}")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (id, actor, action, target, detail) VALUES ($1,$2,$3,$4,$5)`,
		newID(), actor, action, target, detail)
	if err != nil {
		// Logged and counted HERE rather than at the call sites. Of the 49 places
		// that write an audit entry, 46 discard the error — reasonably, since an
		// audit failure must not roll back the action it describes. But that left
		// the trail able to lose entries in complete silence, which for a trail is
		// the worst failure it can have. One place catches all of them.
		auditWriteFailures.Add(1)
		log.Printf("audit: FAILED to record %q by %q on %q: %v", action, actor, target, err)
	}
	return err
}

// AuditFilter narrows the trail.
//
// It exists because "the last 200 entries" is not an audit log, it is a
// dashboard. On an active fleet 200 entries is under a day, so the question an
// audit log is kept for — who approved that script last week — could not be
// asked through the API at all.
//
// Every field is optional; an empty filter is the previous behaviour.
type AuditFilter struct {
	Actor  string    // exact match, case-insensitive
	Action string    // prefix match, so "job." finds every job event
	Target string    // exact match: an id, usually
	Since  time.Time // inclusive
	Until  time.Time // exclusive
	Limit  int
	Offset int
}

// ListAudit returns audit entries matching the filter, newest first, together
// with the total number of matches so a caller can page through them.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]models.AuditEntry, int, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var conds []string
	var args []any
	add := func(cond string, value any) {
		args = append(args, value)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Actor != "" {
		add("lower(actor) = lower($%d)", f.Actor)
	}
	if f.Action != "" {
		// Prefix rather than equality: the actions are dotted namespaces
		// ("job.approve", "job.reject"), and the useful question is almost always
		// about the family. Escaped so a filter containing % or _ matches
		// literally instead of turning into a wildcard.
		add("action LIKE $%d || '%%'", strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(f.Action))
	}
	if f.Target != "" {
		add("target = $%d", f.Target)
	}
	if !f.Since.IsZero() {
		add("created_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("created_at < $%d", f.Until)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	// The total is what makes paging honest: without it the console cannot say
	// whether "200 results" means 200 or the first 200 of nine thousand.
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.pool.Query(ctx,
		`SELECT id, actor, action, target, detail, created_at FROM audit_log`+where+
			fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.AuditEntry
	for rows.Next() {
		var e models.AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// machineAuditActions are the entries written by the system rather than by a
// person. They are the only ones retention touches.
//
// The distinction matters more than a date does. An audit log's value is the
// human record — who approved this script, who changed that scope, who moved
// mail delivery — and that is low-volume enough to keep indefinitely: measured
// on a real instance, an audit row costs 550 bytes with its index.
//
// These two are different. They are telemetry that happens to live in the same
// table, and they scale with the fleet rather than with the number of people:
// job.automated is one row per scheduled task, per device, per run. A tenant
// with 200 devices and three hourly tasks writes 14,400 of them a day — about
// 2.9 GB a year, and job.result doubles it. That is a quarter of the Basic
// plan's disk, spent on rows nobody will ever read.
//
// job.result is also duplicative: the jobs table already holds status,
// exit_code, stdout and stderr. Deleting the audit copy loses nothing the
// system does not still have.
var machineAuditActions = []string{"job.automated", "job.result"}

// minAuditRetentionDays is a floor, not a default. A configured value below it
// is far more likely to be a mistake — or someone shortening the window on
// purpose — than a real requirement, and refusing costs nothing.
const minAuditRetentionDays = 30

// PurgeAuditLog deletes machine-written audit entries older than the cutoff and
// records that it did so.
//
// Everything a person did is kept, whatever the age. Retention here is about the
// telemetry that shares the table, not about forgetting who did what.
//
// days <= 0 disables the purge entirely: unlike the monitoring sweep, which
// forces a default when handed zero, an audit log that deletes nothing is a
// perfectly reasonable configuration and must be reachable.
func (s *Store) PurgeAuditLog(ctx context.Context, now time.Time, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	if days < minAuditRetentionDays {
		days = minAuditRetentionDays
	}
	cutoff := now.AddDate(0, 0, -days)

	// Through the function, not a direct DELETE. The table refuses deletes from
	// anywhere else — and once the server connects as openrmm_app, which holds no
	// DELETE on audit_log at all, this is the only path that exists. The function
	// can remove aged machine entries and nothing else.
	var deleted int64
	if err := s.pool.QueryRow(ctx, `SELECT purge_machine_audit($1)`, cutoff).Scan(&deleted); err != nil {
		return 0, err
	}
	if deleted == 0 {
		return 0, nil
	}

	// The trail explains its own gaps. Without this, a hole in the log is
	// indistinguishable from someone having edited it — and this entry is itself
	// a human-class action, so it is never purged.
	detail := fmt.Sprintf(`{"deleted":%d,"older_than":%q,"actions":%q}`,
		deleted, cutoff.UTC().Format(time.RFC3339), strings.Join(machineAuditActions, ","))
	if err := s.AddAudit(ctx, "system", "audit.purge", "", []byte(detail)); err != nil {
		// The rows are already gone; failing here would only hide that too. The
		// AddAudit failure is logged and counted on its own.
		return deleted, err
	}
	return deleted, nil
}

// AuditChainBreak is a point where the trail stopped adding up.
type AuditChainBreak struct {
	EntryID string    `json:"entry_id"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}

// VerifyAuditChain walks the hashed entries and reports the first that does not
// follow from its predecessor. Nil means the human record is intact.
//
// What this does and does not prove is worth being precise about. Each chained
// entry carries a hash of its own contents plus the previous entry's hash, so
// editing or removing one breaks every link after it. That makes tampering
// evident — it does not prevent it: whoever can write to the table can also
// recompute the whole chain.
//
// It earns its keep alongside the privilege separation, not instead of it. With
// the application unable to delete, rewriting the chain needs the separate
// owner credentials, and the chain means even that leaves a trace unless the
// rewrite is complete and consistent.
//
// Only the human record is chained; machine-written entries are excluded
// because retention deletes them, and a chain a routine purge severs is an
// alarm nobody keeps.
func (s *Store) VerifyAuditChain(ctx context.Context) (*AuditChainBreak, error) {
	var out AuditChainBreak
	err := s.pool.QueryRow(ctx,
		`SELECT broken_id, broken_at, reason FROM verify_audit_chain()`).
		Scan(&out.EntryID, &out.At, &out.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // intact
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AuditActors and AuditActions list the distinct values present, so the console
// can offer a filter built from what actually happened rather than a free-text
// box the operator has to guess into.
func (s *Store) AuditFacets(ctx context.Context) (actors []string, actions []string, err error) {
	actorRows, err := s.pool.Query(ctx,
		`SELECT DISTINCT actor FROM audit_log WHERE actor <> '' ORDER BY actor LIMIT 200`)
	if err != nil {
		return nil, nil, err
	}
	defer actorRows.Close()
	for actorRows.Next() {
		var a string
		if err := actorRows.Scan(&a); err != nil {
			return nil, nil, err
		}
		actors = append(actors, a)
	}
	if err := actorRows.Err(); err != nil {
		return nil, nil, err
	}

	// The family, not the full action: "job.approve" and "job.reject" both offer
	// "job", which is the granularity someone filtering actually wants.
	actionRows, err := s.pool.Query(ctx,
		`SELECT DISTINCT split_part(action, '.', 1) AS family FROM audit_log
		 WHERE action <> '' ORDER BY family LIMIT 200`)
	if err != nil {
		return nil, nil, err
	}
	defer actionRows.Close()
	for actionRows.Next() {
		var a string
		if err := actionRows.Scan(&a); err != nil {
			return nil, nil, err
		}
		actions = append(actions, a)
	}
	return actors, actions, actionRows.Err()
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
