package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

type CheckResultDisposition string

const (
	CheckResultApplied   CheckResultDisposition = "applied"
	CheckResultDuplicate CheckResultDisposition = "duplicate"
	CheckResultStale     CheckResultDisposition = "stale"
)

// CheckAppliesToAgent is the server-side trust boundary for inbound results.
// A device may only report a currently enabled direct check or one inherited
// through a policy assigned to that device, its site, or its client.
func (s *Store) CheckAppliesToAgent(ctx context.Context, checkID, agentID string) (bool, error) {
	var applies bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM checks c
			JOIN agents a ON a.id=$2
			LEFT JOIN sites si ON si.id=a.site_id
			WHERE c.id=$1 AND c.enabled AND a.revoked_at IS NULL AND (
				c.agent_id=a.id OR c.policy_id IN (
					SELECT pa.policy_id
					FROM policy_assignments pa
					WHERE (pa.scope_type='agent' AND pa.scope_id=a.id)
					   OR (pa.scope_type='group' AND pa.scope_id=a.group_id)
					   OR (pa.scope_type='site' AND pa.scope_id=a.site_id)
					   OR (pa.scope_type='client' AND pa.scope_id=si.client_id)
				)
			)
		)`, checkID, agentID).Scan(&applies)
	return applies, err
}

// CreateCheckExecution records a dispatch before it is put on the websocket.
// The partial unique index makes a scheduler retry return the existing pending
// execution instead of creating concurrent runs for one check/device pair.
func (s *Store) CreateCheckExecution(ctx context.Context, checkID, agentID string, now time.Time, timeout time.Duration) (*models.CheckExecution, bool, error) {
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := newID()
	deadline := now.Add(timeout)
	var e models.CheckExecution
	err = tx.QueryRow(ctx, `
		INSERT INTO check_executions (id, check_id, agent_id, dispatched_at, deadline_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (check_id, agent_id) WHERE status='pending' DO NOTHING
		RETURNING id, check_id, agent_id, status, dispatched_at, deadline_at, completed_at`,
		id, checkID, agentID, now, deadline).Scan(
		&e.ID, &e.CheckID, &e.AgentID, &e.Status, &e.DispatchedAt, &e.DeadlineAt, &e.CompletedAt)
	if err == nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO check_states (check_id, agent_id, last_dispatch_at)
			VALUES ($1,$2,$3)
			ON CONFLICT (check_id, agent_id) DO UPDATE
			SET last_dispatch_at=EXCLUDED.last_dispatch_at`, checkID, agentID, now); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &e, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	err = tx.QueryRow(ctx, `
		SELECT id, check_id, agent_id, status, dispatched_at, deadline_at, completed_at
		FROM check_executions
		WHERE check_id=$1 AND agent_id=$2 AND status='pending'`, checkID, agentID).Scan(
		&e.ID, &e.CheckID, &e.AgentID, &e.Status, &e.DispatchedAt, &e.DeadlineAt, &e.CompletedAt)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &e, false, nil
}

func (s *Store) CancelPendingCheckExecutions(ctx context.Context, checkID string, agentID *string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE check_executions
		SET status='canceled', completed_at=now()
		WHERE check_id=$1 AND status='pending'
		  AND ($2::uuid IS NULL OR agent_id=$2)`, checkID, agentID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) CancelAgentCheckExecutions(ctx context.Context, agentID string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE check_executions SET status='canceled', completed_at=now()
		WHERE agent_id=$1 AND status='pending'`, agentID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ExpireCheckExecutions claims overdue executions exactly once. A timed-out
// execution remains eligible for a late durable result, but it cannot increment
// the failure counter more than once because only pending rows are selected.
func (s *Store) ExpireCheckExecutions(ctx context.Context, now time.Time, limit int) ([]models.CheckExecution, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM check_executions
			WHERE status='pending' AND deadline_at <= $1
			ORDER BY deadline_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE check_executions e
		SET status='timed_out', completed_at=$1
		FROM due WHERE e.id=due.id
		RETURNING e.id, e.check_id, e.agent_id, e.status,
		          e.dispatched_at, e.deadline_at, e.completed_at`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CheckExecution
	for rows.Next() {
		var e models.CheckExecution
		if err := rows.Scan(&e.ID, &e.CheckID, &e.AgentID, &e.Status,
			&e.DispatchedAt, &e.DeadlineAt, &e.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanCheckState(row pgx.Row) (*models.CheckState, error) {
	var st models.CheckState
	err := row.Scan(&st.CheckID, &st.AgentID, &st.Status,
		&st.ConsecutiveFailures, &st.ConsecutiveSuccesses, &st.LastValue,
		&st.LastOutput, &st.LastResultAt, &st.LastDispatchAt, &st.ReconciledAt, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) GetCheckState(ctx context.Context, checkID, agentID string) (*models.CheckState, error) {
	return scanCheckState(s.pool.QueryRow(ctx, `
		SELECT check_id, agent_id, status, consecutive_failures, consecutive_successes,
		       last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at
		FROM check_states WHERE check_id=$1 AND agent_id=$2`, checkID, agentID))
}

// LastCheckActivity is the scheduler cadence anchor. Dispatch time is included
// so a timeout on a five-minute check does not cause an immediate extra run
// merely because no result row was produced.
func (s *Store) LastCheckActivity(ctx context.Context, checkID, agentID string) (time.Time, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT GREATEST(
		(SELECT max(ran_at) FROM check_results WHERE check_id=$1 AND agent_id=$2),
		(SELECT max(dispatched_at) FROM check_executions
		 WHERE check_id=$1 AND agent_id=$2 AND status<>'canceled')
	)`, checkID, agentID).Scan(&at)
	if err != nil || at == nil {
		return time.Time{}, err
	}
	return *at, nil
}

