package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/store"
)

// ToolContext carries the dependencies and identity a tool needs to run.
type ToolContext struct {
	Store   *store.Store
	Jobs    *jobs.Service
	Patches *patches.Service
	Actor   string // who is driving the chat (recorded as job creator / audit actor)
	// Config and PublicURL let a tool hand back a signed download link instead of
	// reading rows into the model. See export_audit.
	Config      *config.Config
	PublicURL   string
	ScopeUserID string // restrict tools to this user's access scope ("" = full)
	Permissions map[string]bool
}

// allows reports whether the caller's scope permits acting on an agent.
func (tc ToolContext) allows(ctx context.Context, agentID string) bool {
	if tc.ScopeUserID == "" {
		return true
	}
	ok, err := tc.Store.AgentAllowedForUser(ctx, tc.ScopeUserID, agentID)
	return err == nil && ok
}

func (tc ToolContext) hasPermission(permission string) bool {
	if permission == "" || tc.Permissions == nil {
		return true
	}
	return tc.Permissions[permission]
}

// ToolResult is returned to the chat loop after a tool runs.
type ToolResult struct {
	Output        string   // JSON string fed back to the model
	CreatedJobIDs []string // jobs proposed during this call (surfaced to the UI)
}

// Tools returns the catalog advertised to the model.
//
// Design note: the advertised catalog is filtered by permission. Write-capable
// tools never execute anything directly — they create jobs in 'awaiting_approval'.
// A human approves in the UI before anything reaches an endpoint.
func Tools() []Tool {
	base := []Tool{
		{Type: "function", Function: FunctionDef{
			Name:        "list_devices",
			Description: "List managed devices (agents) with id, hostname, OS, arch and online status. Use this to find the target device id for an action.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "get_inventory",
			Description: "Get the latest collected inventory (hardware/software/services) for a device, as JSON. Use it to gather context before proposing a script.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"agent_id":{"type":"string","description":"target device id"}},"required":["agent_id"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "propose_script",
			Description: "Propose a script to run on a device. This DOES NOT execute it: it creates a job that a human must approve first. Write the full script yourself for the device's OS. Pick the correct shell.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string","description":"target device id"},
				"shell":{"type":"string","enum":["bash","sh","powershell","cmd","python"],"description":"interpreter for the script"},
				"script":{"type":"string","description":"the full script source to run"},
				"description":{"type":"string","description":"one-line human summary of what this does"}
			},"required":["agent_id","shell","script","description"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "get_job_status",
			Description: "Get the current status and result (exit code, stdout, stderr) of a previously created job.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string"}},"required":["job_id"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "fleet_summary",
			Description: "Get an aggregate summary of the fleet (device counts by OS/status and recent jobs). Use this to generate reports.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "list_patches",
			Description: "List the OS patches/updates currently missing on a device (Windows KBs, Linux advisories) with their classification (security/critical/...), status and whether a reboot is needed. Read-only — installing patches is a human/policy action.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"agent_id":{"type":"string","description":"target device id"}},"required":["agent_id"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "inventory_report",
			Description: "Fleet-wide inventory across many devices, optionally narrowed to one client, site or department. report_type 'software' covers installed packages; 'hardware' each machine's profile; 'machines' the fleet with status. DEFAULTS TO COUNTS: mode=\"summary\" groups and counts in the database, which is the right call for \"how many machines have X\", \"what is installed\" or \"what OS mix do we run\" — use match to ask about one item. Only use mode=\"rows\" when you must read individual records, and narrow first; it is capped. For the complete data, call export_inventory and give the operator the link. Read-only. For one named device, use get_inventory instead.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"report_type":{"type":"string","enum":["software","hardware","machines"],"description":"which report to produce"},
				"mode":{"type":"string","enum":["summary","rows"],"description":"summary (default) returns counts by group; rows returns individual records, capped"},
				"group_by":{"type":"string","description":"summary only. software: name|version|manufacturer|client. hardware: os|manufacturer|model|platform|client. machines: os|status|client|site|department|version"},
				"match":{"type":"string","description":"summary only: narrow to groups containing this text, e.g. \"nginx\" to count the machines that have it"},
				"scope_type":{"type":"string","enum":["client","site","group"],"description":"optional: narrow to one client, site or department (group)"},
				"scope_id":{"type":"string","description":"id of the client/site/group; required when scope_type is set"}
			},"required":["report_type"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "cancel_job",
			Description: "Cancel a job: rejects it if still awaiting approval, or kills the running process on the device if it is in progress (e.g. a stuck script/download). This stops execution; it never runs anything.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string"}},"required":["job_id"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "propose_bulk_deploy",
			Description: "Propose running ONE script across MANY devices at once (a mass deployment, e.g. \"install X on every device\" or \"deploy to all companies\"). DOES NOT execute: it creates approval-gated jobs, automatically SPLIT INTO ONE BATCH PER COMPANY — a deployment never spans companies — and a human approves each company batch in the UI. To choose targets, first call list_devices and pass their ids in agent_ids (optionally narrow with site_id or group_id). Write the script for the targets' OS.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_ids":{"type":"array","items":{"type":"string"},"description":"target device ids (from list_devices)"},
				"site_id":{"type":"string","description":"optional: also target every device of this site"},
				"group_id":{"type":"string","description":"optional: also target every device of this department (group)"},
				"shell":{"type":"string","enum":["bash","sh","powershell","cmd","python"],"description":"interpreter for the script"},
				"script":{"type":"string","description":"the full script source to run on every target"},
				"description":{"type":"string","description":"one-line human summary of the deployment"}
			},"required":["shell","script","description"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "propose_task",
			Description: "Propose a SCHEDULED task (a script that runs on a schedule) for one device or for a policy. This DOES NOT create working automation: the task is created DISABLED and never runs until a person reviews and enables it in the console. Always tell the user their review is required. Use propose_script instead for something that should run once.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string","description":"target device id; give this OR policy_id, never both"},
				"policy_id":{"type":"string","description":"target policy id; the task then applies to every device the policy is assigned to"},
				"name":{"type":"string","description":"short name for the task"},
				"shell":{"type":"string","enum":["bash","sh","powershell","cmd","python"],"description":"interpreter for the script"},
				"script":{"type":"string","description":"the full script source"},
				"schedule_kind":{"type":"string","enum":["interval","daily","weekly"],"description":"how it repeats"},
				"interval_sec":{"type":"integer","description":"interval schedules: seconds between runs"},
				"run_at_minute":{"type":"integer","description":"daily schedules: minutes since midnight UTC (0-1439)"},
				"days_of_week":{"type":"integer","description":"weekly schedules: bitmask, bit 0 = Sunday"},
				"run_at_minutes":{"type":"array","items":{"type":"integer"},"description":"weekly schedules: times of day, minutes since midnight UTC"}
			},"required":["name","shell","script","schedule_kind"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "propose_check",
			Description: "Propose a monitoring check for one device or for a policy. Created DISABLED: it monitors nothing until a person reviews and enables it in the console. Types: disk_space/memory need warn_threshold above err_threshold (percent free); cpu_load needs err_threshold above warn_threshold; ping needs config {\"host\":\"...\"}; winservice needs config {\"service\":\"...\"}; script needs config {\"shell\":\"...\",\"script\":\"...\"}.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string","description":"target device id; give this OR policy_id, never both"},
				"policy_id":{"type":"string","description":"target policy id"},
				"type":{"type":"string","enum":["disk_space","memory","cpu_load","ping","winservice","script"],"description":"what to monitor"},
				"name":{"type":"string","description":"short name for the check"},
				"config":{"type":"object","description":"type-specific settings"},
				"warn_threshold":{"type":"number","description":"percent threshold for the warning level"},
				"err_threshold":{"type":"number","description":"percent threshold for the failing level"},
				"interval_sec":{"type":"integer","description":"seconds between runs (30-86400, default 300)"}
			},"required":["type","name"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "propose_policy",
			Description: "Propose a new policy (a named bundle of checks and tasks that can then be assigned to clients, sites, departments or devices). The policy is created empty and marked as awaiting review; attach tasks and checks to it with propose_task/propose_check. Only unrestricted administrators may create policies — a user confined to one client cannot.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"name":{"type":"string","description":"policy name"},
				"description":{"type":"string","description":"what this policy is for"}
			},"required":["name"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "install_patches",
			Description: "Install patches on a device that a PERSON has already approved. Name the exact identifiers (from list_patches). Anything not already approved is skipped — you cannot approve a patch, and there is no tool to do so: approval is always a human decision made in the console. If the user asks you to install something unapproved, tell them it needs their approval first.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string","description":"target device id"},
				"identifiers":{"type":"array","items":{"type":"string"},"description":"exact patch identifiers to install (from list_patches)"}
			},"required":["agent_id","identifiers"],"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "list_tasks",
			Description: "List the scheduled tasks defined on one device or on one policy, including whether each is enabled and whether it is an AI proposal still awaiting a person's review.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string"},"policy_id":{"type":"string"}
			},"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "list_checks",
			Description: "List the monitoring checks defined on one device or on one policy, including whether each is enabled and whether it is an AI proposal still awaiting a person's review.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string"},"policy_id":{"type":"string"}
			},"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "list_policies",
			Description: "List the policies visible to the caller, with id and name. Use this to find a policy_id before proposing a task or check against it.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "query_audit",
			Description: "Search the audit log: who did what, and when. Read-only — there is no tool to edit or purge it. DEFAULTS TO COUNTS: mode=\"summary\" returns totals by actor and by action family, computed in the database, and is the right call for \"what happened this week\" or \"has anyone touched X\". Use mode=\"entries\" only when you need to read individual records, and narrow it first — the trail is large and reading it wholesale produces confident guesses instead of answers. With no date given, it looks at the last 7 days. To hand the operator the full set, use export_audit instead of paging.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"mode":{"type":"string","enum":["summary","entries"],"description":"summary (default) returns counts; entries returns individual records"},
				"actor":{"type":"string","description":"exact actor, from a summary's by_actor"},
				"action":{"type":"string","description":"action or family prefix, e.g. \"job\" or \"job.approve\""},
				"target":{"type":"string","description":"exact id of the device, job or user acted on"},
				"since":{"type":"string","description":"RFC3339 start, e.g. 2026-08-01T00:00:00Z"},
				"until":{"type":"string","description":"RFC3339 end, exclusive"}
			},"additionalProperties":false}`),
		}},
		{Type: "function", Function: FunctionDef{
			Name:        "export_audit",
			Description: "Produce a CSV of audit entries and return a download link for the operator. Use this whenever they want the DATA rather than an answer about it — the rows stream from the database straight to their disk, so nothing is truncated, nothing enters this conversation, and the file contains what the database holds rather than what you recalled. Takes the same filters as query_audit. The link expires and works only for the account that asked.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"actor":{"type":"string"},
				"action":{"type":"string","description":"action or family prefix"},
				"target":{"type":"string"},
				"since":{"type":"string","description":"RFC3339 start"},
				"until":{"type":"string","description":"RFC3339 end, exclusive"}
			},"additionalProperties":false}`),
		}},
	}
	return append(append(base, highLevelTools()...), deviceToolDefs...)
}

