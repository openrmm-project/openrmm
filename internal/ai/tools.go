package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/store"
)

// ToolContext carries the dependencies and identity a tool needs to run.
type ToolContext struct {
	Store *store.Store
	Jobs  *jobs.Service
	Actor string // who is driving the chat (recorded as job creator / audit actor)
}

// ToolResult is returned to the chat loop after a tool runs.
type ToolResult struct {
	Output        string   // JSON string fed back to the model
	CreatedJobIDs []string // jobs proposed during this call (surfaced to the UI)
}

// Tools returns the catalog advertised to the model.
//
// Design note: the model can READ freely (list_devices, get_inventory,
// get_job_status, fleet_summary) but the only WRITE tool, propose_script, never
// executes anything — it creates a job in 'awaiting_approval'. A human approves
// in the UI before it ever reaches an endpoint. This is the core guardrail.
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
			Name: "propose_script",
			Description: "Propose a script to run on a device. This DOES NOT execute it: it creates a job that a human must approve first. Write the full script yourself for the device's OS. Pick the correct shell.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"agent_id":{"type":"string","description":"target device id"},
				"shell":{"type":"string","enum":["bash","sh","powershell","cmd"],"description":"interpreter for the script"},
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
	}
	return append(base, highLevelTools()...)
}

// Execute dispatches a single tool call and returns its JSON output.
func Execute(ctx context.Context, tc ToolContext, name string, args json.RawMessage) ToolResult {
	if res, ok := executeHighLevel(ctx, tc, name, args); ok {
		return res
	}
	switch name {
	case "list_devices":
		return jsonResult(listDevices(ctx, tc))
	case "get_inventory":
		return jsonResult(getInventory(ctx, tc, args))
	case "propose_script":
		return proposeScript(ctx, tc, args)
	case "get_job_status":
		return jsonResult(getJobStatus(ctx, tc, args))
	case "fleet_summary":
		return jsonResult(fleetSummary(ctx, tc))
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
	agents, err := tc.Store.ListAgents(ctx)
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
	inv, err := tc.Store.GetInventory(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	return inv, nil
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

func getJobStatus(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	return tc.Store.GetJob(ctx, in.JobID)
}

func fleetSummary(ctx context.Context, tc ToolContext) (any, error) {
	agents, err := tc.Store.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	byOS := map[string]int{}
	byStatus := map[string]int{}
	for _, a := range agents {
		byOS[a.OS]++
		byStatus[a.Status]++
	}
	recent, err := tc.Store.ListJobs(ctx, "", 25)
	if err != nil {
		return nil, err
	}
	type jobSummary struct {
		ID          string `json:"id"`
		AgentID     string `json:"agent_id"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	js := make([]jobSummary, 0, len(recent))
	for _, j := range recent {
		js = append(js, jobSummary{j.ID, j.AgentID, j.Description, j.Status})
	}
	return map[string]any{
		"total_devices":   len(agents),
		"devices_by_os":   byOS,
		"devices_by_state": byStatus,
		"recent_jobs":     js,
	}, nil
}
