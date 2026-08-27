package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p1ngu/rmm/internal/models"
)

// Configuration tools.
//
// These exist because "create a task" is not the harmless-sounding request it
// appears to be. A task is a script with a schedule, and the scheduler runs one
// through jobs.RunAutomated — pre-approved, dispatched immediately, repeating
// forever. An AI able to create an enabled task would have been strictly more
// powerful than one able to propose a script, and would have walked straight
// around the approval gate that is the whole basis of the AI's guarantee.
//
// So every tool here PROPOSES. The row is written disabled and stamped with the
// proposing actor; the scheduler selects WHERE enabled and never sees it. A
// person enables it in the console, and the database refuses to enable an
// unreviewed proposal — the guard is a CHECK constraint rather than a branch in
// this file, because the REST handlers are a second path into the same rows.
//
// The one tool here that acts rather than proposes is install_patches, and it is
// safe for a reason that has nothing to do with this file: DispatchInstall calls
// ClaimApprovedPatches, so it can only ever install what a person already
// approved. Approving a patch is a human decision and has no tool.

// proposalActor is what lands in proposed_by. Prefixing keeps AI-authored
// configuration distinguishable from a person's forever, which is the point of
// the column: provenance outlives the review.
func (tc ToolContext) proposalActor() string {
	if strings.HasPrefix(tc.Actor, "mcp:") {
		return "mcp:" + strings.TrimPrefix(tc.Actor, "mcp:")
	}
	return "ai:" + tc.Actor
}

// targetOwner resolves the agent_id / policy_id pair every task and check
// carries, enforcing both the "exactly one" rule and the caller's access scope.
// A policy is the dangerous target: it reaches every machine assigned to it, so
// a restricted caller must be entitled to manage it, not merely to see it.
func (tc ToolContext) targetOwner(ctx context.Context, agentID, policyID string) (*string, *string, error) {
	agentID, policyID = strings.TrimSpace(agentID), strings.TrimSpace(policyID)
	if (agentID == "") == (policyID == "") {
		return nil, nil, fmt.Errorf("exactly one of agent_id or policy_id is required")
	}
	if agentID != "" {
		if !tc.allows(ctx, agentID) {
			return nil, nil, fmt.Errorf("device is outside your access scope")
		}
		return &agentID, nil, nil
	}
	if tc.ScopeUserID != "" {
		ok, err := tc.Store.PolicyManageAllowedForUser(ctx, tc.ScopeUserID, policyID)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, fmt.Errorf("policy is outside your access scope")
		}
	} else {
		ok, err := tc.Store.PolicyExists(ctx, policyID)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, fmt.Errorf("no such policy")
		}
	}
	return nil, &policyID, nil
}

// proposalResult is the standard answer: it says plainly that nothing runs yet,
// because a model told "created" will report success to the operator and move on.
func proposalResult(kind, id, name string, target string) (any, error) {
	return map[string]any{
		"id":     id,
		"kind":   kind,
		"name":   name,
		"target": target,
		"status": "awaiting_review",
		"message": "PROPOSAL CREATED AND DISABLED. It does not run and will not run until a " +
			"person reviews it in the console and enables it. Tell the user it is waiting for their review.",
	}, nil
}

func proposeTask(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID      string `json:"agent_id"`
		PolicyID     string `json:"policy_id"`
		Name         string `json:"name"`
		Shell        string `json:"shell"`
		Script       string `json:"script"`
		ScheduleKind string `json:"schedule_kind"`
		IntervalSec  int    `json:"interval_sec"`
		RunAtMinute  int    `json:"run_at_minute"`
		DaysOfWeek   int    `json:"days_of_week"`
		RunAtMinutes []int  `json:"run_at_minutes"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	agentID, policyID, err := tc.targetOwner(ctx, in.AgentID, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(in.Script) == "" {
		return nil, fmt.Errorf("script is required")
	}
	if !models.SupportedShell(in.Shell) {
		return nil, fmt.Errorf("unsupported shell %q", in.Shell)
	}
	t := &models.Task{
		AgentID: agentID, PolicyID: policyID,
		Name: strings.TrimSpace(in.Name), Shell: in.Shell, Script: in.Script,
		ScheduleKind: in.ScheduleKind, IntervalSec: in.IntervalSec,
		RunAtMinute: in.RunAtMinute, DaysOfWeek: in.DaysOfWeek, RunAtMinutes: in.RunAtMinutes,
		// Not negotiable, and not a default the model can override: the schema
		// itself refuses an enabled proposal that nobody has reviewed.
		Enabled:    false,
		ProposedBy: tc.proposalActor(),
	}
	if t.ScheduleKind == "" {
		t.ScheduleKind = models.ScheduleInterval
	}
	// The same validation the console goes through, so a proposal cannot be a
	// definition the REST API would have rejected.
	if err := models.ValidateSchedule(t); err != nil {
		return nil, err
	}
	out, err := tc.Store.CreateTask(ctx, t)
	if err != nil {
		return nil, err
	}
	return proposalResult("task", out.ID, out.Name, ownerLabel(agentID, policyID))
}

func proposeCheck(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID       string          `json:"agent_id"`
		PolicyID      string          `json:"policy_id"`
		Type          string          `json:"type"`
		Name          string          `json:"name"`
		Config        json.RawMessage `json:"config"`
		WarnThreshold *float64        `json:"warn_threshold"`
		ErrThreshold  *float64        `json:"err_threshold"`
		IntervalSec   int             `json:"interval_sec"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	agentID, policyID, err := tc.targetOwner(ctx, in.AgentID, in.PolicyID)
	if err != nil {
		return nil, err
	}
	c := &models.Check{
		AgentID: agentID, PolicyID: policyID,
		Type: in.Type, Name: in.Name, Config: in.Config,
		WarnThreshold: in.WarnThreshold, ErrThreshold: in.ErrThreshold,
		IntervalSec: in.IntervalSec,
		Enabled:     false,
		ProposedBy:  tc.proposalActor(),
	}
	// The console's create path fills these from the request body; there is no
	// body here, so apply the same defaults ValidateCheck expects to find.
	if c.IntervalSec <= 0 {
		c.IntervalSec = 300
	}
	c.FailureCount, c.RecoveryCount, c.TimeoutSec = 2, 2, 180
	if err := models.ValidateCheck(c); err != nil {
		return nil, err
	}
	out, err := tc.Store.CreateCheck(ctx, c)
	if err != nil {
		return nil, err
	}
	return proposalResult("check", out.ID, out.Name, ownerLabel(agentID, policyID))
}