func ToolsForPermissions(perms map[string]bool) []Tool {
	tools := Tools()
	if perms == nil {
		return tools
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		permission := toolPermission(t.Function.Name)
		if permission == "" || perms[permission] {
			out = append(out, t)
		}
	}
	return out
}

func toolPermission(name string) string {
	if highLevelNames[name] {
		return models.PermJobsCreate
	}
	if permission, ok := deviceToolPermission(name); ok {
		return permission
	}
	switch name {
	case "list_devices", "get_inventory":
		return models.PermDevicesView
	case "list_patches":
		return models.PermPatchesView
	case "propose_script", "propose_bulk_deploy":
		return models.PermJobsCreate
	case "cancel_job":
		return models.PermJobsManage
	case "get_job_status":
		return models.PermJobsView
	case "fleet_summary":
		return models.PermDevicesView
	case "inventory_report", "export_inventory":
		return models.PermReportsView
	case "propose_task":
		return models.PermTasksManage
	case "propose_check":
		return models.PermChecksManage
	case "propose_policy":
		return models.PermPoliciesManage
	case "install_patches":
		return models.PermPatchesManage
	case "list_tasks":
		return models.PermTasksView
	case "list_checks":
		return models.PermChecksView
	case "list_policies":
		return models.PermPoliciesView
	case "query_audit", "export_audit":
		return models.PermAuditView
	default:
		return ""
	}
}

