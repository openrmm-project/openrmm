package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// ===================== Patch management =====================

// agentScope builds a WHERE fragment selecting the agents covered by a scope
// (”=global -> all agents; agent|site|client). Aliased to `a` (agents) joined
// to `sites s` when needed by the caller's query; here we use subqueries so it
// works standalone against agent_patches.agent_id.
func agentScopeClause(scopeType, scopeID string, n int) (string, []any) {
	switch scopeType {
	case "agent":
		return fmt.Sprintf("agent_id = $%d", n), []any{scopeID}
	case "site":
		return fmt.Sprintf("agent_id IN (SELECT id FROM agents WHERE site_id = $%d)", n), []any{scopeID}
	case "client":
		return fmt.Sprintf("agent_id IN (SELECT a.id FROM agents a JOIN sites s ON s.id=a.site_id WHERE s.client_id = $%d)", n), []any{scopeID}
	default: // global
		return "TRUE", nil
	}
}

// UpsertPatchReport stores an agent's current set of missing patches: it upserts
// the catalog, marks reported patches as still-missing (preserving approved/
// excluded state), and clears patches no longer reported (treated as resolved/
// installed). It also stamps last_patch_scan.
func (s *Store) UpsertPatchReport(ctx context.Context, agentID string, rep *protocol.PatchReportMsg) error {
	if rep.Error != "" {
		// A failed scan is not an empty scan. Preserve the last trusted snapshot.
		return fmt.Errorf("patch scan failed: %s", rep.Error)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	seen := make([]string, 0, len(rep.Patches))
	for _, p := range rep.Patches {
		class := normalizeClass(p.Classification)
		var patchID string
		err := tx.QueryRow(ctx, `
			INSERT INTO patches (id, os, identifier, title, classification, kb, cves, reboot_required, first_seen, last_seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), now())
			ON CONFLICT (os, identifier) DO UPDATE SET
			    title=EXCLUDED.title, classification=EXCLUDED.classification, kb=EXCLUDED.kb,
			    cves=EXCLUDED.cves, reboot_required=EXCLUDED.reboot_required, last_seen=now()
			RETURNING id`,
			newID(), rep.OS, p.Identifier, p.Title, class, p.KB, emptyToArr(p.CVEs), p.RebootRequired).Scan(&patchID)
		if err != nil {
			return err
		}
		seen = append(seen, patchID)
		// Upsert agent_patches without clobbering an approval/exclusion decision.
		if _, err := tx.Exec(ctx, `
			INSERT INTO agent_patches (agent_id, patch_id, status, available_version, detected_at)
			VALUES ($1,$2,'missing',$3, now())
			ON CONFLICT (agent_id, patch_id) DO UPDATE SET
			    available_version=EXCLUDED.available_version,
			    status = CASE WHEN agent_patches.status IN ('installed','installing') THEN 'missing'
			                  ELSE agent_patches.status END,
			    detected_at = now(), error=''`,
			agentID, patchID, p.Version); err != nil {
			return err
		}
	}
	// Patches the device no longer reports are gone (installed elsewhere / removed).
	if len(seen) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM agent_patches WHERE agent_id=$1 AND patch_id <> ALL($2)`, agentID, seen); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM agent_patches WHERE agent_id=$1`, agentID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET last_patch_scan=now() WHERE id=$1`, agentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func normalizeClass(c string) string {
	switch c {
	case models.PatchClassSecurity, models.PatchClassCritical, models.PatchClassImportant,
		models.PatchClassModerate, models.PatchClassOptional:
		return c
	default:
		return models.PatchClassOther
	}
}

func scanAgentPatch(rows interface {
	Scan(...any) error
}) (*models.AgentPatch, error) {
	var p models.AgentPatch
	if err := rows.Scan(&p.AgentID, &p.PatchID, &p.Status, &p.AvailableVer, &p.DetectedAt,
		&p.InstalledAt, &p.Error, &p.Hostname, &p.Identifier, &p.Title, &p.Classification,
		&p.KB, &p.CVEs, &p.RebootRequired); err != nil {
		return nil, err
	}
	return &p, nil
}

const agentPatchCols = `ap.agent_id, ap.patch_id, ap.status, ap.available_version, ap.detected_at,
	ap.installed_at, ap.error, a.hostname, p.identifier, p.title, p.classification, p.kb, p.cves, p.reboot_required`

// ListAgentPatches returns a device's patches joined with catalog metadata.
func (s *Store) ListAgentPatches(ctx context.Context, agentID string) ([]models.AgentPatch, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentPatchCols+`
		FROM agent_patches ap
		JOIN patches p ON p.id = ap.patch_id
		JOIN agents a ON a.id = ap.agent_id
		WHERE ap.agent_id=$1
		ORDER BY (p.classification='security') DESC, p.classification, p.title`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AgentPatch
	for rows.Next() {
		p, err := scanAgentPatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PatchCatalogRow is a catalog entry with how many in-scope devices are missing it.
type PatchCatalogRow struct {
	models.Patch
	MissingCount  int `json:"missing_count"`
	ApprovedCount int `json:"approved_count"`
}

// PatchAllowedForUser reports whether a patch exists on any visible device.
func (s *Store) PatchAllowedForUser(ctx context.Context, userID, patchID string) (bool, error) {
	var ok bool
	if userID == "" {
		err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM patches WHERE id=$1)`, patchID).Scan(&ok)
		return ok, err
	}
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM agent_patches ap JOIN agents a ON a.id=ap.agent_id
		WHERE ap.patch_id=$2 AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='agent' AND us.scope_id=a.id)
		    OR (us.scope_type='group' AND us.scope_id=a.group_id)
		    OR (us.scope_type='site' AND us.scope_id=a.site_id)
		    OR (us.scope_type='client' AND us.scope_id=(SELECT si.client_id FROM sites si WHERE si.id=a.site_id)))))
		)`, userID, patchID).Scan(&ok)
	return ok, err
}

// PatchCatalog lists distinct patches with per-state device counts, filtered to
// the caller's scope (reuses ReportFilter).
func (s *Store) PatchCatalog(ctx context.Context, f ReportFilter) ([]PatchCatalogRow, error) {
	where, args := f.where()
	q := `
		SELECT p.id, p.os, p.identifier, p.title, p.classification, p.kb, p.cves,
		       p.reboot_required, p.first_seen, p.last_seen,
		       COUNT(*) FILTER (WHERE ap.status IN ('missing','approved','installing','failed')),
		       COUNT(*) FILTER (WHERE ap.status = 'approved')
		FROM patches p
		JOIN agent_patches ap ON ap.patch_id = p.id
		JOIN agents a ON a.id = ap.agent_id` + reportJoins + where + `
		GROUP BY p.id
		ORDER BY (p.classification='security') DESC, p.classification, p.title`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PatchCatalogRow
	for rows.Next() {
		var r PatchCatalogRow
		if err := rows.Scan(&r.ID, &r.OS, &r.Identifier, &r.Title, &r.Classification, &r.KB,
			&r.CVEs, &r.RebootRequired, &r.FirstSeen, &r.LastSeen, &r.MissingCount, &r.ApprovedCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetApproval records an approve/decline for a patch within a scope and moves the
// matching agent_patches rows to approved/excluded (never touching in-flight or
// installed rows).
func (s *Store) SetApproval(ctx context.Context, patchID, scopeType, scopeID, decision, source, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO patch_approvals (id, patch_id, scope_type, scope_id, decision, source, actor)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (patch_id, scope_type, scope_id) DO UPDATE SET
		    decision=EXCLUDED.decision, source=EXCLUDED.source, actor=EXCLUDED.actor, created_at=now()`,
		newID(), patchID, scopeType, scopeID, decision, source, actor); err != nil {
		return err
	}
	newStatus := models.PatchStatusApproved
	if decision == "decline" {
		newStatus = models.PatchStatusExcluded
	}
	clause, sargs := agentScopeClause(scopeType, scopeID, 2)
	args := append([]any{patchID}, sargs...)
	if _, err := tx.Exec(ctx, `UPDATE agent_patches SET status=$`+itoa(len(args)+1)+`
		WHERE patch_id=$1 AND status IN ('missing','approved','excluded') AND `+clause,
		append(args, newStatus)...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// ApprovedIdentifiers returns the patch identifiers currently approved for a
// device (ready to install).
func (s *Store) ApprovedIdentifiers(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.identifier FROM agent_patches ap
		JOIN patches p ON p.id=ap.patch_id WHERE ap.agent_id=$1 AND ap.status='approved'`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ClaimApprovedPatches atomically moves approved patches into installing and
// returns the exact identifiers claimed. Callers must dispatch only this list;
// never pass an empty list to an endpoint package manager as "install all".
func (s *Store) ClaimApprovedPatches(ctx context.Context, agentID string, identifiers []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `UPDATE agent_patches ap SET status='installing', error=''
		FROM patches p WHERE p.id=ap.patch_id AND ap.agent_id=$1 AND ap.status='approved'
		AND ($2::text[] IS NULL OR p.identifier = ANY($2))
		RETURNING p.identifier`, agentID, nilIfEmpty(identifiers))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStrings(rows)
}

// RequeueClaimedPatches restores a claim when the command could not be queued
// to a live agent. It never changes a result that has already been processed.
func (s *Store) RequeueClaimedPatches(ctx context.Context, agentID string, identifiers []string) error {
	if len(identifiers) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_patches ap SET status='approved'
		FROM patches p WHERE p.id=ap.patch_id AND ap.agent_id=$1 AND ap.status='installing'
		AND p.identifier = ANY($2)`, agentID, identifiers)
	return err
}

