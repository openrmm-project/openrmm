package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

var ErrPatchScanBusy = errors.New("patch scan already pending")

const PatchScanTimeout = 6 * time.Minute
const patchScanStateCols = `request_id, refresh_metadata, status, last_attempt, deadline, last_success, last_metadata_refresh, scan_mode, error_code, consecutive_failures`

func readPatchScanState(row interface{ Scan(...any) error }) (*models.PatchScanState, error) {
	s := &models.PatchScanState{}
	err := row.Scan(&s.RequestID, &s.RefreshMetadata, &s.Status, &s.LastAttempt, &s.Deadline, &s.LastSuccess, &s.LastMetadataRefresh, &s.ScanMode, &s.ErrorCode, &s.ConsecutiveFailures)
	if err != nil {
		return nil, err
	}
	s.Freshness = models.PatchFreshness(s, time.Now())
	return s, nil
}

// ExpirePatchScans releases abandoned requests before polling or scheduling.
func (s *Store) ExpirePatchScans(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_patch_scan_state SET status='timed_out', error_code='timeout', deadline=NULL, consecutive_failures=consecutive_failures+1 WHERE status='pending' AND deadline<=now()`)
	return err
}

func (s *Store) GetPatchScanState(ctx context.Context, agentID string) (*models.PatchScanState, error) {
	if err := s.ExpirePatchScans(ctx); err != nil {
		return nil, err
	}
	state, err := readPatchScanState(s.pool.QueryRow(ctx, `SELECT `+patchScanStateCols+` FROM agent_patch_scan_state WHERE agent_id=$1`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return &models.PatchScanState{Status: "unknown", ScanMode: protocol.PatchScanLegacy, Freshness: "unknown"}, nil
	}
	return state, err
}

// ReservePatchScan atomically claims the only pending slot for this device.
func (s *Store) ReservePatchScan(ctx context.Context, agentID, requestID string, refresh bool) error {
	if err := s.ExpirePatchScans(ctx); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO agent_patch_scan_state(agent_id,request_id,refresh_metadata,status,last_attempt,deadline)
 VALUES($1,$2,$3,'pending',now(),now()+$4::interval)
 ON CONFLICT(agent_id) DO UPDATE SET request_id=EXCLUDED.request_id,refresh_metadata=EXCLUDED.refresh_metadata,
 status='pending',last_attempt=now(),deadline=EXCLUDED.deadline,error_code=''
 WHERE agent_patch_scan_state.status<>'pending'`, agentID, requestID, refresh, PatchScanTimeout.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPatchScanBusy
	}
	return nil
}

// FailPatchScan releases only this request, never a newer reservation.
func (s *Store) FailPatchScan(ctx context.Context, agentID, requestID, code string) error {
	code = protocol.SafePatchScanError(code)
	_, err := s.pool.Exec(ctx, `UPDATE agent_patch_scan_state SET status=CASE WHEN $3='offline' THEN 'offline' ELSE 'failed' END,
 deadline=NULL,error_code=$3,consecutive_failures=consecutive_failures+1
 WHERE agent_id=$1 AND request_id=$2 AND status='pending'`, agentID, requestID, code)
	return err
}

// ReleaseOfflinePatchScan is called only when the current hub lease disconnects.
func (s *Store) ReleaseOfflinePatchScan(ctx context.Context, agentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_patch_scan_state SET status='offline',deadline=NULL,error_code='offline',consecutive_failures=consecutive_failures+1 WHERE agent_id=$1 AND status='pending'`, agentID)
	return err
}