// Execute dispatches a single tool call and returns its JSON output.
func Execute(ctx context.Context, tc ToolContext, name string, args json.RawMessage) ToolResult {
	if permission := toolPermission(name); permission != "" && !tc.hasPermission(permission) {
		return errResult("missing permission: " + permission)
	}
	if res, ok := executeHighLevel(ctx, tc, name, args); ok {
		return res
	}
	if res, ok := executeDeviceTool(ctx, tc, name, args); ok {
		return res
	}
	switch name {
	case "list_devices":
		return jsonResult(listDevices(ctx, tc))
	case "get_inventory":
		return jsonResult(getInventory(ctx, tc, args))
	case "list_patches":
		return jsonResult(listPatches(ctx, tc, args))
	case "propose_script":
		return proposeScript(ctx, tc, args)
	case "propose_bulk_deploy":
		return proposeBulkDeploy(ctx, tc, args)
	case "get_job_status":
		return jsonResult(getJobStatus(ctx, tc, args))
	case "cancel_job":
		return jsonResult(cancelJobTool(ctx, tc, args))
	case "fleet_summary":
		return jsonResult(fleetSummary(ctx, tc))
	case "inventory_report":
		return jsonResult(inventoryReport(ctx, tc, args))
	case "propose_task":
		return jsonResult(proposeTask(ctx, tc, args))
	case "propose_check":
		return jsonResult(proposeCheck(ctx, tc, args))
	case "propose_policy":
		return jsonResult(proposePolicy(ctx, tc, args))
	case "install_patches":
		return jsonResult(installPatches(ctx, tc, args))
	case "list_tasks":
		return jsonResult(listTasksTool(ctx, tc, args))
	case "list_checks":
		return jsonResult(listChecksTool(ctx, tc, args))
	case "list_policies":
		return jsonResult(listPoliciesTool(ctx, tc))
	case "query_audit":
		return jsonResult(queryAudit(ctx, tc, args))
	case "export_audit":
		return jsonResult(exportAudit(ctx, tc, args))
	case "export_inventory":
		return jsonResult(exportInventory(ctx, tc, args))
	default:
		return ToolResult{Output: fmt.Sprintf(`{"error":"unknown tool %q"}`, name)}
	}
}

