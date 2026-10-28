package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppSetting is a persisted runtime setting. Secret values are expected to be
// encrypted before reaching the store layer.
type AppSetting struct {
	Key       string    `json:"key"`
	Value     string    `json:"-"`
	IsSecret  bool      `json:"is_secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListAppSettings returns all runtime settings keyed by name.
func (s *Store) ListAppSettings(ctx context.Context) (map[string]AppSetting, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value, is_secret, updated_at FROM app_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AppSetting{}
	for rows.Next() {
		var st AppSetting
		if err := rows.Scan(&st.Key, &st.Value, &st.IsSecret, &st.UpdatedAt); err != nil {
			return nil, err
		}
		out[st.Key] = st
	}
	return out, rows.Err()
}

// GetAppSetting returns one setting by key.
func (s *Store) GetAppSetting(ctx context.Context, key string) (*AppSetting, error) {
	var st AppSetting
	err := s.pool.QueryRow(ctx, `SELECT key, value, is_secret, updated_at FROM app_settings WHERE key=$1`, key).
		Scan(&st.Key, &st.Value, &st.IsSecret, &st.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// DeleteAppSetting removes a setting; a missing one is not an error.
func (s *Store) DeleteAppSetting(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM app_settings WHERE key=$1`, key)
	return err
}

// UpsertAppSetting writes a setting value.
func (s *Store) UpsertAppSetting(ctx context.Context, key, value string, secret bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, is_secret, updated_at)
		VALUES ($1,$2,$3,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, is_secret=EXCLUDED.is_secret, updated_at=now()`,
		key, value, secret)
	return err
}