func proposePolicy(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	// Same rule the REST handler enforces: a policy definition is global, and a
	// principal confined to one client has no business creating one.
	if tc.ScopeUserID != "" {
		return nil, fmt.Errorf("scoped users cannot create global policy definitions")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	out, err := tc.Store.CreatePolicy(ctx, strings.TrimSpace(in.Name), in.Description, tc.proposalActor())
	if err != nil {
		return nil, err
	}
	return proposalResult("policy", out.ID, out.Name, "fleet")
}

// installPatches dispatches an install for patches a PERSON already approved.
// There is deliberately no tool to approve one: approval is the human decision
// about what enters the fleet, and handing it to the model would make this tool's
// safety circular. The store enforces it regardless — ClaimApprovedPatches
// filters to approved identifiers, so an unapproved id here installs nothing.
func installPatches(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID     string   `json:"agent_id"`
		Identifiers []string `json:"identifiers"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if !tc.allows(ctx, in.AgentID) {
		return nil, fmt.Errorf("device is outside your access scope")
	}
	if len(in.Identifiers) == 0 {
		return nil, fmt.Errorf("identifiers is required: name the patches to install, never install everything implicitly")
	}
	if tc.Patches == nil {
		return nil, fmt.Errorf("patch service unavailable")
	}
	if err := tc.Patches.DispatchInstall(ctx, in.AgentID, in.Identifiers); err != nil {
		return nil, err
	}
	return map[string]any{
		"agent_id":  in.AgentID,
		"requested": in.Identifiers,
		"status":    "dispatched",
		"message": "Only patches a person had already approved were sent. Any identifier that " +
			"was not approved was silently skipped; approval is a human action with no tool.",
	}, nil
}

func ownerLabel(agentID, policyID *string) string {
	if agentID != nil {
		return "device " + *agentID
	}
	return "policy " + *policyID
}

func listTasksTool(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	agentID, policyID, err := tc.readTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	return tc.Store.ListTasks(ctx, agentID, policyID)
}

func listChecksTool(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	agentID, policyID, err := tc.readTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	return tc.Store.ListChecks(ctx, agentID, policyID)
}

func listPoliciesTool(ctx context.Context, tc ToolContext) (any, error) {
	if tc.ScopeUserID != "" {
		return tc.Store.ListPoliciesForUser(ctx, tc.ScopeUserID)
	}
	return tc.Store.ListPolicies(ctx)
}

// readTarget is targetOwner's read-only twin: the same ownership rule, but a
// policy only has to be visible rather than manageable.
func (tc ToolContext) readTarget(ctx context.Context, args json.RawMessage) (string, string, error) {
	var in struct {
		AgentID  string `json:"agent_id"`
		PolicyID string `json:"policy_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", "", err
	}
	in.AgentID, in.PolicyID = strings.TrimSpace(in.AgentID), strings.TrimSpace(in.PolicyID)
	if (in.AgentID == "") == (in.PolicyID == "") {
		return "", "", fmt.Errorf("exactly one of agent_id or policy_id is required")
	}
	if in.AgentID != "" {
		if !tc.allows(ctx, in.AgentID) {
			return "", "", fmt.Errorf("device is outside your access scope")
		}
		return in.AgentID, "", nil
	}
	if tc.ScopeUserID != "" {
		ok, err := tc.Store.PolicyAllowedForUser(ctx, tc.ScopeUserID, in.PolicyID)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fmt.Errorf("policy is outside your access scope")
		}
	}
	return "", in.PolicyID, nil
}