func nilIfEmpty(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s
}

// ApplyPatchResult updates agent_patches from an install run and flags a pending
// reboot when needed.
func (s *Store) ApplyPatchResult(ctx context.Context, agentID string, res *protocol.PatchResultMsg) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, r := range res.Results {
		status := models.PatchStatusInstalled
		var installedAt any = time.Now()
		if !r.Success {
			status, installedAt = models.PatchStatusFailed, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_patches ap SET status=$3, installed_at=$4, error=$5
			FROM patches p WHERE p.id=ap.patch_id AND ap.agent_id=$1 AND p.identifier=$2`,
			agentID, r.Identifier, status, installedAt, r.Error); err != nil {
			return err
		}
	}
	if res.RebootRequired {
		if _, err := tx.Exec(ctx, `UPDATE agents SET reboot_pending=TRUE WHERE id=$1`, agentID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SetRebootPending toggles the reboot flag (cleared after a reboot is dispatched).
func (s *Store) SetRebootPending(ctx context.Context, agentID string, v bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET reboot_pending=$2 WHERE id=$1`, agentID, v)
	return err
}

// PatchScanDue returns agent ids that have never scanned or scanned longer ago
// than interval (the scheduler filters these to online agents).
func (s *Store) PatchScanDue(ctx context.Context, interval time.Duration) ([]string, error) {
	cutoff := time.Now().Add(-interval)
	rows, err := s.pool.Query(ctx, `SELECT id FROM agents WHERE last_patch_scan IS NULL OR last_patch_scan < $1`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDs(rows)
}

// RebootPendingAgents returns agent ids flagged as needing a reboot.
func (s *Store) RebootPendingAgents(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM agents WHERE reboot_pending`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDs(rows)
}

func scanIDs(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]string, error) {
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func scanStrings(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]string, error) {
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// ===================== Patch policies =====================

const patchPolicyCols = `id, name, enabled, auto_approve, auto_approve_age_days, exclusions,
	window_days, window_start_min, window_end_min, reboot, created_at`

func scanPatchPolicy(row interface{ Scan(...any) error }) (*models.PatchPolicy, error) {
	var p models.PatchPolicy
	if err := row.Scan(&p.ID, &p.Name, &p.Enabled, &p.AutoApprove, &p.AutoApproveAgeDays,
		&p.Exclusions, &p.WindowDays, &p.WindowStartMin, &p.WindowEndMin, &p.Reboot, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) CreatePatchPolicy(ctx context.Context, p *models.PatchPolicy) (*models.PatchPolicy, error) {
	p.ID = newID()
	err := s.pool.QueryRow(ctx, `INSERT INTO patch_policies
		(id,name,enabled,auto_approve,auto_approve_age_days,exclusions,window_days,window_start_min,window_end_min,reboot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at`,
		p.ID, p.Name, p.Enabled, emptyToArr(p.AutoApprove), p.AutoApproveAgeDays, emptyToArr(p.Exclusions),
		emptyToIntArr(p.WindowDays), p.WindowStartMin, p.WindowEndMin, defReboot(p.Reboot)).Scan(&p.CreatedAt)
	return p, err
}

func defReboot(r string) string {
	switch r {
	case models.RebootNever, models.RebootIfRequired, models.RebootAlways:
		return r
	default:
		return models.RebootIfRequired
	}
}

func (s *Store) UpdatePatchPolicy(ctx context.Context, p *models.PatchPolicy) error {
	_, err := s.pool.Exec(ctx, `UPDATE patch_policies SET name=$2, enabled=$3, auto_approve=$4,
		auto_approve_age_days=$5, exclusions=$6, window_days=$7, window_start_min=$8,
		window_end_min=$9, reboot=$10 WHERE id=$1`,
		p.ID, p.Name, p.Enabled, emptyToArr(p.AutoApprove), p.AutoApproveAgeDays, emptyToArr(p.Exclusions),
		emptyToIntArr(p.WindowDays), p.WindowStartMin, p.WindowEndMin, defReboot(p.Reboot))
	return err
}

func (s *Store) DeletePatchPolicy(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM patch_policies WHERE id=$1`, id)
	return err
}

func (s *Store) ListPatchPolicies(ctx context.Context) ([]models.PatchPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+patchPolicyCols+` FROM patch_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PatchPolicy
	for rows.Next() {
		p, err := scanPatchPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) PatchPolicyExists(ctx context.Context, policyID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM patch_policies WHERE id=$1)`, policyID).Scan(&ok)
	return ok, err
}

