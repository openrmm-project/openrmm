package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
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
1. You can READ freely, but you can NEVER execute anything directly. The only way to
   act on a device is propose_script, which creates a job that a HUMAN must approve.
2. Always identify the correct target device id (use list_devices) before proposing.
3. Write complete, correct scripts for the device's actual OS and pick the right shell
   (powershell/cmd for Windows, bash/sh for Linux/macOS).
4. After proposing a script, clearly tell the user it is awaiting their approval and
   show what the script will do. Never claim something ran unless a job result confirms it.
5. Prefer idempotent, safe scripts. Avoid destructive operations; if one is unavoidable,
   call it out explicitly.`

// Engine runs the chat assistant against a persisted conversation.
type Engine struct {
	client *Client
	store  *store.Store
	jobs   *jobs.Service
}

// NewEngine builds the chat engine.
func NewEngine(c *Client, s *store.Store, j *jobs.Service) *Engine {
	return &Engine{client: c, store: s, jobs: j}
}

// Reply is the outcome of one user turn.
type Reply struct {
	Content       string   `json:"content"`
	CreatedJobIDs []string `json:"created_job_ids"`
}

// Respond appends the user's message, runs the tool-calling loop, persists every
// step, and returns the assistant's final text plus any jobs it proposed.
func (e *Engine) Respond(ctx context.Context, sessionID, userContent, actor string) (*Reply, error) {
	if err := e.store.AddChatMessage(ctx, &models.ChatMessage{
		SessionID: sessionID, Role: "user", Content: userContent,
	}); err != nil {
		return nil, err
	}

	history, err := e.loadHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	tc := ToolContext{Store: e.store, Jobs: e.jobs, Actor: actor}
	var createdJobs []string

	for i := 0; i < maxIters; i++ {
		msg, err := e.client.Chat(ctx, history, Tools())
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
