package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// ListRoles returns every role with its permission keys.
func (s *Store) ListRoles(ctx context.Context) ([]models.Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT role_key, name, description, system, created_at, updated_at FROM roles ORDER BY system DESC, role_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Role
	byKey := map[string]int{}
	for rows.Next() {
		var r models.Role
		if err := rows.Scan(&r.Key, &r.Name, &r.Description, &r.System, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		byKey[r.Key] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prows, err := s.pool.Query(ctx, `SELECT role_key, permission FROM role_permissions ORDER BY permission`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var key, perm string
		if err := prows.Scan(&key, &perm); err != nil {
			return nil, err
		}
		if i, ok := byKey[key]; ok {
			out[i].Permissions = append(out[i].Permissions, perm)
		}
	}
	return out, prows.Err()
}

func (s *Store) GetRole(ctx context.Context, key string) (*models.Role, error) {
	var r models.Role
	err := s.pool.QueryRow(ctx, `SELECT role_key, name, description, system, created_at, updated_at FROM roles WHERE role_key=$1`, key).
		Scan(&r.Key, &r.Name, &r.Description, &r.System, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	perms, err := s.RolePermissions(ctx, key)
	if err != nil {
		return nil, err
	}
	r.Permissions = perms
	return &r, nil
}

func (s *Store) RoleExists(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(1) FROM roles WHERE role_key=$1`, key).Scan(&n)
	return n > 0, err
}

func (s *Store) RolePermissions(ctx context.Context, key string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT permission FROM role_permissions WHERE role_key=$1 ORDER BY permission`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateRole(ctx context.Context, r *models.Role) (*models.Role, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx,
		`INSERT INTO roles (role_key, name, description, system) VALUES ($1,$2,$3,FALSE)
		 RETURNING role_key, name, description, system, created_at, updated_at`,
		r.Key, r.Name, r.Description).Scan(&r.Key, &r.Name, &r.Description, &r.System, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if err := replaceRolePermissions(ctx, tx, r.Key, r.Permissions); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRole(ctx, r.Key)
}

func (s *Store) UpdateRole(ctx context.Context, r *models.Role) (*models.Role, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var system bool
	if err := tx.QueryRow(ctx, `SELECT system FROM roles WHERE role_key=$1`, r.Key).Scan(&system); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if system {
		return nil, fmt.Errorf("system roles cannot be edited")
	}
	if _, err := tx.Exec(ctx, `UPDATE roles SET name=$2, description=$3, updated_at=now() WHERE role_key=$1`, r.Key, r.Name, r.Description); err != nil {
		return nil, err
	}
	if err := replaceRolePermissions(ctx, tx, r.Key, r.Permissions); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRole(ctx, r.Key)
}

func (s *Store) DeleteRole(ctx context.Context, key string) error {
	var system bool
	err := s.pool.QueryRow(ctx, `SELECT system FROM roles WHERE role_key=$1`, key).Scan(&system)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if system {
		return fmt.Errorf("system roles cannot be deleted")
	}
	var refs int
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(1) FROM users WHERE role=$1) +
		(SELECT count(1) FROM api_tokens WHERE role=$1)`, key).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("role is assigned to %d principal(s)", refs)
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM roles WHERE role_key=$1`, key)
	return err
}

func replaceRolePermissions(ctx context.Context, tx pgx.Tx, key string, permissions []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_key=$1`, key); err != nil {
		return err
	}
	for _, p := range permissions {
		if p == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_permissions (role_key, permission) VALUES ($1,$2) ON CONFLICT DO NOTHING`, key, p); err != nil {
			return err
		}
	}
	return nil
}
