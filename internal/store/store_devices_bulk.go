package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// MaxBulkDevices bounds one bulk move or delete. The database holds proposals
// to the same limit, so a request the API accepts can always be stored.
const MaxBulkDevices = 500

// SiteNotEmptyError refuses to delete a site that still has devices. Deleting
// it would leave them unassigned — out of every client, report and scope that
// named that site — which is rarely what the person meant, and not visible
// from the dialog that asked.
type SiteNotEmptyError struct{ Devices int }

func (e *SiteNotEmptyError) Error() string {
	if e.Devices == 1 {
		return "the site still has 1 device; move or delete it before deleting the site"
	}
	return fmt.Sprintf("the site still has %d devices; move or delete them before deleting the site", e.Devices)
}

func (e *SiteNotEmptyError) Is(target error) bool { return target == ErrConflict }

// deleteSiteEnrollTokens removes the enrollment tokens that place new devices
// in the given sites. They have nowhere left to place them, and a token bound
// to one of the site's departments could not even survive the delete: the
// cascade clears its site before its department, and the trigger from
// migration 010 rejects a department without a site. That rejection was the
// "internal server error" an operator saw when deleting a site.
func deleteSiteEnrollTokens(ctx context.Context, tx pgx.Tx, siteFilter string, arg any) error {
	_, err := tx.Exec(ctx, `DELETE FROM enroll_tokens WHERE site_id IN (`+siteFilter+`)`, arg)
	return err
}

// DeleteSite removes a site that has no devices, together with its
// departments and the enrollment tokens that point at it.
func (s *Store) DeleteSite(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Locked first: enrolling or moving a device into the site takes a key
	// share on this row, so none can arrive between the count and the delete.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM sites WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var devices int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents WHERE site_id=$1`, id).Scan(&devices); err != nil {
		return err
	}
	if devices > 0 {
		return &SiteNotEmptyError{Devices: devices}
	}
	if err := deleteSiteEnrollTokens(ctx, tx, `$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sites WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteClient removes a client with its sites and departments. Its devices
// become unassigned, which the console says before asking. Enrollment tokens
// for its sites go first, for the reason given at deleteSiteEnrollTokens.
func (s *Store) DeleteClient(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := deleteSiteEnrollTokens(ctx, tx, `SELECT id FROM sites WHERE client_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM clients WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CountSiteAgents is what the console shows before offering to delete a site.
func (s *Store) CountSiteAgents(ctx context.Context, siteID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE site_id=$1`, siteID).Scan(&n)
	return n, err
}

// MoveAgents puts every listed device in siteID and, when groupID is given, in
// that department. All or none: a list with one unknown id moves nothing, so a
// partially applied move never has to be untangled by hand.
//
// Without a department each device keeps the one it has when that department
// belongs to the destination, and loses it otherwise — the rule SetAgentSite
// follows for a single device.
func (s *Store) MoveAgents(ctx context.Context, ids []string, siteID string, groupID *string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if groupID != nil && *groupID != "" {
		var groupSite *string
		err := tx.QueryRow(ctx, `SELECT site_id FROM device_groups WHERE id=$1`, *groupID).Scan(&groupSite)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if groupSite == nil || *groupSite != siteID {
			return fmt.Errorf("%w: the department belongs to another site", ErrConflict)
		}
	} else {
		groupID = nil
	}
	ct, err := tx.Exec(ctx, `UPDATE agents AS a SET site_id=$2,
		group_id = CASE
		  WHEN $3::uuid IS NOT NULL THEN $3::uuid
		  WHEN EXISTS (SELECT 1 FROM device_groups g WHERE g.id=a.group_id AND g.site_id=$2) THEN a.group_id
		  ELSE NULL
		END
		WHERE a.id = ANY($1::uuid[])`, ids, siteID, groupID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() != int64(len(ids)) {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

const deviceOpCols = `id, kind, agent_ids::text[], site_id, group_id, description, status, proposed_by, decided_by, result, created_at, decided_at`

func scanDeviceOp(row pgx.Row) (*models.DeviceOperation, error) {
	var op models.DeviceOperation
	var result []byte
	err := row.Scan(&op.ID, &op.Kind, &op.AgentIDs, &op.SiteID, &op.GroupID, &op.Description, &op.Status,
		&op.ProposedBy, &op.DecidedBy, &result, &op.CreatedAt, &op.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	op.Result = json.RawMessage(result)
	return &op, nil
}

// CreateDeviceOperation records a proposal. It changes no device.
func (s *Store) CreateDeviceOperation(ctx context.Context, op *models.DeviceOperation) (*models.DeviceOperation, error) {
	return scanDeviceOp(s.pool.QueryRow(ctx, `INSERT INTO device_operations
		(id, kind, agent_ids, site_id, group_id, description, proposed_by)
		VALUES ($1, $2, $3::uuid[], $4, $5, $6, $7) RETURNING `+deviceOpCols,
		newID(), op.Kind, op.AgentIDs, op.SiteID, op.GroupID, op.Description, op.ProposedBy))
}

// GetDeviceOperation fetches one proposal.
func (s *Store) GetDeviceOperation(ctx context.Context, id string) (*models.DeviceOperation, error) {
	return scanDeviceOp(s.pool.QueryRow(ctx, `SELECT `+deviceOpCols+` FROM device_operations WHERE id=$1`, id))
}

// ListDeviceOperations returns the proposals still waiting, then the most
// recent decided ones, newest first within each.
func (s *Store) ListDeviceOperations(ctx context.Context, limit int) ([]models.DeviceOperation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+deviceOpCols+` FROM device_operations
		ORDER BY (status = 'awaiting_approval') DESC, created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.DeviceOperation{}
	for rows.Next() {
		op, err := scanDeviceOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *op)
	}
	return out, rows.Err()
}

