package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/p1ngu/rmm/internal/models"
)

// Policy activation (docs/10, docs/16).
//
// A policy has its own switch. Switched off it applies nothing: every query that
// decides what reaches a device — effective tasks and checks, server alert
// rules, the devices a check covers, "run now", and the sweep that closes
// alerts of checks that stopped applying — selects only policies in
// activePoliciesSQL. AI Chat and MCP propose a policy complete with its scopes,
// tasks and checks, all of it switched off, and a person switches it on in one
// step after reading all of it. The database refuses to switch on an AI
// proposal nobody reviewed (policies_proposal_needs_review), as it already did
// for tasks and checks, because the REST API is a second way into those rows.

// activePoliciesSQL selects the policies that are switched on.
const activePoliciesSQL = `(SELECT id FROM policies WHERE enabled)`

// dbQuerier is what a pool and a transaction have in common, so one insert or
// read can serve both.
type dbQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrReviewStale refuses an activation whose review no longer matches: the
// policy changed after the person opened it, so they have not seen what the
// click would switch on.
var ErrReviewStale = fmt.Errorf("%w: it changed after this review was opened; review it again", ErrConflict)

// PolicyProposal is a policy AI Chat or MCP proposes in one piece.
type PolicyProposal struct {
	Name, Description, ProposedBy string
	Assignments                   []models.PolicyAssignment
	Tasks                         []models.Task
	Checks                        []models.Check
	// ExcludedAgents are devices the policy leaves out even where an
	// assignment reaches them.
	ExcludedAgents []string
}

