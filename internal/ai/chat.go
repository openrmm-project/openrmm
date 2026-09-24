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

// maxIters caps the tool-calling loop to avoid runaway model loops.
const maxIters = 8

const systemPrompt = `You are the AI operator of a self-hosted RMM (remote monitoring & management) platform.
You help an IT administrator manage fleets of computers using natural language.

Capabilities (via tools):
- Inspect devices and their inventory.
- Propose scripts to install software, configure printers, remediate issues, etc.
- Check job results and generate reports from fleet data.

Hard rules:
1. You can only read through the tools made available to you for this user's
   permissions and access scope. You can NEVER execute anything directly. The only way to
   act is to PROPOSE: propose_script (one device) or propose_bulk_deploy (the same
   script across MANY devices at once — e.g. "install X on every device" or "deploy
   to all companies"), each of which creates approval-gated job(s) a HUMAN must
   approve. propose_bulk_deploy automatically splits the targets into ONE BATCH PER
   COMPANY — a deployment never spans companies.
2. Always identify the correct target device id (use list_devices) before proposing.
3. Prefer the dedicated high-level tools (install_software, uninstall_software,
   restart_service, install_printer, flush_dns, reboot_device) over writing a raw
   script when one fits — they generate the correct, vetted command for the
   device's OS. Use propose_script only for actions without a dedicated tool, and
   then write a complete, correct script for the device's OS with the right shell
   (powershell/cmd for Windows, bash/sh for Linux/macOS). "python" is available on
   either OS, but only where an interpreter is installed and on agents 0.4.9 or
   later — prefer the native shell unless the task genuinely needs Python.
   Moving devices between sites/departments and deleting devices work the same
   way: propose_move_devices and propose_delete_devices only create a proposal a
   HUMAN approves under Devices > Pending operations. Resolve names with list_org
   and list_devices first. Propose a deletion only when the user explicitly asks
   to remove devices, and list which ones.
4. After proposing a script, clearly tell the user it is awaiting their approval and
   show what the script will do. Never claim something ran unless a job result confirms it.
5. Prefer idempotent, safe scripts. Avoid destructive operations; if one is unavoidable,
   call it out explicitly.
6. REPORTS ARE READ-ONLY. To view/list/show/report data (inventory, installed software,
   disks, network, uptime, status, fleet summaries), use the READ tools — get_inventory,
   fleet_summary, list_devices, get_job_status — and present the result directly in THIS
   chat (a Markdown table is great). Do NOT propose a script that writes a file on the
   endpoint just to produce a report. Only propose a script when the user wants to CHANGE
   the machine. If a report genuinely needs a script, make it print to STDOUT (the output
   comes back in the job result) — never write a file unless the user explicitly asks to
   save one on the device. Example: "report installed software on X" -> get_inventory(X)
   and summarize its software list here; never a CSV on disk.
7. DOWNLOADABLE REPORTS: when the user asks for a report/list/export (especially if
   they mention CSV, Excel, spreadsheet, or "download"), render the data as a GitHub
   Markdown table (| col | col |\n| --- | --- |\n| val | val |). The chat UI auto-detects
   the table and shows a "Baixar CSV" button under your message, so the user downloads it
   straight from the chat — no file is written on the endpoint. For very large/precise
   exports you may instead emit a fenced csv code block (triple backtick + "csv"); it is
   offered for download the same way. Keep one table/csv block per report.`

// Engine runs the chat assistant against a persisted conversation.
type Engine struct {
	client  *Client
	store   *store.Store
	jobs    *jobs.Service
	patches *patches.Service
	// Carried so a tool can mint a signed download link rather than reading rows
	// into the conversation.
	cfg       *config.Config
	publicURL string
}

// NewEngine builds the chat engine.
func NewEngine(c *Client, s *store.Store, j *jobs.Service, p *patches.Service, cfg *config.Config) *Engine {
	return &Engine{client: c, store: s, jobs: j, patches: p, cfg: cfg, publicURL: cfg.PublicURL}
}

// UpdateProvider swaps model provider settings for future chat requests.
func (e *Engine) UpdateProvider(baseURL, apiKey, model string) {
	e.client.Update(baseURL, apiKey, model)
}

// Reply is the outcome of one user turn.
type Reply struct {
	Content       string   `json:"content"`
	CreatedJobIDs []string `json:"created_job_ids"`
}

// Respond appends the user's message, runs the tool-calling loop, persists every
// step, and returns the assistant's final text plus any jobs it proposed.
func (e *Engine) Respond(ctx context.Context, sessionID, userContent, actor, scopeUser string, permissions map[string]bool) (*Reply, error) {
	if err := e.store.AddChatMessage(ctx, &models.ChatMessage{
		SessionID: sessionID, Role: "user", Content: userContent,
	}); err != nil {
		return nil, err
	}

	history, err := e.loadHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	tc := ToolContext{Store: e.store, Jobs: e.jobs, Patches: e.patches, Config: e.cfg, PublicURL: e.publicURL, Actor: actor, ScopeUserID: scopeUser, Permissions: permissions}
	tools := ToolsForPermissions(permissions)
	var createdJobs []string

	for i := 0; i < maxIters; i++ {
		msg, err := e.client.Chat(ctx, history, tools)
		if err != nil {
			return nil, err
		}
		// Persist + append the assistant message.
		if err := e.persistAssistant(ctx, sessionID, msg); err != nil {
			return nil, err
		}
		history = append(history, *msg)

		if len(msg.ToolCalls) == 0 {
			return &Reply{Content: msg.Content, CreatedJobIDs: createdJobs}, nil
		}

		// Run each requested tool and feed the results back.
		for _, call := range msg.ToolCalls {
			res := Execute(ctx, tc, call.Function.Name, json.RawMessage(call.Function.Arguments))
			createdJobs = append(createdJobs, res.CreatedJobIDs...)
			toolMsg := Message{Role: "tool", Content: res.Output, ToolCallID: call.ID, Name: call.Function.Name}
			if err := e.store.AddChatMessage(ctx, &models.ChatMessage{
				SessionID: sessionID, Role: "tool", Content: res.Output,
				ToolCallID: call.ID, Name: call.Function.Name,
			}); err != nil {
				return nil, err
			}
			history = append(history, toolMsg)
		}
	}
	return &Reply{Content: "(stopped: reached the maximum number of tool steps)", CreatedJobIDs: createdJobs}, nil
}

// loadHistory rebuilds the model-facing message list from the stored session,
// prefixed with the system prompt.
func (e *Engine) loadHistory(ctx context.Context, sessionID string) ([]Message, error) {
	stored, err := e.store.ListChatMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(stored)+1)
	out = append(out, Message{Role: "system", Content: systemPrompt})
	for _, m := range stored {
		msg := Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		if len(m.ToolCalls) > 0 {
			var calls []ToolCall
			if err := json.Unmarshal(m.ToolCalls, &calls); err == nil {
				msg.ToolCalls = calls
			}
		}
		out = append(out, msg)
	}
	return out, nil
}

func (e *Engine) persistAssistant(ctx context.Context, sessionID string, msg *Message) error {
	cm := &models.ChatMessage{SessionID: sessionID, Role: "assistant", Content: msg.Content}
	if len(msg.ToolCalls) > 0 {
		raw, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return fmt.Errorf("marshal tool_calls: %w", err)
		}
		cm.ToolCalls = raw
	}
	return e.store.AddChatMessage(ctx, cm)
}
