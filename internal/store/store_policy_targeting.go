package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/p1ngu/rmm/internal/models"
)

// Policy reach (docs/10, RFC #258).
//
// Whether a policy reaches a device used to be spelled out in each query that
// needed it — effective tasks and checks, the check scheduler, "run now", the
// devices a check covers, the server rule governing a device, the sweep that
// closes inapplicable alerts — and every change (the department scope, the
// on/off switch) had to be made in all of them. These fragments are the one
// definition: an assignment reaches a device when its scope covers it and the
// device matches its OS and role filters; a policy reaches a device when it is
// switched on, one of its assignments reaches it, and it does not exclude it.

// deviceOSSQL is a device's OS in the filter vocabulary (darwin is macos).
func deviceOSSQL(a string) string {
	return `(CASE ` + a + `.os WHEN 'darwin' THEN 'macos' ELSE ` + a + `.os END)`
}

// deviceRoleSQL is a device's role in effect: the override when set.
func deviceRoleSQL(a string) string {
	return `COALESCE(NULLIF(` + a + `.role_override, ''), ` + a + `.os_role)`
}

// assignmentReachesSQL is true when assignment pa (of either assignment
// table) reaches device a.
func assignmentReachesSQL(pa, a string) string {
	return `((` + pa + `.scope_type = 'global')
	   OR (` + pa + `.scope_type = 'agent'  AND ` + pa + `.scope_id = ` + a + `.id)
	   OR (` + pa + `.scope_type = 'group'  AND ` + pa + `.scope_id = ` + a + `.group_id)
	   OR (` + pa + `.scope_type = 'site'   AND ` + pa + `.scope_id = ` + a + `.site_id)
	   OR (` + pa + `.scope_type = 'client' AND ` + pa + `.scope_id = (SELECT rs.client_id FROM sites rs WHERE rs.id = ` + a + `.site_id)))
	  AND (` + pa + `.os = '' OR ` + pa + `.os = ` + deviceOSSQL(a) + `)
	  AND (` + pa + `.role = '' OR ` + pa + `.role = ` + deviceRoleSQL(a) + `)`
}

// policiesReachingSQL selects the ids of the switched-on policies that reach
// device a and do not exclude it.
func policiesReachingSQL(a string) string {
	return `(SELECT rpa.policy_id FROM policy_assignments rpa
	  WHERE rpa.policy_id IN ` + activePoliciesSQL + ` AND ` + assignmentReachesSQL("rpa", a) + `
	    AND NOT EXISTS (SELECT 1 FROM policy_exclusions rpe WHERE rpe.policy_id = rpa.policy_id AND rpe.agent_id = ` + a + `.id))`
}

// patchPolicyReachesSQL is true when patch policy pp reaches device a (its
// switch is checked by the caller).
func patchPolicyReachesSQL(pp, a string) string {
	return `(EXISTS (SELECT 1 FROM patch_policy_assignments ppa WHERE ppa.policy_id = ` + pp + `.id AND ` + assignmentReachesSQL("ppa", a) + `)
	  AND NOT EXISTS (SELECT 1 FROM patch_policy_exclusions ppe WHERE ppe.policy_id = ` + pp + `.id AND ppe.agent_id = ` + a + `.id))`
}

// ErrInvalidTarget refuses an unknown OS or role filter, or a role override.
var ErrInvalidTarget = errors.New("os must be windows, macos or linux and role workstation or server (or empty for any)")

// PolicyExclusion is models.PolicyExclusion.
type PolicyExclusion = models.PolicyExclusion

// exclusionTable names the exclusion table of a policy kind; it is never
// built from input.
func exclusionTable(patch bool) string {
	if patch {
		return "patch_policy_exclusions"
	}
	return "policy_exclusions"
}

// ListExclusions returns the devices a policy (or patch policy) excludes.
func (s *Store) ListExclusions(ctx context.Context, patch bool, policyID string) ([]PolicyExclusion, error) {
	return listExclusions(ctx, s.pool, patch, policyID)
}

func listExclusions(ctx context.Context, q dbQuerier, patch bool, policyID string) ([]PolicyExclusion, error) {
	rows, err := q.Query(ctx, `SELECT e.agent_id::text, COALESCE(a.hostname, ''), e.created_by, to_char(e.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM `+exclusionTable(patch)+` e LEFT JOIN agents a ON a.id = e.agent_id
		WHERE e.policy_id = $1 ORDER BY a.hostname, e.agent_id`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PolicyExclusion{}
	for rows.Next() {
		var e PolicyExclusion
		if err := rows.Scan(&e.AgentID, &e.Hostname, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddExclusion leaves a device out of a policy (or patch policy). Excluding a
// device twice is not an error.
func (s *Store) AddExclusion(ctx context.Context, patch bool, policyID, agentID, actor string) error {
	return addExclusion(ctx, s.pool, patch, policyID, agentID, actor)
}

func addExclusion(ctx context.Context, q dbQuerier, patch bool, policyID, agentID, actor string) error {
	_, err := q.Exec(ctx, `INSERT INTO `+exclusionTable(patch)+` (policy_id, agent_id, created_by) VALUES ($1,$2,$3)
		ON CONFLICT DO NOTHING`, policyID, agentID, actor)
	return err
}

// RemoveExclusion puts a device back in a policy's reach.
func (s *Store) RemoveExclusion(ctx context.Context, patch bool, policyID, agentID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM `+exclusionTable(patch)+` WHERE policy_id=$1 AND agent_id=$2`, policyID, agentID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetAgentRoleOverride sets (or, with an empty role, clears) an
// administrator's correction of a device's role.
func (s *Store) SetAgentRoleOverride(ctx context.Context, agentID, role string) (*models.Agent, error) {
	if !models.ValidTargetFilter("", role) {
		return nil, ErrInvalidTarget
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agents SET role_override=$2 WHERE id=$1`, agentID, role)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetAgent(ctx, agentID)
}

// detectedRoleFromInventory derives the role an inventory snapshot implies.
// ok is false when the snapshot says nothing about the OS.
func detectedRoleFromInventory(data []byte) (role string, ok bool) {
	var inv struct {
		OS             string `json:"os"`
		Platform       string `json:"platform"`
		PlatformFamily string `json:"platform_family"`
	}
	if err := json.Unmarshal(data, &inv); err != nil || inv.OS == "" {
		return "", false
	}
	return models.DetectRole(inv.OS, inv.Platform, inv.PlatformFamily), true
}

// validateAssignmentTarget checks an assignment's filters and global scope.
func validateAssignmentTarget(scopeType, scopeID, os, role string) error {
	if !models.ValidTargetFilter(os, role) {
		return ErrInvalidTarget
	}
	if scopeType == models.ScopeGlobal && scopeID != models.GlobalScopeID {
		return fmt.Errorf("a global assignment has scope id %s", models.GlobalScopeID)
	}
	return nil
}
