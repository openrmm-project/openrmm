package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// ===================== API tokens (management) =====================

// ListAPITokens returns all API tokens (without the secret).
func (s *Store) ListAPITokens(ctx context.Context) ([]models.APITokenInfo, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, role, created_at, expires_at FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.APITokenInfo
	for rows.Next() {
		var t models.APITokenInfo
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.CreatedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAPITokensForUser returns only tokens inheriting the specified user's
// tenant scope.
func (s *Store) ListAPITokensForUser(ctx context.Context, userID string) ([]models.APITokenInfo, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, role, created_at, expires_at
		FROM api_tokens WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.APITokenInfo
	for rows.Next() {
		var token models.APITokenInfo
		if err := rows.Scan(&token.ID, &token.Name, &token.Role, &token.CreatedAt, &token.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes an API token by id.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAPITokenForUser(ctx context.Context, id, userID string) (bool, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id=$1 AND user_id=$2`, id, userID)
	return err == nil && ct.RowsAffected() == 1, err
}

// ===================== Device deletion =====================

// DeleteAgent removes an agent and its dependent rows (cascade).
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE id=$1`, id)
	return err
}

// ===================== Device groups =====================

// CreateGroup adds a device group (a department) under a site.
func (s *Store) CreateGroup(ctx context.Context, name string, siteID *string) (*models.DeviceGroup, error) {
	id := newID()
	if _, err := s.pool.Exec(ctx, `INSERT INTO device_groups (id, name, site_id) VALUES ($1,$2,$3)`, id, name, siteID); err != nil {
		return nil, err
	}
	return &models.DeviceGroup{ID: id, Name: name, SiteID: siteID, CreatedAt: time.Now()}, nil
}

// GetGroup fetches one device group.
func (s *Store) GetGroup(ctx context.Context, id string) (*models.DeviceGroup, error) {
	var g models.DeviceGroup
	err := s.pool.QueryRow(ctx, `SELECT id, name, site_id, created_at FROM device_groups WHERE id=$1`, id).Scan(&g.ID, &g.Name, &g.SiteID, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGroups returns all device groups (departments).
func (s *Store) ListGroups(ctx context.Context) ([]models.DeviceGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, site_id, created_at FROM device_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DeviceGroup
	for rows.Next() {
		var g models.DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.SiteID, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListGroupsForUser returns departments inside the user's access scope. A user
// with no scope rows sees all departments; otherwise a department is visible if
// it is directly scoped, under a scoped site/client, or contains a scoped agent.
func (s *Store) ListGroupsForUser(ctx context.Context, userID string) ([]models.DeviceGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, site_id, created_at FROM device_groups g WHERE
		NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		OR g.id IN (SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='group')
		OR g.site_id IN (SELECT scope_id FROM user_scopes WHERE user_id=$1 AND scope_type='site')
		OR g.site_id IN (
		  SELECT s.id FROM sites s JOIN user_scopes us ON us.scope_id=s.client_id
		  WHERE us.user_id=$1 AND us.scope_type='client'
		)
		OR g.id IN (
		  SELECT a.group_id FROM agents a JOIN user_scopes us ON us.scope_id=a.id
		  WHERE us.user_id=$1 AND us.scope_type='agent' AND a.group_id IS NOT NULL
		)
		ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DeviceGroup
	for rows.Next() {
		var g models.DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.SiteID, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ClientForAgents maps each agent id to its company (client) id, resolved via
// site → client. Agents with no site are omitted (they have no company). Used to
// split a bulk deployment into one batch per company.
// AgentVersionInfo is what a capability gate needs about one agent: the version
// it reports, and a name to put in the message when it is too old.
type AgentVersionInfo struct {
	Hostname string
	Version  string
}

// AgentVersions returns the reported version of each agent, keyed by id.
//
// Missing ids are absent from the map rather than zero-valued, so a caller can
// tell "this agent reports no version" from "there is no such agent" — both bar
// a capability gate, but only one of them is a bug.
func (s *Store) AgentVersions(ctx context.Context, agentIDs []string) (map[string]AgentVersionInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, hostname, COALESCE(version,'') FROM agents WHERE id = ANY($1)`, agentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AgentVersionInfo{}
	for rows.Next() {
		var id string
		var info AgentVersionInfo
		if err := rows.Scan(&id, &info.Hostname, &info.Version); err != nil {
			return nil, err
		}
		out[id] = info
	}
	return out, rows.Err()
}

func (s *Store) ClientForAgents(ctx context.Context, agentIDs []string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, s.client_id FROM agents a JOIN sites s ON s.id = a.site_id WHERE a.id = ANY($1)`, agentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var aid, cid string
		if err := rows.Scan(&aid, &cid); err != nil {
			return nil, err
		}
		out[aid] = cid
	}
	return out, rows.Err()
}

// DeleteGroup removes a device group (agents keep existing; group_id is nulled).
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device_groups WHERE id=$1`, id)
	return err
}

// SetAgentGroup assigns an agent to a group (or clears it with nil).
func (s *Store) SetAgentGroup(ctx context.Context, agentID string, groupID *string) error {
	if groupID == nil || *groupID == "" {
		ct, err := s.pool.Exec(ctx, `UPDATE agents SET group_id=NULL WHERE id=$1`, agentID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	ct, err := s.pool.Exec(ctx, `UPDATE agents AS a SET group_id=g.id
		FROM device_groups g
		WHERE a.id=$1 AND g.id=$2 AND a.site_id=g.site_id`, agentID, *groupID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}