func jsonResult(v any, err error) ToolResult {
	if err != nil {
		return ToolResult{Output: fmt.Sprintf(`{"error":%q}`, err.Error())}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ToolResult{Output: fmt.Sprintf(`{"error":%q}`, err.Error())}
	}
	return ToolResult{Output: string(b)}
}

func listDevices(ctx context.Context, tc ToolContext) (any, error) {
	var agents []models.Agent
	var err error
	if tc.ScopeUserID != "" {
		agents, err = tc.Store.ListAgentsForUser(ctx, tc.ScopeUserID)
	} else {
		agents, err = tc.Store.ListAgents(ctx)
	}
	if err != nil {
		return nil, err
	}
	// Site and department ids let the model select "every device of site X"
	// and resolve them against list_org, rather than asking for ids by hand.
	type dev struct {
		ID       string  `json:"id"`
		Hostname string  `json:"hostname"`
		OS       string  `json:"os"`
		Arch     string  `json:"arch"`
		Status   string  `json:"status"`
		SiteID   *string `json:"site_id,omitempty"`
		GroupID  *string `json:"group_id,omitempty"`
	}
	out := make([]dev, 0, len(agents))
	for _, a := range agents {
		out = append(out, dev{a.ID, a.Hostname, a.OS, a.Arch, a.Status, a.SiteID, a.GroupID})
	}
	return map[string]any{"devices": out}, nil
}

