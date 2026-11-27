package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/protocol"
)

// ObserveReboot changes pending only on a conclusive OS observation. Unknown
// retains the last known pending flag/since, while recording the failed check.
func (s *Store) ObserveReboot(ctx context.Context, id string, report *protocol.RebootStatus) error {
	if report == nil {
		return nil
	}
	if !report.Valid() {
		return errors.New("invalid reboot observation")
	}
	r := report.Clean()
	_, err := s.pool.Exec(ctx, `UPDATE agents SET
 reboot_status=$2, reboot_reason=$3, reboot_checked_at=now(),
 reboot_observed_at=CASE WHEN reboot_boot_id<>'' AND $4<>'' AND reboot_boot_id<>$4 THEN now() ELSE reboot_observed_at END,
 reboot_boot_id=CASE WHEN $4<>'' THEN $4 ELSE reboot_boot_id END,
 reboot_patch_required=CASE WHEN $2='not_required' THEN FALSE ELSE reboot_patch_required END,
 reboot_pending=CASE WHEN $2='unknown' THEN reboot_pending ELSE $2='required' END,
 reboot_pending_since=CASE WHEN $2='unknown' THEN reboot_pending_since WHEN $2='required' THEN COALESCE(reboot_pending_since,now()) ELSE NULL END
 WHERE id=$1 AND revoked_at IS NULL`, id, r.Status, r.Reason, r.BootID)
	return err
}

// ClaimRebootRequest gives one dispatcher a 15-minute reservation, across
// scheduler ticks and concurrent human requests. A failed send releases it.
func (s *Store) ClaimRebootRequest(ctx context.Context, id string) (string, error) {
	return s.claimRebootRequest(ctx, id, false)
}

// ClaimAutomaticRebootRequest permits only one delivered automatic request per
// pending OS episode or distinct patch-install result, even after the manual
// retry cooldown has elapsed.
func (s *Store) ClaimAutomaticRebootRequest(ctx context.Context, id string) (string, error) {
	return s.claimRebootRequest(ctx, id, true)
}

func (s *Store) claimRebootRequest(ctx context.Context, id string, automatic bool) (string, error) {
	token := uuid.NewString()
	result, err := s.pool.Exec(ctx, `UPDATE agents SET reboot_request_previous_at=reboot_requested_at,reboot_requested_at=now(),reboot_request_id=$2
 WHERE id=$1 AND revoked_at IS NULL AND (reboot_requested_at IS NULL OR reboot_requested_at<now()-interval '15 minutes')
 AND (NOT $3 OR (reboot_pending AND (reboot_patch_required OR (reboot_status='required' AND reboot_checked_at>now()-interval '2 minutes')) AND (reboot_requested_at IS NULL OR reboot_requested_at<GREATEST(reboot_pending_since,reboot_patch_required_at))))`, id, token, automatic)
	if err != nil {
		return "", err
	}
	if result.RowsAffected() == 0 {
		return "", ErrConflict
	}
	return token, nil
}

func (s *Store) ReleaseRebootRequest(ctx context.Context, id, token string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET reboot_requested_at=reboot_request_previous_at,reboot_request_previous_at=NULL,reboot_request_id='' WHERE id=$1 AND reboot_request_id=$2`, id, token)
	return err
}

// Automatic reboot accepts a fresh positive OS observation or a patch result.
// Neither source permits repeated requests for the same observation/result.
func (s *Store) RebootEligibleAgents(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM agents WHERE revoked_at IS NULL AND reboot_pending AND (reboot_patch_required OR (reboot_status='required' AND reboot_checked_at>now()-interval '2 minutes'))
 AND (reboot_requested_at IS NULL OR reboot_requested_at<GREATEST(reboot_pending_since,reboot_patch_required_at))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
