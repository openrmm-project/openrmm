package store

import (
	"context"
	"time"

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

// CreateGroup adds a device group.
func (s *Store) CreateGroup(ctx context.Context, name string) (*models.DeviceGroup, error) {
	id := newID()
	if _, err := s.pool.Exec(ctx, `INSERT INTO device_groups (id, name) VALUES ($1,$2)`, id, name); err != nil {
		return nil, err
	}
	return &models.DeviceGroup{ID: id, Name: name, CreatedAt: time.Now()}, nil
}

// ListGroups returns all device groups.
func (s *Store) ListGroups(ctx context.Context) ([]models.DeviceGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at FROM device_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DeviceGroup
	for rows.Next() {
		var g models.DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
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
