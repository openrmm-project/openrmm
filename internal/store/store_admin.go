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
	rows, err := s.pool.Query(ctx, `SELECT id, name, role, created_at FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.APITokenInfo
	for rows.Next() {
		var t models.APITokenInfo
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes an API token by id.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id=$1`, id)
	return err
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

// ClientForAgents maps each agent id to its company (client) id, resolved via
// site → client. Agents with no site are omitted (they have no company). Used to
// split a bulk deployment into one batch per company.
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
	_, err := s.pool.Exec(ctx, `UPDATE agents SET group_id=$2 WHERE id=$1`, agentID, groupID)
	return err
}