// RecordCheckResult atomically deduplicates an execution result, closes the
// dispatch, stores history, and advances consecutive success/failure counters.
func (s *Store) RecordCheckResult(ctx context.Context, r *models.CheckResult) (*models.CheckState, CheckResultDisposition, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var dispatchedAt *time.Time
	if r.ExecutionID != nil && *r.ExecutionID != "" {
		var checkID, agentID, status string
		var dispatched time.Time
		err = tx.QueryRow(ctx, `SELECT check_id, agent_id, status, dispatched_at
			FROM check_executions WHERE id=$1 FOR UPDATE`, *r.ExecutionID).
			Scan(&checkID, &agentID, &status, &dispatched)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		if checkID != r.CheckID || agentID != r.AgentID {
			return nil, "", fmt.Errorf("%w: execution does not belong to check/device", ErrConflict)
		}
		dispatchedAt = &dispatched
		if status == models.CheckExecutionCompleted || status == models.CheckExecutionCanceled {
			st, stateErr := scanCheckState(tx.QueryRow(ctx, `
				SELECT check_id, agent_id, status, consecutive_failures, consecutive_successes,
				       last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at
				FROM check_states WHERE check_id=$1 AND agent_id=$2`, r.CheckID, r.AgentID))
			if stateErr != nil && !errors.Is(stateErr, ErrNotFound) {
				return nil, "", stateErr
			}
			if st == nil {
				st = &models.CheckState{CheckID: r.CheckID, AgentID: r.AgentID, Status: models.CheckUnknown}
			}
			return st, CheckResultDuplicate, nil
		}
	} else {
		// Legacy agents do not echo execution ids. Correlate to the newest pending
		// dispatch when possible, while retaining compatibility during rollout.
		var executionID string
		var dispatched time.Time
		err = tx.QueryRow(ctx, `SELECT id, dispatched_at FROM check_executions
			WHERE check_id=$1 AND agent_id=$2 AND status='pending'
			ORDER BY dispatched_at DESC LIMIT 1 FOR UPDATE`, r.CheckID, r.AgentID).
			Scan(&executionID, &dispatched)
		if err == nil {
			r.ExecutionID = &executionID
			dispatchedAt = &dispatched
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", err
		}
	}

	r.ID = newID()
	var ranAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO check_results (id, check_id, agent_id, execution_id, status, value, output)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (execution_id) WHERE execution_id IS NOT NULL DO NOTHING
		RETURNING ran_at`, r.ID, r.CheckID, r.AgentID, r.ExecutionID, r.Status, r.Value, r.Output).Scan(&ranAt)
	if errors.Is(err, pgx.ErrNoRows) {
		st, stateErr := scanCheckState(tx.QueryRow(ctx, `
			SELECT check_id, agent_id, status, consecutive_failures, consecutive_successes,
			       last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at
			FROM check_states WHERE check_id=$1 AND agent_id=$2`, r.CheckID, r.AgentID))
		if stateErr != nil {
			return nil, "", stateErr
		}
		return st, CheckResultDuplicate, nil
	}
	if err != nil {
		return nil, "", err
	}
	r.RanAt = ranAt
	if r.ExecutionID != nil {
		if _, err = tx.Exec(ctx, `UPDATE check_executions
			SET status='completed', completed_at=$2 WHERE id=$1`, *r.ExecutionID, ranAt); err != nil {
			return nil, "", err
		}
	}

	// Dispatch order, rather than receive time, is authoritative. A delayed
	// result remains in history and closes its execution, but may not roll the
	// current state back after a newer run was dispatched.
	if dispatchedAt != nil {
		var lastDispatch *time.Time
		err = tx.QueryRow(ctx, `SELECT last_dispatch_at FROM check_states
			WHERE check_id=$1 AND agent_id=$2`, r.CheckID, r.AgentID).Scan(&lastDispatch)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", err
		}
		if lastDispatch != nil && lastDispatch.After(*dispatchedAt) {
			st, stateErr := scanCheckState(tx.QueryRow(ctx, `
				SELECT check_id, agent_id, status, consecutive_failures, consecutive_successes,
				       last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at
				FROM check_states WHERE check_id=$1 AND agent_id=$2`, r.CheckID, r.AgentID))
			if stateErr != nil {
				return nil, "", stateErr
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, "", err
			}
			return st, CheckResultStale, nil
		}
	}

	st, err := scanCheckState(tx.QueryRow(ctx, `
		INSERT INTO check_states (
			check_id, agent_id, status, consecutive_failures, consecutive_successes,
			last_value, last_output, last_result_at, updated_at
		) VALUES (
			$1,$2,$3,
			CASE WHEN $3 IN ('warning','failing') THEN 1 ELSE 0 END,
			CASE WHEN $3='passing' THEN 1 ELSE 0 END,
			$4,$5,$6,$6
		)
		ON CONFLICT (check_id, agent_id) DO UPDATE SET
			status=EXCLUDED.status,
			consecutive_failures=CASE
				WHEN EXCLUDED.status IN ('warning','failing')
				THEN check_states.consecutive_failures + 1 ELSE 0 END,
			consecutive_successes=CASE
				WHEN EXCLUDED.status='passing'
				THEN check_states.consecutive_successes + 1 ELSE 0 END,
			last_value=EXCLUDED.last_value,
			last_output=EXCLUDED.last_output,
			last_result_at=EXCLUDED.last_result_at,
			reconciled_at=NULL,
			updated_at=EXCLUDED.updated_at
		RETURNING check_id, agent_id, status, consecutive_failures, consecutive_successes,
		          last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at`,
		r.CheckID, r.AgentID, r.Status, r.Value, r.Output, ranAt))
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return st, CheckResultApplied, nil
}

// RecordCheckTimeout advances the same state machine as an explicit failure,
// without fabricating a check_results row that would look like endpoint data.
// Locking the execution fences a result racing the timeout: whichever commits
// first is authoritative, and a completed/newer execution is never overwritten.
func (s *Store) RecordCheckTimeout(ctx context.Context, e *models.CheckExecution, output string) (*models.CheckState, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var executionStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM check_executions WHERE id=$1 FOR UPDATE`, e.ID).
		Scan(&executionStatus); errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrNotFound
	} else if err != nil {
		return nil, false, err
	}
	if executionStatus != models.CheckExecutionTimedOut {
		st, err := scanCheckState(tx.QueryRow(ctx, `
			SELECT check_id, agent_id, status, consecutive_failures, consecutive_successes,
			       last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at
			FROM check_states WHERE check_id=$1 AND agent_id=$2`, e.CheckID, e.AgentID))
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return st, false, nil
	}
	var lastDispatch *time.Time
	err = tx.QueryRow(ctx, `SELECT last_dispatch_at FROM check_states
		WHERE check_id=$1 AND agent_id=$2`, e.CheckID, e.AgentID).Scan(&lastDispatch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	if lastDispatch != nil && lastDispatch.After(e.DispatchedAt) {
		st, err := scanCheckState(tx.QueryRow(ctx, `
			SELECT check_id, agent_id, status, consecutive_failures, consecutive_successes,
			       last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at
			FROM check_states WHERE check_id=$1 AND agent_id=$2`, e.CheckID, e.AgentID))
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return st, false, nil
	}
	now := time.Now()
	st, err := scanCheckState(tx.QueryRow(ctx, `
		INSERT INTO check_states (
			check_id, agent_id, status, consecutive_failures, consecutive_successes,
			last_output, last_result_at, last_dispatch_at, updated_at
		) VALUES ($1,$2,'failing',1,0,$3,$4,$5,$4)
		ON CONFLICT (check_id, agent_id) DO UPDATE SET
			status='failing',
			consecutive_failures=check_states.consecutive_failures + 1,
			consecutive_successes=0,
			last_output=EXCLUDED.last_output,
			last_result_at=EXCLUDED.last_result_at,
			reconciled_at=NULL,
			updated_at=EXCLUDED.updated_at
		RETURNING check_id, agent_id, status, consecutive_failures, consecutive_successes,
		          last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at`,
		e.CheckID, e.AgentID, output, now, e.DispatchedAt))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return st, true, nil
}

