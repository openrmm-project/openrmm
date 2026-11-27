package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/p1ngu/rmm/internal/models"
)

const operationsColumns = `purpose, applications, dependencies, criticality, maintenance_window, maintenance_timezone, backup_notes, backup_verified_on::text, version, updated_at, updated_by`

func scanOperations(row pgx.Row) (*models.DeviceOperationsProfile, error) {
	var p models.DeviceOperationsProfile
	err := row.Scan(&p.Purpose, &p.Applications, &p.Dependencies, &p.Criticality, &p.MaintenanceWindow, &p.MaintenanceTimezone, &p.BackupNotes, &p.BackupVerifiedOn, &p.Version, &p.UpdatedAt, &p.UpdatedBy)
	return &p, err
}

func (s *Store) GetDeviceOperationsProfile(ctx context.Context, agentID string) (*models.DeviceOperationsProfile, error) {
	p, err := scanOperations(s.pool.QueryRow(ctx, `SELECT `+operationsColumns+` FROM device_operations_profiles WHERE agent_id=$1`, agentID))
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1)`, agentID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	defaults := models.DefaultDeviceOperationsProfile()
	return &defaults, nil
}

// UpdateDeviceOperationsProfile commits the optimistic write and its audit together.
func (s *Store) UpdateDeviceOperationsProfile(ctx context.Context, agentID, actor string, expected int64, fields models.DeviceOperationsFields) (*models.DeviceOperationsProfile, error) {
	if expected < 0 {
		return nil, errors.New("expected_version must be nonnegative")
	}
	if err := fields.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Serializes profile writes and deletion; the hostname is captured under this lock.
	var hostname string
	err = tx.QueryRow(ctx, `SELECT hostname FROM agents WHERE id=$1 FOR UPDATE`, agentID).Scan(&hostname)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	previous, err := scanOperations(tx.QueryRow(ctx, `SELECT `+operationsColumns+` FROM device_operations_profiles WHERE agent_id=$1`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		p := models.DefaultDeviceOperationsProfile()
		previous = &p
	} else if err != nil {
		return nil, err
	}
	args := []any{agentID, fields.Purpose, fields.Applications, fields.Dependencies, fields.Criticality, fields.MaintenanceWindow, fields.MaintenanceTimezone, fields.BackupNotes, fields.BackupVerifiedOn, actor}
	var row pgx.Row
	if expected == 0 {
		row = tx.QueryRow(ctx, `INSERT INTO device_operations_profiles (agent_id,purpose,applications,dependencies,criticality,maintenance_window,maintenance_timezone,backup_notes,backup_verified_on,updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING RETURNING `+operationsColumns, args...)
	} else {
		args = append(args, expected)
		row = tx.QueryRow(ctx, `UPDATE device_operations_profiles SET purpose=$2,applications=$3,dependencies=$4,criticality=$5,maintenance_window=$6,maintenance_timezone=$7,backup_notes=$8,backup_verified_on=$9,updated_by=$10,version=version+1,updated_at=clock_timestamp() WHERE agent_id=$1 AND version=$11 RETURNING `+operationsColumns, args...)
	}
	result, err := scanOperations(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	changed := []string{}
	oldVal, newVal := reflect.ValueOf(previous.DeviceOperationsFields), reflect.ValueOf(fields)
	typ := oldVal.Type()
	for i := 0; i < oldVal.NumField(); i++ {
		if !reflect.DeepEqual(oldVal.Field(i).Interface(), newVal.Field(i).Interface()) {
			changed = append(changed, typ.Field(i).Tag.Get("json"))
		}
	}
	detail, _ := json.Marshal(map[string]any{"changed_fields": changed, "version": result.Version, "hostname": hostname})
	if err := s.addAuditTx(ctx, tx, actor, "device.operational_profile.update", agentID, detail); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
