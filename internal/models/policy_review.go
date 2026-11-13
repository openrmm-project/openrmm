package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// PolicyReview is everything switching a policy on puts into effect, as the
// person activating it is shown it: where it applies, the tasks and checks it
// carries, and which of those are AI proposals the activation approves.
//
// Fingerprint seals that content. The activation sends back the fingerprint of
// the review the person read, and the server refuses it when the policy no
// longer matches — an AI that proposes one more task while the review is open,
// or a colleague who edits a script, cannot ride along on a click that never
// saw them.
type PolicyReview struct {
	Policy        Policy             `json:"policy"`
	Assignments   []PolicyAssignment `json:"assignments"`
	Tasks         []Task             `json:"tasks"`
	Checks        []Check            `json:"checks"`
	Exclusions    []PolicyExclusion  `json:"exclusions"`
	PendingTasks  int                `json:"pending_tasks"`
	PendingChecks int                `json:"pending_checks"`
	Fingerprint   string             `json:"fingerprint"`
}

// PatchPolicyReview is the same for a patch policy: its settings and the scopes
// it covers, sealed by a fingerprint.
type PatchPolicyReview struct {
	Policy      PatchPolicy             `json:"policy"`
	Assignments []PatchPolicyAssignment `json:"assignments"`
	Exclusions  []PolicyExclusion       `json:"exclusions"`
	Fingerprint string                  `json:"fingerprint"`
}

// Seal computes the review's counts and fingerprint from its content.
func (r *PolicyReview) Seal() {
	r.PendingTasks, r.PendingChecks = 0, 0
	type task struct {
		ID, Name, Shell, Script, ScheduleKind, Timezone, ProposedBy, ApprovedBy string
		ScriptID                                                                string
		IntervalSec, RunAtMinute, DaysOfWeek                                    int
		RunAtMinutes                                                            []int
		Enabled                                                                 bool
	}
	type check struct {
		ID, Type, Name, ProposedBy, ApprovedBy string
		Config                                 string
		Warn, Err                              string
		IntervalSec, FailureCount              int
		RecoveryCount                          int
		Hysteresis                             float64
		CooldownSec, TimeoutSec                int
		Enabled                                bool
	}
	tasks := make([]task, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		if t.ProposedBy != "" && t.ApprovedBy == "" {
			r.PendingTasks++
		}
		scriptID := ""
		if t.ScriptID != nil {
			scriptID = *t.ScriptID
		}
		tasks = append(tasks, task{t.ID, t.Name, t.Shell, t.Script, t.ScheduleKind, t.Timezone, t.ProposedBy, t.ApprovedBy,
			scriptID, t.IntervalSec, t.RunAtMinute, t.DaysOfWeek, slices.Clone(t.RunAtMinutes), t.Enabled})
	}
	checks := make([]check, 0, len(r.Checks))
	for _, c := range r.Checks {
		if c.ProposedBy != "" && c.ApprovedBy == "" {
			r.PendingChecks++
		}
		checks = append(checks, check{c.ID, c.Type, c.Name, c.ProposedBy, c.ApprovedBy, canonicalJSON(c.Config),
			threshold(c.WarnThreshold), threshold(c.ErrThreshold), c.IntervalSec, c.FailureCount,
			c.RecoveryCount, c.Hysteresis, c.CooldownSec, c.TimeoutSec, c.Enabled})
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	p := r.Policy
	r.Fingerprint = fingerprint(struct {
		ID, Name, Description, ProposedBy, ApprovedBy string
		Enabled                                       bool
		Scopes                                        []string
		Tasks                                         []task
		Checks                                        []check
		Excluded                                      []string
	}{p.ID, p.Name, p.Description, p.ProposedBy, p.ApprovedBy, p.Enabled, policyScopes(r.Assignments), tasks, checks, excludedAgents(r.Exclusions)})
}

// Seal computes the patch policy review's fingerprint from its content.
func (r *PatchPolicyReview) Seal() {
	p := r.Policy
	scopes := make([]string, 0, len(r.Assignments))
	for _, a := range r.Assignments {
		scopes = append(scopes, a.ScopeType+":"+a.ScopeID+":"+a.OS+":"+a.Role)
	}
	sort.Strings(scopes)
	r.Fingerprint = fingerprint(struct {
		ID, Name, Timezone, Reboot, ProposedBy, ApprovedBy string
		Enabled                                            bool
		AutoApprove, Exclusions                            []string
		AutoApproveAgeDays, WindowStart, WindowEnd         int
		WindowDays                                         []int
		Scopes                                             []string
		Excluded                                           []string
	}{p.ID, p.Name, p.Timezone, p.Reboot, p.ProposedBy, p.ApprovedBy, p.Enabled, p.AutoApprove, p.Exclusions,
		p.AutoApproveAgeDays, p.WindowStartMin, p.WindowEndMin, p.WindowDays, scopes, excludedAgents(r.Exclusions)})
}

// policyScopes and excludedAgents are the order-free forms the fingerprint
// seals: each assignment with its filters, and the devices left out.
func policyScopes(as []PolicyAssignment) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ScopeType+":"+a.ScopeID+":"+a.OS+":"+a.Role)
	}
	sort.Strings(out)
	return out
}

func excludedAgents(es []PolicyExclusion) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.AgentID)
	}
	sort.Strings(out)
	return out
}

func threshold(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(*v)
}

// canonicalJSON re-encodes a JSON document with sorted keys, so the same
// configuration always seals to the same fingerprint.
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, _ := json.Marshal(v) // encoding/json sorts map keys
	return string(out)
}

func fingerprint(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// PatchPolicyClasses are the classifications a patch policy may auto-approve.
var PatchPolicyClasses = []string{PatchClassSecurity, PatchClassCritical, PatchClassImportant,
	PatchClassModerate, PatchClassOptional, PatchClassOther}

// ValidatePatchPolicy refuses a patch policy that cannot mean what it says: an
// unknown classification or reboot mode, a weekday or minute outside the day,
// or an exclusion that is empty. The console and AI Chat/MCP both go through it.
func ValidatePatchPolicy(p *PatchPolicy) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(p.Name) > 200 {
		return fmt.Errorf("name is longer than 200 characters")
	}
	for _, c := range p.AutoApprove {
		if !slices.Contains(PatchPolicyClasses, c) {
			return fmt.Errorf("unknown patch classification %q", c)
		}
	}
	if p.AutoApproveAgeDays < 0 || p.AutoApproveAgeDays > 3650 {
		return fmt.Errorf("auto_approve_age_days must be between 0 and 3650")
	}
	seen := map[int]bool{}
	for _, d := range p.WindowDays {
		if d < 0 || d > 6 || seen[d] {
			return fmt.Errorf("window_days must be distinct weekdays from 0 (Sunday) to 6")
		}
		seen[d] = true
	}
	if p.WindowStartMin < 0 || p.WindowStartMin > 1439 || p.WindowEndMin < 0 || p.WindowEndMin > 1439 {
		return fmt.Errorf("window_start_min and window_end_min must be minutes of the day (0-1439)")
	}
	switch p.Reboot {
	case "", RebootNever, RebootIfRequired, RebootAlways:
	default:
		return fmt.Errorf("reboot must be never, if_required or always")
	}
	if len(p.Exclusions) > 500 {
		return fmt.Errorf("at most 500 exclusions")
	}
	for _, e := range p.Exclusions {
		if strings.TrimSpace(e) == "" || len(e) > 200 {
			return fmt.Errorf("each exclusion must be a patch identifier or classification of up to 200 characters")
		}
	}
	return nil
}