// PatchPolicyAllowedForUser reports whether at least one assignment intersects
// the user's visible devices. It is intended for reads, not mutations.
func (s *Store) PatchPolicyAllowedForUser(ctx context.Context, userID, policyID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM patch_policy_assignments pa
		JOIN agents a ON (
		  (pa.scope_type='agent' AND pa.scope_id=a.id)
		  OR (pa.scope_type='site' AND pa.scope_id=a.site_id)
		  OR (pa.scope_type='client' AND pa.scope_id=(SELECT si.client_id FROM sites si WHERE si.id=a.site_id)))
		WHERE pa.policy_id=$2 AND (
		  NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1)
		  OR EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
		    (us.scope_type='agent' AND us.scope_id=a.id)
		    OR (us.scope_type='group' AND us.scope_id=a.group_id)
		    OR (us.scope_type='site' AND us.scope_id=a.site_id)
		    OR (us.scope_type='client' AND us.scope_id=(SELECT si.client_id FROM sites si WHERE si.id=a.site_id)))))
		)`, userID, policyID).Scan(&ok)
	return ok, err
}

// PatchPolicyManageAllowedForUser requires every assignment to be wholly
// controlled by the user. Unassigned global policies are not scoped resources.
func (s *Store) PatchPolicyManageAllowedForUser(ctx context.Context, userID, policyID string) (bool, error) {
	assignments, err := s.ListPatchPolicyAssignments(ctx, policyID)
	if err != nil {
		return false, err
	}
	if len(assignments) == 0 {
		return false, nil
	}
	for _, a := range assignments {
		ok, err := s.ScopeAllowedForUser(ctx, userID, a.ScopeType, a.ScopeID)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (s *Store) ListPatchPolicyAssignments(ctx context.Context, policyID string) ([]models.PatchPolicyAssignment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, policy_id, scope_type, scope_id, created_at
		FROM patch_policy_assignments WHERE policy_id=$1`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PatchPolicyAssignment
	for rows.Next() {
		var a models.PatchPolicyAssignment
		if err := rows.Scan(&a.ID, &a.PolicyID, &a.ScopeType, &a.ScopeID, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetPatchPolicyAssignment(ctx context.Context, id string) (*models.PatchPolicyAssignment, error) {
	var a models.PatchPolicyAssignment
	err := s.pool.QueryRow(ctx, `SELECT id, policy_id, scope_type, scope_id, created_at
		FROM patch_policy_assignments WHERE id=$1`, id).
		Scan(&a.ID, &a.PolicyID, &a.ScopeType, &a.ScopeID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) AddPatchPolicyAssignment(ctx context.Context, policyID, scopeType, scopeID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO patch_policy_assignments (id,policy_id,scope_type,scope_id)
		VALUES ($1,$2,$3,$4)`, newID(), policyID, scopeType, scopeID)
	return err
}

func (s *Store) DeletePatchPolicyAssignment(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM patch_policy_assignments WHERE id=$1`, id)
	return err
}

// PatchPoliciesForAgent resolves the enabled policies covering an agent (by its
// own id, its site, or its client) — mirrors ChecksForAgent.
func (s *Store) PatchPoliciesForAgent(ctx context.Context, agentID string) ([]models.PatchPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT `+patchPolicyCols+` FROM patch_policies pp
		WHERE pp.enabled AND EXISTS (
		  SELECT 1 FROM patch_policy_assignments pa WHERE pa.policy_id=pp.id AND (
		    (pa.scope_type='agent'  AND pa.scope_id=$1) OR
		    (pa.scope_type='site'   AND pa.scope_id=(SELECT site_id FROM agents WHERE id=$1)) OR
		    (pa.scope_type='client' AND pa.scope_id=(SELECT s.client_id FROM agents a JOIN sites s ON s.id=a.site_id WHERE a.id=$1))
		  ))`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PatchPolicy
	for rows.Next() {
		p, err := scanPatchPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// AutoApproveForAgent approves an agent's eligible missing patches per a policy:
// classification in auto_approve, old enough (first_seen <= now-age), not excluded
// or manually-declined. Returns how many it approved.
func (s *Store) AutoApproveForAgent(ctx context.Context, agentID string, p *models.PatchPolicy) (int, error) {
	if len(p.AutoApprove) == 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -p.AutoApproveAgeDays)
	tag, err := s.pool.Exec(ctx, `UPDATE agent_patches ap SET status='approved'
		FROM patches pt WHERE pt.id=ap.patch_id AND ap.agent_id=$1 AND ap.status='missing'
		AND pt.classification = ANY($2) AND pt.first_seen <= $3
		AND pt.classification <> ALL($4) AND pt.identifier <> ALL($4)`,
		agentID, p.AutoApprove, cutoff, emptyToArr(p.Exclusions))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func emptyToArr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func emptyToIntArr(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}

// ===================== Compliance =====================

// ComplianceReport summarizes patch state per in-scope device.
func (s *Store) ComplianceReport(ctx context.Context, f ReportFilter) ([]models.ComplianceRow, error) {
	where, args := f.where()
	q := `
		SELECT a.id, a.hostname, a.os, COALESCE(cl.name,''), COALESCE(s.name,''), COALESCE(g.name,''),
		       COUNT(ap.patch_id) FILTER (WHERE ap.status IN ('missing','approved','installing')),
		       COUNT(ap.patch_id) FILTER (WHERE ap.status IN ('missing','approved','installing') AND pt.classification='security'),
		       COUNT(ap.patch_id) FILTER (WHERE ap.status='approved'),
		       COUNT(ap.patch_id) FILTER (WHERE ap.status='failed'),
		       a.reboot_pending, a.last_patch_scan
		FROM agents a` + reportJoins + `
		LEFT JOIN agent_patches ap ON ap.agent_id = a.id
		LEFT JOIN patches pt ON pt.id = ap.patch_id` + where + `
		GROUP BY a.id, cl.name, s.name, g.name
		ORDER BY a.hostname`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ComplianceRow
	for rows.Next() {
		var r models.ComplianceRow
		if err := rows.Scan(&r.AgentID, &r.Hostname, &r.OS, &r.Client, &r.Site, &r.Department,
			&r.MissingTotal, &r.MissingSec, &r.Approved, &r.Failed, &r.RebootPending, &r.LastScan); err != nil {
			return nil, err
		}
		r.Status = complianceStatus(r)
		out = append(out, r)
	}
	return out, rows.Err()
}

func complianceStatus(r models.ComplianceRow) string {
	switch {
	case r.LastScan == nil:
		return "unknown"
	case r.MissingSec > 0 || r.Failed > 0:
		return "critical"
	case r.MissingTotal > 0 || r.RebootPending:
		return "at_risk"
	default:
		return "compliant"
	}
}