func getInventory(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if !tc.allows(ctx, in.AgentID) {
		return nil, fmt.Errorf("device is outside your access scope")
	}
	inv, err := tc.Store.GetInventory(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func listPatches(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if !tc.allows(ctx, in.AgentID) {
		return nil, fmt.Errorf("device is outside your access scope")
	}
	ps, err := tc.Store.ListAgentPatches(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"patches": ps}, nil
}

func proposeScript(ctx context.Context, tc ToolContext, args json.RawMessage) ToolResult {
	var in struct {
		AgentID     string `json:"agent_id"`
		Shell       string `json:"shell"`
		Script      string `json:"script"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Output: fmt.Sprintf(`{"error":%q}`, err.Error())}
	}
	return proposeJob(ctx, tc, in.AgentID, in.Shell, in.Script, in.Description)
}

// proposeJob creates an approval-gated job from a tool and returns the standard
// "awaiting approval" tool result. Shared by propose_script and every
// high-level tool so the guardrail is identical across all of them.
func proposeJob(ctx context.Context, tc ToolContext, agentID, shell, script, desc string) ToolResult {
	if !tc.hasPermission(models.PermJobsCreate) {
		return errResult("missing permission: " + models.PermJobsCreate)
	}
	if !tc.allows(ctx, agentID) {
		return errResult("device is outside your access scope")
	}
	job, err := tc.Jobs.Propose(ctx, agentID, shell, script, desc, "ai:"+tc.Actor, true)
	if err != nil {
		return errResult(err.Error())
	}
	out, _ := json.Marshal(map[string]any{
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "Job created and is AWAITING HUMAN APPROVAL. It will not run until a person approves it in the UI.",
	})
	return ToolResult{Output: string(out), CreatedJobIDs: []string{job.ID}}
}

// proposeBulkDeploy proposes a mass deployment: it resolves the target devices
// (agent_ids ∪ site ∪ department), keeps only those in the caller's access
// scope, and hands them to jobs.ProposeBulk, which splits them BY COMPANY into
// one approval-gated batch per company. Nothing runs until a human approves.
func proposeBulkDeploy(ctx context.Context, tc ToolContext, args json.RawMessage) ToolResult {
	var in struct {
		AgentIDs    []string `json:"agent_ids"`
		SiteID      string   `json:"site_id"`
		GroupID     string   `json:"group_id"`
		Shell       string   `json:"shell"`
		Script      string   `json:"script"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(err.Error())
	}
	if in.Script == "" {
		return errResult("script is required")
	}
	if in.Shell == "" {
		in.Shell = models.ShellBash
	}
	targets := map[string]bool{}
	for _, id := range in.AgentIDs {
		if id != "" {
			targets[id] = true
		}
	}
	if in.GroupID != "" {
		ags, err := tc.Store.ListAgentsByGroup(ctx, in.GroupID)
		if err != nil {
			return errResult(err.Error())
		}
		for _, a := range ags {
			targets[a.ID] = true
		}
	}
	if in.SiteID != "" {
		ags, err := tc.Store.ListAgentsBySite(ctx, in.SiteID)
		if err != nil {
			return errResult(err.Error())
		}
		for _, a := range ags {
			targets[a.ID] = true
		}
	}
	// Keep only in-scope devices.
	if tc.ScopeUserID != "" {
		ags, err := tc.Store.ListAgentsForUser(ctx, tc.ScopeUserID)
		if err != nil {
			return errResult(err.Error())
		}
		allowed := make(map[string]bool, len(ags))
		for _, a := range ags {
			allowed[a.ID] = true
		}
		for id := range targets {
			if !allowed[id] {
				delete(targets, id)
			}
		}
	}
	if len(targets) == 0 {
		return errResult("no in-scope target devices resolved")
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	batches, err := tc.Jobs.ProposeBulk(ctx, ids, in.Shell, in.Script, in.Description, "ai:"+tc.Actor)
	if err != nil {
		return errResult(err.Error())
	}
	var jobIDs []string
	for _, b := range batches {
		jobIDs = append(jobIDs, b.JobIDs...)
	}
	out, _ := json.Marshal(map[string]any{
		"batches": len(batches),
		"jobs":    len(jobIDs),
		"message": fmt.Sprintf("Prepared a bulk deployment: %d job(s) split into %d company batch(es). ALL are AWAITING HUMAN APPROVAL — nothing runs until a person approves each batch in the UI.", len(jobIDs), len(batches)),
	})
	return ToolResult{Output: string(out), CreatedJobIDs: jobIDs}
}

// maxReportRows caps the rows an inventory_report returns to the model so a
// large fleet (machines × packages) can't blow the context window. The full
// data is always available via the Reports tab / /api/reports CSV export.
const maxReportRows = 1000

// inventoryReport runs a deterministic, scope-aware fleet inventory report,
// reusing the exact store functions the REST /api/reports endpoints use.
func inventoryReport(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		ReportType string `json:"report_type"`
		ScopeType  string `json:"scope_type"`
		ScopeID    string `json:"scope_id"`
		Mode       string `json:"mode"`
		GroupBy    string `json:"group_by"`
		Match      string `json:"match"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	f := store.ReportFilter{ScopeUserID: tc.ScopeUserID}
	switch in.ScopeType {
	case "client", "site", "group":
		if in.ScopeID == "" {
			return nil, fmt.Errorf("scope_id is required when scope_type is set")
		}
		f.ScopeType, f.ScopeID = in.ScopeType, in.ScopeID
	case "":
		// whole in-scope fleet
	default:
		return nil, fmt.Errorf("unknown scope_type %q", in.ScopeType)
	}
	// Counts by default. Rows are the exception, not the norm: most questions
	// asked of an inventory are "how many" and "which kinds", and answering
	// those with a thousand rows spends a context window to arrive at a number
	// the database already had — and one the model may get wrong on the way.
	if in.Mode == "" || in.Mode == "summary" {
		groups, more, err := tc.Store.InventorySummary(ctx, f, in.ReportType, in.GroupBy, in.Match)
		if err != nil {
			return nil, err
		}
		grouped := in.GroupBy
		if grouped == "" {
			grouped = store.DefaultGroupBy[in.ReportType]
		}
		out := map[string]any{
			"report":        in.ReportType,
			"grouped_by":    grouped,
			"groups":        groups,
			"group_options": store.GroupOptions(in.ReportType),
			"note": "counts computed in the database, ordered by how many devices each " +
				"covers. `machines` is distinct devices; `rows` counts matching records. " +
				"Use match to ask about one item, mode=\"rows\" to read records, or " +
				"export_inventory for the full set as a CSV.",
		}
		if more > 0 {
			out["truncated"] = true
			out["note"] = fmt.Sprintf("showing the %d largest groups; there are more in the tail. "+
				"Use match to ask about a specific one.", len(groups))
		}
		return out, nil
	}
	if in.Mode != "rows" {
		return nil, fmt.Errorf("mode must be \"summary\" or \"rows\"")
	}

	switch in.ReportType {
	case "software":
		rows, err := tc.Store.SoftwareReport(ctx, f)
		if err != nil {
			return nil, err
		}
		return cappedRows(rows), nil
	case "hardware":
		rows, err := tc.Store.HardwareReport(ctx, f)
		if err != nil {
			return nil, err
		}
		return cappedRows(rows), nil
	case "machines":
		rows, err := tc.Store.MachinesReport(ctx, f)
		if err != nil {
			return nil, err
		}
		return cappedRows(rows), nil
	default:
		return nil, fmt.Errorf("unknown report_type %q", in.ReportType)
	}
}

// cappedRows wraps report rows for the model, truncating to maxReportRows and
// flagging when it did so.
func cappedRows[T any](rows []T) map[string]any {
	out := map[string]any{"total": len(rows)}
	if len(rows) > maxReportRows {
		out["rows"] = rows[:maxReportRows]
		out["truncated"] = true
		out["note"] = fmt.Sprintf(
			"showing the first %d of %d rows. Call export_inventory with the same filters "+
				"for the complete set as a CSV the operator downloads — do not ask for more rows here.",
			maxReportRows, len(rows))
	} else {
		out["rows"] = rows
	}
	return out
}

func getJobStatus(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	job, err := tc.Store.GetJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	if !tc.allows(ctx, job.AgentID) {
		return nil, fmt.Errorf("job is outside your access scope")
	}
	return job, nil
}

func cancelJobTool(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if tc.ScopeUserID != "" {
		job, err := tc.Store.GetJob(ctx, in.JobID)
		if err != nil {
			return nil, err
		}
		if !tc.allows(ctx, job.AgentID) {
			return nil, fmt.Errorf("job is outside your access scope")
		}
	}
	return tc.Jobs.Cancel(ctx, in.JobID, "ai:"+tc.Actor)
}

func fleetSummary(ctx context.Context, tc ToolContext) (any, error) {
	var agents []models.Agent
	var err error
	if tc.ScopeUserID != "" {
		agents, err = tc.Store.ListAgentsForUser(ctx, tc.ScopeUserID)
	} else {
		agents, err = tc.Store.ListAgents(ctx)
	}
	if err != nil {
		return nil, err
	}
	byOS := map[string]int{}
	byStatus := map[string]int{}
	for _, a := range agents {
		byOS[a.OS]++
		byStatus[a.Status]++
	}
	var recent []models.Job
	if tc.hasPermission(models.PermJobsView) {
		var err error
		recent, err = tc.Store.ListJobs(ctx, "", 25)
		if err != nil {
			return nil, err
		}
	}
	type jobSummary struct {
		ID          string `json:"id"`
		AgentID     string `json:"agent_id"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	// When scoped, only surface jobs for in-scope agents.
	var allowed map[string]bool
	if tc.ScopeUserID != "" {
		allowed = make(map[string]bool, len(agents))
		for _, a := range agents {
			allowed[a.ID] = true
		}
	}
	js := make([]jobSummary, 0, len(recent))
	if tc.hasPermission(models.PermJobsView) {
		for _, j := range recent {
			if allowed != nil && !allowed[j.AgentID] {
				continue
			}
			js = append(js, jobSummary{j.ID, j.AgentID, j.Description, j.Status})
		}
	}
	return map[string]any{
		"total_devices":    len(agents),
		"devices_by_os":    byOS,
		"devices_by_state": byStatus,
		"recent_jobs":      js,
	}, nil
}
