package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
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

// proposedTaskArgs is one task as AI Chat and MCP describe it, alone
// (propose_task) or inside a policy proposal (propose_policy).
type proposedTaskArgs struct {
	Name         string `json:"name"`
	Shell        string `json:"shell"`
	Script       string `json:"script"`
	ScheduleKind string `json:"schedule_kind"`
	IntervalSec  int    `json:"interval_sec"`
	RunAtMinute  int    `json:"run_at_minute"`
	DaysOfWeek   int    `json:"days_of_week"`
	RunAtMinutes []int  `json:"run_at_minutes"`
	Timezone     string `json:"timezone"`
}

// task builds the proposed task, switched off and attributed, through the
// same validation the console goes through, so a proposal cannot be a
// definition the REST API would have rejected.
func (in proposedTaskArgs) task(tc ToolContext) (*models.Task, error) {
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
		Name: strings.TrimSpace(in.Name), Shell: in.Shell, Script: in.Script,
		ScheduleKind: in.ScheduleKind, IntervalSec: in.IntervalSec,
		RunAtMinute: in.RunAtMinute, DaysOfWeek: in.DaysOfWeek, RunAtMinutes: in.RunAtMinutes,
		Timezone: in.Timezone,
		// Not negotiable, and not a default the model can override: the schema
		// itself refuses an enabled proposal that nobody has reviewed.
		Enabled:    false,
		ProposedBy: tc.proposalActor(),
	}
	if t.ScheduleKind == "" {
		t.ScheduleKind = models.ScheduleInterval
	}
	if err := models.ValidateSchedule(t); err != nil {
		return nil, err
	}
	return t, nil
}

func proposeTask(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID  string `json:"agent_id"`
		PolicyID string `json:"policy_id"`
		proposedTaskArgs
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	agentID, policyID, err := tc.targetOwner(ctx, in.AgentID, in.PolicyID)
	if err != nil {
		return nil, err
	}
	t, err := in.proposedTaskArgs.task(tc)
	if err != nil {
		return nil, err
	}
	t.AgentID, t.PolicyID = agentID, policyID
	out, err := tc.Store.CreateTask(ctx, t)
	if err != nil {
		return nil, err
	}
	return proposalResult("task", out.ID, out.Name, ownerLabel(agentID, policyID))
}

// legacyCheckTypes are the names propose_check advertised until it was found
// that the server never accepted them: "disk_space" and "cpu_load" against the
// models' diskspace and cpuload, so every disk or CPU check the assistant
// proposed was refused as an unsupported type. The schema now offers the real
// names; these keep a client that cached the old schema (an MCP connector does)
// working.
var legacyCheckTypes = map[string]string{
	"disk_space": models.CheckDiskSpace,
	"cpu_load":   models.CheckCPULoad,
}

// proposedCheckArgs is one check or server rule as AI Chat and MCP describe
// it, alone (propose_check) or inside a policy proposal (propose_policy).
type proposedCheckArgs struct {
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	Config        json.RawMessage `json:"config"`
	WarnThreshold *float64        `json:"warn_threshold"`
	ErrThreshold  *float64        `json:"err_threshold"`
	IntervalSec   int             `json:"interval_sec"`
}

