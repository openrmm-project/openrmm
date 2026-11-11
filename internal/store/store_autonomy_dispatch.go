package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/p1ngu/rmm/internal/autonomy"
	"github.com/p1ngu/rmm/internal/models"
)

// WithAutonomousDispatch makes the final, nonblocking send the authorization
// point. A pause or release withdrawal that finishes before it wins. The shared
// locks are held through send, so a withdrawal cannot finish and then have an
// older snapshot dispatched. No database or network wait belongs in send.
func (s *Store) WithAutonomousDispatch(ctx context.Context, j *models.Job, releaseHash string, send func() bool, scopeRevision ...time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Reuse this transaction; nesting a pool-backed guard can exhaust the pool.
	if len(scopeRevision) > 0 && !scopeRevision[0].IsZero() {
		if err := lockScriptVariableScope(ctx, tx, j.AgentID, scopeRevision[0]); err != nil {
			return false, err
		}
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('autonomy:pause'))`); err != nil {
		return false, err
	}
	var paused string
	err = tx.QueryRow(ctx, `SELECT value FROM app_settings WHERE key='ai.autonomy_paused'`).Scan(&paused)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if paused != "" && paused != "false" {
		return false, fmt.Errorf("AI autonomy is paused")
	}
	if j.Autonomy == models.AutonomyPreapproved {
		if j.ScriptID == nil {
			return false, fmt.Errorf("missing released script")
		}
		sc, err := scanScript(tx.QueryRow(ctx, `SELECT `+scriptCols+` FROM scripts WHERE id=$1 FOR SHARE`, *j.ScriptID))
		if err != nil {
			return false, err
		}
		if err := validateAutonomousRelease(sc, j, releaseHash); err != nil {
			return false, err
		}
	} else if j.Autonomy != models.AutonomyQuery {
		return false, fmt.Errorf("unknown autonomy")
	}
	return send(), nil
}

func validateAutonomousRelease(sc *models.Script, j *models.Job, releaseHash string) error {
	if !sc.AIReleaseCurrent() || sc.AIReleasedHash != releaseHash || sc.Shell != j.Shell || sc.Body != j.Script || !slices.Equal(sc.Variables, j.Variables) {
		return fmt.Errorf("the script release changed before dispatch")
	}
	if err := autonomy.CheckRelease(sc); err != nil {
		return err
	}
	values := make(map[string]any, len(j.Params))
	for k, v := range j.Params {
		values[k] = v
	}
	_, err := autonomy.ValidateParamValues(sc.Params, values)
	return err
}