// CreatePolicyProposal writes the policy, its scopes, tasks and checks in one
// transaction, all of it switched off and attributed to the proposer, and
// returns the review a person reads before switching it on.
func (s *Store) CreatePolicyProposal(ctx context.Context, p *PolicyProposal) (*models.PolicyReview, error) {
	if p.ProposedBy == "" {
		return nil, errors.New("a policy proposal needs its proposer")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	pol, err := scanPolicy(tx.QueryRow(ctx,
		`INSERT INTO policies (id, name, description, proposed_by, enabled) VALUES ($1,$2,$3,$4,false) RETURNING `+policyCols,
		newID(), p.Name, p.Description, p.ProposedBy))
	if err != nil {
		return nil, err
	}
	for _, a := range p.Assignments {
		if err := validateAssignmentTarget(a.ScopeType, a.ScopeID, a.OS, a.Role); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO policy_assignments (id, policy_id, scope_type, scope_id, os, role) VALUES ($1,$2,$3,$4,$5,$6)`,
			newID(), pol.ID, a.ScopeType, a.ScopeID, a.OS, a.Role); err != nil {
			return nil, err
		}
	}
	for _, agentID := range p.ExcludedAgents {
		if err := addExclusion(ctx, tx, false, pol.ID, agentID, p.ProposedBy); err != nil {
			return nil, err
		}
	}
	for i := range p.Tasks {
		t := p.Tasks[i]
		t.AgentID, t.PolicyID = nil, &pol.ID
		t.Enabled, t.ProposedBy, t.ApprovedBy = false, p.ProposedBy, ""
		if err := insertTask(ctx, tx, &t); err != nil {
			return nil, err
		}
	}
	for i := range p.Checks {
		c := p.Checks[i]
		c.AgentID, c.PolicyID = nil, &pol.ID
		c.Enabled, c.ProposedBy, c.ApprovedBy = false, p.ProposedBy, ""
		if err := insertCheck(ctx, tx, &c); err != nil {
			return nil, err
		}
	}
	review, err := policyReview(ctx, tx, pol.ID, false)
	if err != nil {
		return nil, err
	}
	return review, tx.Commit(ctx)
}

// PolicyReview reads everything switching the policy on would put into effect.
func (s *Store) PolicyReview(ctx context.Context, id string) (*models.PolicyReview, error) {
	return policyReview(ctx, s.pool, id, false)
}

// policyReview reads a policy, its scopes, tasks and checks. With lock, it
// holds the policy row FOR UPDATE — a new scope, task or check references it
// with a key-share lock, so none can be added until the transaction ends — and
// the task and check rows, so none can be edited either.
func policyReview(ctx context.Context, q dbQuerier, id string, lock bool) (*models.PolicyReview, error) {
	forUpdate := ""
	if lock {
		forUpdate = " FOR UPDATE"
	}
	pol, err := scanPolicy(q.QueryRow(ctx, `SELECT `+policyCols+` FROM policies WHERE id=$1`+forUpdate, id))
	if err != nil {
		return nil, err
	}
	review := &models.PolicyReview{Policy: *pol, Assignments: []models.PolicyAssignment{}}
	rows, err := q.Query(ctx, `SELECT `+assignmentCols+` FROM policy_assignments
		WHERE policy_id=$1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a models.PolicyAssignment
		if err := rows.Scan(&a.ID, &a.PolicyID, &a.ScopeType, &a.ScopeID, &a.OS, &a.Role, &a.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		review.Assignments = append(review.Assignments, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	taskRows, err := q.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE policy_id=$1 ORDER BY created_at, id`+forUpdate, id)
	if err != nil {
		return nil, err
	}
	if review.Tasks, err = scanTasks(taskRows); err != nil {
		return nil, err
	}
	checkRows, err := q.Query(ctx, `SELECT `+checkCols+` FROM checks WHERE policy_id=$1 ORDER BY created_at, id`+forUpdate, id)
	if err != nil {
		return nil, err
	}
	if review.Checks, err = scanChecks(checkRows); err != nil {
		return nil, err
	}
	if review.Tasks == nil {
		review.Tasks = []models.Task{}
	}
	if review.Checks == nil {
		review.Checks = []models.Check{}
	}
	if review.Exclusions, err = listExclusions(ctx, q, false, id); err != nil {
		return nil, err
	}
	review.Seal()
	return review, nil
}

// ActivationRights are the caller's approval permissions. Switching a policy on
// is a policy decision; approving an AI-proposed task or check inside it is a
// task or check decision, and the activation makes it only for a caller who
// could make it alone. Checked against the locked review, not an earlier read.
type ActivationRights struct {
	ApproveTasks, ApproveChecks bool
}

// ErrApprovalNotPermitted refuses an activation that would approve proposals
// the caller has no permission to approve.
var ErrApprovalNotPermitted = errors.New("approving the AI-proposed tasks or checks in this policy needs the tasks.manage or checks.manage permission")

// PolicyActivation is what ActivatePolicy switched on.
type PolicyActivation struct {
	Policy         *models.Policy `json:"policy"`
	ApprovedPolicy bool           `json:"approved_policy"`
	ApprovedTasks  int64          `json:"approved_tasks"`
	ApprovedChecks int64          `json:"approved_checks"`
}

// ActivatePolicy switches a policy on and approves, in the same transaction,
// every AI proposal it carries — the policy itself, its tasks and its checks —
// provided the policy still matches the review the person read (fingerprint).
// A task or check a person switched off stays off.
func (s *Store) ActivatePolicy(ctx context.Context, id, actor, fingerprint string, rights ActivationRights) (*PolicyActivation, error) {
	if actor == "" {
		return nil, errors.New("activation needs the person who reviewed it")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	review, err := policyReview(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if fingerprint == "" || review.Fingerprint != fingerprint {
		return nil, ErrReviewStale
	}
	if (review.PendingTasks > 0 && !rights.ApproveTasks) || (review.PendingChecks > 0 && !rights.ApproveChecks) {
		return nil, ErrApprovalNotPermitted
	}
	out := &PolicyActivation{ApprovedPolicy: review.Policy.Pending}
	tag, err := tx.Exec(ctx, `UPDATE tasks SET enabled=true, approved_by=$2
		WHERE policy_id=$1 AND proposed_by<>'' AND approved_by=''`, id, actor)
	if err != nil {
		return nil, err
	}
	out.ApprovedTasks = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `UPDATE checks SET enabled=true, approved_by=$2
		WHERE policy_id=$1 AND proposed_by<>'' AND approved_by=''`, id, actor)
	if err != nil {
		return nil, err
	}
	out.ApprovedChecks = tag.RowsAffected()
	if out.Policy, err = scanPolicy(tx.QueryRow(ctx, `UPDATE policies SET enabled=true,
		approved_by = CASE WHEN proposed_by<>'' AND approved_by='' THEN $2 ELSE approved_by END,
		approved_at = CASE WHEN proposed_by<>'' AND approved_by='' THEN now() ELSE approved_at END
		WHERE id=$1 RETURNING `+policyCols, id, actor)); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// DeactivatePolicy switches a policy off. Its tasks and checks keep their own
// flags, so switching it on again restores exactly what applied before.
func (s *Store) DeactivatePolicy(ctx context.Context, id string) (*models.Policy, error) {
	return scanPolicy(s.pool.QueryRow(ctx, `UPDATE policies SET enabled=false WHERE id=$1 RETURNING `+policyCols, id))
}

// CreatePatchPolicyProposal writes an AI-proposed patch policy and its scopes
// in one transaction, switched off, and returns its review.
func (s *Store) CreatePatchPolicyProposal(ctx context.Context, p *models.PatchPolicy, assignments []models.PatchPolicyAssignment, excluded ...string) (*models.PatchPolicyReview, error) {
	if p.ProposedBy == "" {
		return nil, errors.New("a patch policy proposal needs its proposer")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := insertPatchPolicy(ctx, tx, p); err != nil {
		return nil, err
	}
	for _, a := range assignments {
		if err := validateAssignmentTarget(a.ScopeType, a.ScopeID, a.OS, a.Role); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO patch_policy_assignments (id, policy_id, scope_type, scope_id, os, role) VALUES ($1,$2,$3,$4,$5,$6)`,
			newID(), p.ID, a.ScopeType, a.ScopeID, a.OS, a.Role); err != nil {
			return nil, err
		}
	}
	for _, agentID := range excluded {
		if err := addExclusion(ctx, tx, true, p.ID, agentID, p.ProposedBy); err != nil {
			return nil, err
		}
	}
	review, err := patchPolicyReview(ctx, tx, p.ID, false)
	if err != nil {
		return nil, err
	}
	return review, tx.Commit(ctx)
}

// PatchPolicyReview reads a patch policy and its scopes, sealed.
func (s *Store) PatchPolicyReview(ctx context.Context, id string) (*models.PatchPolicyReview, error) {
	return patchPolicyReview(ctx, s.pool, id, false)
}

func patchPolicyReview(ctx context.Context, q dbQuerier, id string, lock bool) (*models.PatchPolicyReview, error) {
	forUpdate := ""
	if lock {
		forUpdate = " FOR UPDATE"
	}
	p, err := scanPatchPolicy(q.QueryRow(ctx, `SELECT `+patchPolicyCols+` FROM patch_policies WHERE id=$1`+forUpdate, id))
	if err != nil {
		return nil, err
	}
	review := &models.PatchPolicyReview{Policy: *p, Assignments: []models.PatchPolicyAssignment{}}
	rows, err := q.Query(ctx, `SELECT `+assignmentCols+` FROM patch_policy_assignments
		WHERE policy_id=$1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a models.PatchPolicyAssignment
		if err := rows.Scan(&a.ID, &a.PolicyID, &a.ScopeType, &a.ScopeID, &a.OS, &a.Role, &a.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		review.Assignments = append(review.Assignments, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if review.Exclusions, err = listExclusions(ctx, q, true, id); err != nil {
		return nil, err
	}
	review.Seal()
	return review, nil
}

// ActivatePatchPolicy switches a patch policy on, approving it when it is an AI
// proposal, provided it still matches the review the person read.
func (s *Store) ActivatePatchPolicy(ctx context.Context, id, actor, fingerprint string) (*models.PatchPolicy, bool, error) {
	if actor == "" {
		return nil, false, errors.New("activation needs the person who reviewed it")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	review, err := patchPolicyReview(ctx, tx, id, true)
	if err != nil {
		return nil, false, err
	}
	if fingerprint == "" || review.Fingerprint != fingerprint {
		return nil, false, ErrReviewStale
	}
	p, err := scanPatchPolicy(tx.QueryRow(ctx, `UPDATE patch_policies SET enabled=true,
		approved_by = CASE WHEN proposed_by<>'' AND approved_by='' THEN $2 ELSE approved_by END,
		approved_at = CASE WHEN proposed_by<>'' AND approved_by='' THEN now() ELSE approved_at END
		WHERE id=$1 RETURNING `+patchPolicyCols, id, actor))
	if err != nil {
		return nil, false, err
	}
	return p, review.Policy.Pending, tx.Commit(ctx)
}

// IsProposalUnreviewed reports a write the review constraint refused: an AI
// proposal switched on before a person reviewed it.
func IsProposalUnreviewed(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" &&
		(pgErr.ConstraintName == "policies_proposal_needs_review" || pgErr.ConstraintName == "patch_policies_proposal_needs_review")
}