func (s *Store) MarkCheckUnknown(ctx context.Context, checkID, agentID, output string, at time.Time) (*models.CheckState, error) {
	if at.IsZero() {
		at = time.Now()
	}
	return scanCheckState(s.pool.QueryRow(ctx, `
		INSERT INTO check_states (
			check_id, agent_id, status, consecutive_failures, consecutive_successes,
			last_output, updated_at
		) VALUES ($1,$2,'unknown',0,0,$3,$4)
		ON CONFLICT (check_id, agent_id) DO UPDATE SET
			status='unknown', consecutive_failures=0, consecutive_successes=0,
			last_output=EXCLUDED.last_output, reconciled_at=NULL, updated_at=EXCLUDED.updated_at
		RETURNING check_id, agent_id, status, consecutive_failures, consecutive_successes,
		          last_value, last_output, last_result_at, last_dispatch_at, reconciled_at, updated_at`,
		checkID, agentID, output, at))
}

func (s *Store) ListUnreconciledCheckStates(ctx context.Context, limit int) ([]models.CheckState, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT check_id, agent_id, status,
		consecutive_failures, consecutive_successes, last_value, last_output,
		last_result_at, last_dispatch_at, reconciled_at, updated_at
		FROM check_states
		WHERE reconciled_at IS NULL OR reconciled_at < updated_at
		ORDER BY updated_at, check_id, agent_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CheckState
	for rows.Next() {
		st, err := scanCheckState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// TryMarkCheckStateReconciled is the optimistic completion fence for alert
// reconciliation. false means a newer result replaced the state while the
// caller was applying alert side effects and that newer state must be applied
// again before reconciliation may be considered complete.
func (s *Store) TryMarkCheckStateReconciled(ctx context.Context, checkID, agentID string, stateUpdatedAt time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE check_states SET reconciled_at=now()
		WHERE check_id=$1 AND agent_id=$2 AND updated_at=$3`, checkID, agentID, stateUpdatedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CheckInstances returns every effective target of a direct or policy check.
// Missing state is explicit "unknown" rather than being reported as healthy.
func (s *Store) CheckInstances(ctx context.Context, checkID, userID string, scoped bool) ([]models.CheckInstance, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.hostname, COALESCE(cs.status, 'unknown'), cs.last_value,
		       COALESCE(cs.last_output, ''), cs.last_result_at
		FROM checks c
		JOIN agents a ON (
			c.agent_id=a.id OR c.policy_id IN (
				SELECT pa.policy_id
				FROM policy_assignments pa
				LEFT JOIN sites asi ON asi.id=a.site_id
				WHERE (pa.scope_type='agent' AND pa.scope_id=a.id)
				   OR (pa.scope_type='group' AND pa.scope_id=a.group_id)
				   OR (pa.scope_type='site' AND pa.scope_id=a.site_id)
				   OR (pa.scope_type='client' AND pa.scope_id=asi.client_id)
			)
		)
		LEFT JOIN check_states cs ON cs.check_id=c.id AND cs.agent_id=a.id
		WHERE c.id=$1 AND a.revoked_at IS NULL AND (
			NOT $3 OR NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=NULLIF($2,'')::uuid)
			OR EXISTS (
				SELECT 1 FROM user_scopes us
				WHERE us.user_id=NULLIF($2,'')::uuid AND (
					(us.scope_type='agent' AND us.scope_id=a.id) OR
					(us.scope_type='group' AND us.scope_id=a.group_id) OR
					(us.scope_type='site' AND us.scope_id=a.site_id) OR
					(us.scope_type='client' AND us.scope_id=(SELECT client_id FROM sites WHERE id=a.site_id))
				)
			)
		)
		ORDER BY a.hostname, a.id`, checkID, userID, scoped)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CheckInstance
	for rows.Next() {
		var in models.CheckInstance
		if err := rows.Scan(&in.AgentID, &in.Hostname, &in.Status, &in.Value,
			&in.Output, &in.LastResultAt); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
