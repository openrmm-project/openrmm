package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// ToolContext carries the dependencies and identity a tool needs to run.
type ToolContext struct {
	Store       *store.Store
	Jobs        *jobs.Service
	Actor       string // who is driving the chat (recorded as job creator / audit actor)
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
			Description: "Generate a FLEET-WIDE inventory report aggregated across many devices, optionally narrowed to one client, site or department (group). report_type 'software' lists installed packages per machine; 'hardware' lists each machine's hardware profile; 'machines' lists the fleet (parque) with status. Use THIS (not get_inventory) when the user asks for an inventory of a client/site/department or of 'all machines'. Read-only — it never changes anything. For one specific named device, use get_inventory instead.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"report_type":{"type":"string","enum":["software","hardware","machines"],"description":"which report to produce"},
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
	}
	return append(base, highLevelTools()...)
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
	case "inventory_report":
		return models.PermReportsView
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
	type dev struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Status   string `json:"status"`
	}
	out := make([]dev, 0, len(agents))
	for _, a := range agents {
		out = append(out, dev{a.ID, a.Hostname, a.OS, a.Arch, a.Status})
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
		out["note"] = fmt.Sprintf("showing the first %d of %d rows; use the Reports tab to export the full CSV", maxReportRows, len(rows))
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
