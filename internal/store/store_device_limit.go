package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/devicelimit"
	"github.com/p1ngu/rmm/internal/models"
)

// deviceLimitLock serialises enrolments while a limit is enforced, so two
// agents enrolling at once cannot both take the last free place. Any constant
// distinct from the other advisory locks in this database will do.
const deviceLimitLock int64 = 0x4f524d4d_444c4d54 // "ORMM" "DLMT"

// CountAgents is the number of devices the console holds: every enrolled
// agent, online or not. Deleting a device is what frees its place.
func (s *Store) CountAgents(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&n)
	return n, err
}

// EnrollAgent consumes an enroll token and creates the agent it admits, in one
// transaction. The token is checked first and its row locked: an enrolment
// without a valid token learns nothing about the limit and takes no lock. With
// limit > 0 the agents are then counted under an advisory lock, and one that
// would exceed the limit is refused with devicelimit.ErrLimitReached before the
// token is spent: a refused install does not use up one of the token's uses.
func (s *Store) EnrollAgent(ctx context.Context, enrollToken string, limit int, hostname, os, arch, version, tokenHash string) (*models.Agent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var siteID, groupID *string
	err = tx.QueryRow(ctx,
		`SELECT site_id, group_id FROM enroll_tokens
		 WHERE token=$1 AND uses_left > 0 AND (expires_at IS NULL OR expires_at > now())
		 FOR UPDATE`, enrollToken).Scan(&siteID, &groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Always token row, then this lock: one order, so no deadlock.
	if limit > 0 {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, deviceLimitLock); err != nil {
			return nil, err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&n); err != nil {
			return nil, err
		}
		if n >= limit {
			return nil, devicelimit.ErrLimitReached
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE enroll_tokens SET uses_left = uses_left - 1 WHERE token=$1`, enrollToken); err != nil {
		return nil, err
	}
	id := newID()
	// Same department rule as CreateAgent: honoured only inside the site.
	if _, err := tx.Exec(ctx,
		`INSERT INTO agents (id, hostname, os, arch, version, token_hash, site_id, group_id, status, last_seen)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,
		         (SELECT g.id FROM device_groups g WHERE g.id=$8 AND g.site_id=$7),
		         'online',now())`,
		id, hostname, os, arch, version, tokenHash, siteID, groupID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, id)
}

// DeviceLimitState is the portal's last answer, or the zero State (no limit)
// when the console never had one.
func (s *Store) DeviceLimitState(ctx context.Context) (devicelimit.State, error) {
	setting, err := s.GetAppSetting(ctx, devicelimit.SettingKey)
	if errors.Is(err, ErrNotFound) {
		return devicelimit.State{}, nil
	}
	if err != nil {
		return devicelimit.State{}, err
	}
	var st devicelimit.State
	if err := json.Unmarshal([]byte(setting.Value), &st); err != nil {
		return devicelimit.State{}, err
	}
	return st, nil
}

// SaveDeviceLimitState records the portal's answer.
func (s *Store) SaveDeviceLimitState(ctx context.Context, st devicelimit.State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.UpsertAppSetting(ctx, devicelimit.SettingKey, string(b), false)
}