// ClaimDeviceOperation moves a proposal from waiting to approved for exactly
// one caller. Two people approving at once must not carry it out twice: the
// second finds it no longer waiting and gets ErrConflict.
func (s *Store) ClaimDeviceOperation(ctx context.Context, id, decidedBy string) (*models.DeviceOperation, error) {
	op, err := scanDeviceOp(s.pool.QueryRow(ctx, `UPDATE device_operations
		SET status='approved', decided_by=$2, decided_at=now()
		WHERE id=$1 AND status='awaiting_approval' RETURNING `+deviceOpCols, id, decidedBy))
	if errors.Is(err, ErrNotFound) {
		if _, getErr := s.GetDeviceOperation(ctx, id); getErr == nil {
			return nil, ErrConflict
		}
	}
	return op, err
}

// FinishDeviceOperation records how an approved proposal ended.
func (s *Store) FinishDeviceOperation(ctx context.Context, id, status string, result any) error {
	if status != "completed" && status != "failed" {
		return fmt.Errorf("invalid final status %q", status)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE device_operations SET status=$2, result=$3 WHERE id=$1 AND status='approved'`, id, status, body)
	return err
}

// RejectDeviceOperation discards a proposal that is still waiting.
func (s *Store) RejectDeviceOperation(ctx context.Context, id, decidedBy string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE device_operations
		SET status='rejected', decided_by=$2, decided_at=$3
		WHERE id=$1 AND status='awaiting_approval'`, id, decidedBy, time.Now())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		if _, getErr := s.GetDeviceOperation(ctx, id); getErr != nil {
			return getErr
		}
		return ErrConflict
	}
	return nil
}

// AgentHostnames returns the hostname of each listed device that exists. It is
// both the existence check for a bulk request and the label its audit entries
// keep after the devices are gone.
func (s *Store) AgentHostnames(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, hostname FROM agents WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id, host string
		if err := rows.Scan(&id, &host); err != nil {
			return nil, err
		}
		out[id] = host
	}
	return out, rows.Err()
}
