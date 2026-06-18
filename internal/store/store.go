// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/models"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

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

// Migrate runs the (idempotent) schema.
func (s *Store) Migrate(ctx context.Context, schema string) error {
	_, err := s.pool.Exec(ctx, schema)
	return err
}

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
	_, err := s.pool.Exec(ctx, `UPDATE agents SET site_id=$2 WHERE id=$1`, id, siteID)
	return err
}

func scanAgent(row pgx.Row) (*models.Agent, error) {
	var a models.Agent
	err := row.Scan(&a.ID, &a.Hostname, &a.Description, &a.Note, &a.OS, &a.Arch, &a.Version, &a.Status,
		&a.SiteID, &a.GroupID, &a.Tags, &a.MeshID, &a.LastSeen, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

const agentCols = `id, hostname, description, note, os, arch, version, status, site_id, group_id, tags, mesh_id, last_seen, created_at`

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
	err := s.pool.QueryRow(ctx, `SELECT token_hash FROM agents WHERE id=$1`, id).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
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

// SetAgentStatus updates online/offline state and last_seen.
func (s *Store) SetAgentStatus(ctx context.Context, id, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET status=$2, last_seen=now() WHERE id=$1`, id, status)
	return err
}

// TouchAgent bumps last_seen (heartbeat).
func (s *Store) TouchAgent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen=now(), status='online' WHERE id=$1`, id)
	return err
}

// RefreshAgentInfo updates the metadata an agent reports at (re-)registration,
// so a binary upgrade is reflected (version/hostname/os/arch otherwise stay
// frozen at first enrollment).
func (s *Store) RefreshAgentInfo(ctx context.Context, id, hostname, os, arch, version string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE agents SET hostname=$2, os=$3, arch=$4, version=$5, last_seen=now(), status='online' WHERE id=$1`,
		id, hostname, os, arch, version)
	return err
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
func (s *Store) CreateAPIToken(ctx context.Context, hash, name, role string, userID *string) (string, error) {
	id := newID()
	_, err := s.pool.Exec(ctx, `INSERT INTO api_tokens (id, token_hash, name, role, user_id) VALUES ($1,$2,$3,$4,$5)`, id, hash, name, role, userID)
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
	created_by, approved_by, task_id, exit_code, stdout, stderr, created_at, updated_at`

func scanJob(row pgx.Row) (*models.Job, error) {
	var j models.Job
	err := row.Scan(&j.ID, &j.AgentID, &j.Type, &j.Description, &j.Shell, &j.Script, &j.Status,
		&j.RequiresApproval, &j.CreatedBy, &j.ApprovedBy, &j.TaskID, &j.ExitCode, &j.Stdout, &j.Stderr,
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
		`INSERT INTO jobs (id, agent_id, type, description, shell, script, status, requires_approval, created_by, task_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		j.ID, j.AgentID, j.Type, j.Description, j.Shell, j.Script, j.Status, j.RequiresApproval, j.CreatedBy, j.TaskID)
	if err != nil {
		return nil, err
	}
	return s.GetJob(ctx, j.ID)
}

// GetJob fetches one job.
func (s *Store) GetJob(ctx context.Context, id string) (*models.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id=$1`, id))
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
		`SELECT `+jobCols+` FROM jobs WHERE agent_id=$1 AND status IN ('pending','sent') ORDER BY created_at`, agentID)
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
		`INSERT INTO remote_sessions (id, agent_id, provider, status, token, connect_url, created_by, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		rs.ID, rs.AgentID, rs.Provider, rs.Status, rs.Token, rs.ConnectURL, rs.CreatedBy, rs.ExpiresAt)
	return err
}

// GetRemoteSessionByToken looks up a session by its ephemeral token.
func (s *Store) GetRemoteSessionByToken(ctx context.Context, token string) (*models.RemoteSession, error) {
	var rs models.RemoteSession
	err := s.pool.QueryRow(ctx,
		`SELECT id, agent_id, provider, status, token, connect_url, created_by, created_at, expires_at
		 FROM remote_sessions WHERE token=$1`, token).
		Scan(&rs.ID, &rs.AgentID, &rs.Provider, &rs.Status, &rs.Token, &rs.ConnectURL, &rs.CreatedBy, &rs.CreatedAt, &rs.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rs, nil
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
