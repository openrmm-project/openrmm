// Package models defines the core domain types persisted by the RMM backend.
package models

import (
	"encoding/json"
	"strconv"
	"strings"
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
	// JobDeviceQuery runs one of the agent's built-in read-only queries
	// (protocol.DeviceQuery*). Its Script holds the query request as JSON; no
	// shell is involved and no code is sent to the device.
	JobDeviceQuery = "device_query"
)

// How a job came to run without a person approving it at the time
// (docs/23-ai-autonomy.md). Empty for every other job.
const (
	AutonomyNone        = ""
	AutonomyQuery       = "query"       // a read-only device query the user's role allows
	AutonomyPreapproved = "preapproved" // a library script an admin released in advance
)

// MinAgentVersionAutonomy is the first agent release that answers device
// queries and passes a script's parameters through its environment. An older
// agent would run a parameterised script without its parameters, so the server
// refuses both up front.
const MinAgentVersionAutonomy = "0.10.0"

// MinAgentVersionScriptVars is the first agent that reads the vars field of a
// job and sets those environment variables. An older agent would run the
// script without them.
const MinAgentVersionScriptVars = "0.11.0"

// Supported script shells.
const (
	ShellBash       = "bash"
	ShellSh         = "sh"
	ShellPowerShell = "powershell"
	ShellCmd        = "cmd"
	ShellPython     = "python"
)

// MinAgentVersionPython is the first agent release that can run a Python
// script. Older agents answer "unsupported shell", which is a clear failure but
// only after the job has been dispatched and waited on — so the server refuses
// the job up front instead. Raise this only when the execution contract changes.
const MinAgentVersionPython = "0.4.9"

// SupportedShell reports whether the agent has an explicit execution path for a
// shell. Unknown values must never silently fall back to Bash.
func SupportedShell(shell string) bool {
	switch shell {
	case ShellBash, ShellSh, ShellPowerShell, ShellCmd, ShellPython:
		return true
	default:
		return false
	}
}

// MinAgentVersionFor returns the agent version a shell requires, or "" when
// every agent that can report a version can run it.
//
// Only shells added after the first release need a floor: bash, sh, powershell
// and cmd have been executable since the beginning, so gating them would refuse
// work that would in fact succeed.
func MinAgentVersionFor(shell string) string {
	if shell == ShellPython {
		return MinAgentVersionPython
	}
	return ""
}

// AgentVersionAtLeast compares a reported agent version against a minimum.
//
// An unparseable or missing version reports false: agents self-report this
// string, so treating "unknown" as new enough would let exactly the endpoints
// whose state is least trustworthy past a gate that exists to protect them.
func AgentVersionAtLeast(reported, minimum string) bool {
	got, ok := parseVersion(reported)
	if !ok {
		return false
	}
	want, ok := parseVersion(minimum)
	if !ok {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}

// parseVersion splits a plain major.minor.patch, tolerating a leading "v".
// Anything else — a pre-release suffix, a build tag, a short version — is not
// something to guess at, so it fails rather than being partially understood.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Agent is a managed endpoint.
type Agent struct {
	// MonitoringScopeSince is a server-side provenance marker, never client input.
	MonitoringScopeSince time.Time  `json:"-"`
	ID                   string     `json:"id"`
	Hostname             string     `json:"hostname"`
	Description          string     `json:"description"`
	Note                 string     `json:"note"`
	OS                   string     `json:"os"`
	Arch                 string     `json:"arch"`
	Version              string     `json:"version"`
	Status               string     `json:"status"`
	SiteID               *string    `json:"site_id,omitempty"`
	GroupID              *string    `json:"group_id,omitempty"`
	Tags                 []string   `json:"tags"`
	MeshID               *string    `json:"mesh_id,omitempty"`
	LastSeen             *time.Time `json:"last_seen,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	// EnrollTokenID is the install token that admitted the device, so the
	// devices a leaked mass-deployment token enrolled can be found and removed.
	EnrollTokenID *string `json:"enroll_token_id,omitempty"`
	// OSRole is the role detected from the device's inventory (Windows product
	// type or edition name; macOS is a workstation; Linux has none).
	// RoleOverride is an administrator's correction and wins over it. Role is
	// the one in effect; policy assignments filter on it.
	OSRole       string `json:"os_role"`
	RoleOverride string `json:"role_override"`
	Role         string `json:"role"`
	// LoggedInUser is the user of the active interactive session as the agent
	// last reported it (DOMAIN\user on Windows), "" when no one is logged in;
	// LoggedInUserSince is when it last changed.
	LoggedInUser      string     `json:"logged_in_user"`
	LoggedInUserSince *time.Time `json:"logged_in_user_since,omitempty"`
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
	// Autonomy says why the job ran without approval at the time: a device
	// query its creator's role allows, or a script released in advance. Empty
	// for every other job.
	Autonomy string `json:"autonomy,omitempty"`
	// ScriptID and Params are set for a released library script: the script it
	// was built from, and the declared parameters, which reach the device as
	// environment variables and never as script text.
	ScriptID *string           `json:"script_id,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	// Variables are the script-variable names snapshotted when the job was
	// created. The values are not stored on the job.
	Variables []string `json:"variables,omitempty"`
}

// EnrollToken authorizes a new agent to register.
// The bounds on an install token. One token can enrol a whole fleet (mass
// deployment through a GPO, Intune or a script), so it may be used many times
// and for weeks, but never without end: a token that leaked stays usable only
// until it expires, is used up, or is revoked.
const (
	MaxEnrollTokenUses     = 10000
	MaxEnrollTokenTTL      = 90 * 24 * time.Hour
	DefaultEnrollTokenTTL  = 24 * time.Hour
	DefaultEnrollTokenUses = 1
)

type EnrollToken struct {
	ID string `json:"id"`
	// Token is the secret itself, present only in the answer that creates it:
	// the database keeps its SHA-256, like every other credential.
	Token       string     `json:"token,omitempty"`
	Description string     `json:"description"`
	UsesLeft    int        `json:"uses_left"`
	UsesTotal   int        `json:"uses_total"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	// Where the token places the agent. The command an operator copies does not
	// describe the placement — the token does — so a token bound to a department
	// is how a machine enrols straight into one.
	SiteID    *string `json:"site_id,omitempty"`
	GroupID   *string `json:"group_id,omitempty"`
	CreatedBy string  `json:"created_by"`
	// CreatedByName is the creator's username, in a listing.
	CreatedByName string     `json:"created_by_name,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	RevokedBy     *string    `json:"revoked_by,omitempty"`
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
	ViewerTicket string `json:"viewer_ticket,omitempty"`
	AgentTicket  string `json:"-"`
	Token        string `json:"-"` // retained for external-provider internals
	ConnectURL   string `json:"connect_url"`
	// MatchedBy says how an external provider finds the device: "node" (a
	// linked id) or "hostname" (a name another computer could share). It is
	// recorded in the remote.start audit entry and not stored with the session.
	MatchedBy string `json:"matched_by,omitempty"`
	// MeshAccount says which MeshCentral account a login token signs in as:
	// "personal" (the caller's own) or "shared"; empty without a login token.
	// Like MatchedBy, it goes to the audit entry, with the account's name, and
	// is not stored with the session.
	MeshAccount string    `json:"mesh_account,omitempty"`
	MeshUser    string    `json:"-"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
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
