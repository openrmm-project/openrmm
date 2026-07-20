// Package models defines the core domain types persisted by the RMM backend.
package models

import (
	"encoding/json"
	"time"
)

// Agent status values.
const (
	AgentOnline  = "online"
	AgentOffline = "offline"
	AgentRevoked = "revoked"
)

// Job lifecycle status values.
const (
	JobAwaitingApproval = "awaiting_approval" // proposed (e.g. by the AI), not yet allowed to run
	JobPending          = "pending"           // approved, queued, waiting for the agent
	JobSent             = "sent"              // delivered to a connected agent
	JobRunning          = "running"
	JobCompleted        = "completed"
	JobFailed           = "failed"
	JobRejected         = "rejected" // a human declined the proposal
)

// Job type values.
const (
	JobRunScript = "run_script"
)

// Supported script shells.
const (
	ShellBash       = "bash"
	ShellSh         = "sh"
	ShellPowerShell = "powershell"
	ShellCmd        = "cmd"
)

// SupportedShell reports whether the agent has an explicit execution path for a
// shell. Unknown values must never silently fall back to Bash.
func SupportedShell(shell string) bool {
	switch shell {
	case ShellBash, ShellSh, ShellPowerShell, ShellCmd:
		return true
	default:
		return false
	}
}

// Agent is a managed endpoint.
type Agent struct {
	ID          string     `json:"id"`
	Hostname    string     `json:"hostname"`
	Description string     `json:"description"`
	Note        string     `json:"note"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	Version     string     `json:"version"`
	Status      string     `json:"status"`
	SiteID      *string    `json:"site_id,omitempty"`
	GroupID     *string    `json:"group_id,omitempty"`
	Tags        []string   `json:"tags"`
	MeshID      *string    `json:"mesh_id,omitempty"`
	LastSeen    *time.Time `json:"last_seen,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Job is a unit of work targeted at an agent (currently: run a script).
type Job struct {
	ID               string    `json:"id"`
	AgentID          string    `json:"agent_id"`
	Type             string    `json:"type"`
	Description      string    `json:"description"`
	Shell            string    `json:"shell"`
	Script           string    `json:"script"`
	Status           string    `json:"status"`
	RequiresApproval bool      `json:"requires_approval"`
	CreatedBy        string    `json:"created_by"`
	ApprovedBy       *string   `json:"approved_by,omitempty"`
	TaskID           *string   `json:"task_id,omitempty"`
	BatchID          *string   `json:"batch_id,omitempty"`
	ExitCode         *int      `json:"exit_code,omitempty"`
	Stdout           string    `json:"stdout"`
	Stderr           string    `json:"stderr"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// EnrollToken authorizes a new agent to register.
type EnrollToken struct {
	ID          string     `json:"id"`
	Token       string     `json:"token"`
	Description string     `json:"description"`
	UsesLeft    int        `json:"uses_left"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Inventory is the latest collected state of an agent.
type Inventory struct {
	AgentID     string          `json:"agent_id"`
	Data        json.RawMessage `json:"data"`
	CollectedAt time.Time       `json:"collected_at"`
}

// RemoteSession brokers a remote-access connection to an agent.
type RemoteSession struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	// Tickets are returned only once when a native session is brokered. Their
	// hashes, not raw values, are persisted by the server.
	ViewerTicket string    `json:"viewer_ticket,omitempty"`
	AgentTicket  string    `json:"-"`
	Token        string    `json:"-"` // retained for external-provider internals
	ConnectURL   string    `json:"connect_url"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// AuditEntry is an immutable record of a sensitive action.
type AuditEntry struct {
	ID        string          `json:"id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"created_at"`
}

// ChatSession groups a conversation with the AI.
type ChatSession struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// ChatMessage is one message in a chat session. tool_calls / tool_call_id mirror
// the OpenAI-compatible chat schema so history can be replayed to the model.
type ChatMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}