// check builds the proposed check, switched off and attributed, through the
// validation the console uses. owner is the device or policy it will belong
// to; server rule and platform checks depend on it.
func (in proposedCheckArgs) check(tc ToolContext, agentID, policyID *string) (*models.Check, error) {
	if canonical, ok := legacyCheckTypes[in.Type]; ok {
		in.Type = canonical
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
	return c, nil
}

func proposeCheck(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID  string `json:"agent_id"`
		PolicyID string `json:"policy_id"`
		proposedCheckArgs
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	agentID, policyID, err := tc.targetOwner(ctx, in.AgentID, in.PolicyID)
	if err != nil {
		return nil, err
	}
	c, err := in.proposedCheckArgs.check(tc, agentID, policyID)
	if err != nil {
		return nil, err
	}
	out, err := tc.Store.CreateCheck(ctx, c)
	if err != nil {
		return nil, err
	}
	return proposalResult("check", out.ID, out.Name, ownerLabel(agentID, policyID))
}

// maxPolicyProposalItems bounds each list in one policy proposal, so a model
// cannot bury the review under hundreds of rows.
const maxPolicyProposalItems = 25

// proposedScope is a client, site, department (group) or device a proposal
// applies to.
type proposedScope struct {
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
}

// proposalScopes validates and de-duplicates the scopes of a proposal: each
// must be of an allowed type and exist. Unknown ids are refused by name, so the
// model can correct the one it got wrong.
func proposalScopes(ctx context.Context, tc ToolContext, in []proposedScope, allowed ...string) ([]proposedScope, error) {
	if len(in) > maxPolicyProposalItems {
		return nil, fmt.Errorf("at most %d scopes in one proposal", maxPolicyProposalItems)
	}
	seen := map[string]bool{}
	var out []proposedScope
	for _, a := range in {
		a.ScopeType, a.ScopeID = strings.TrimSpace(a.ScopeType), strings.TrimSpace(a.ScopeID)
		if !slices.Contains(allowed, a.ScopeType) {
			return nil, fmt.Errorf("scope_type must be one of %s", strings.Join(allowed, ", "))
		}
		canonical, err := uuidCanonical(a.ScopeID)
		if err != nil {
			return nil, fmt.Errorf("scope_id %q is not an id (use list_org or list_devices)", a.ScopeID)
		}
		// One spelling per id, so "{…}" or upper case cannot slip a duplicate past.
		a.ScopeID = canonical
		key := a.ScopeType + ":" + a.ScopeID
		if seen[key] {
			continue
		}
		seen[key] = true
		ok, err := tc.Store.ScopeExists(ctx, a.ScopeType, a.ScopeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("scope not found: %s %s", a.ScopeType, a.ScopeID)
		}
		out = append(out, a)
	}
	return out, nil
}

// uuidCanonical returns the one spelling of an id.
func uuidCanonical(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// proposePolicy proposes a policy in one piece — where it applies, the tasks
// and the checks it carries — all of it switched off. A person reads all of it
// in Policies and switches it on with one activation (store.ActivatePolicy),
// which refuses if anything changed after they opened the review.
func proposePolicy(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Name        string              `json:"name"`
		Description string              `json:"description"`
		Assignments []proposedScope     `json:"assignments"`
		Tasks       []proposedTaskArgs  `json:"tasks"`
		Checks      []proposedCheckArgs `json:"checks"`
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
	if len(in.Tasks) > maxPolicyProposalItems || len(in.Checks) > maxPolicyProposalItems {
		return nil, fmt.Errorf("at most %d tasks and %d checks in one proposal", maxPolicyProposalItems, maxPolicyProposalItems)
	}
	// The tasks and checks need the permissions propose_task and propose_check
	// need: a policy is not a way around them.
	if len(in.Tasks) > 0 && !tc.Permissions[models.PermTasksManage] {
		return nil, fmt.Errorf("proposing tasks needs the tasks.manage permission")
	}
	if len(in.Checks) > 0 && !tc.Permissions[models.PermChecksManage] {
		return nil, fmt.Errorf("proposing checks needs the checks.manage permission")
	}
	scopes, err := proposalScopes(ctx, tc, in.Assignments, models.ScopeClient, models.ScopeSite, models.ScopeGroup, models.ScopeAgent)
	if err != nil {
		return nil, err
	}
	// Everything is validated before anything is written: the proposal is one
	// transaction, and a refusal leaves nothing half-created behind.
	p := &store.PolicyProposal{Name: strings.TrimSpace(in.Name), Description: in.Description, ProposedBy: tc.proposalActor()}
	for _, a := range scopes {
		p.Assignments = append(p.Assignments, models.PolicyAssignment{ScopeType: a.ScopeType, ScopeID: a.ScopeID})
	}
	placeholder := "proposal"
	for i, ta := range in.Tasks {
		t, err := ta.task(tc)
		if err != nil {
			return nil, fmt.Errorf("task %d: %w", i+1, err)
		}
		p.Tasks = append(p.Tasks, *t)
	}
	for i, ca := range in.Checks {
		c, err := ca.check(tc, nil, &placeholder)
		if err != nil {
			return nil, fmt.Errorf("check %d: %w", i+1, err)
		}
		p.Checks = append(p.Checks, *c)
	}
	review, err := tc.Store.CreatePolicyProposal(ctx, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":     review.Policy.ID,
		"kind":   "policy",
		"name":   review.Policy.Name,
		"status": "awaiting_review",
		"scopes": len(review.Assignments),
		"tasks":  len(review.Tasks),
		"checks": len(review.Checks),
		"message": "PROPOSAL CREATED SWITCHED OFF, with its scopes, tasks and checks. Nothing in it applies to any " +
			"device until a person reviews all of it in Policies and activates it. Tell the user it is waiting for their review.",
	}, nil
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
