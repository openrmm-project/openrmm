// Package protocol defines the JSON messages exchanged between the agent and the
// server over the persistent WebSocket connection.
package protocol

import "encoding/json"

// Message types (the "type" field of Envelope).
const (
	MsgRegister     = "register"      // agent -> server
	MsgRegistered   = "registered"    // server -> agent
	MsgError        = "error"         // server -> agent
	MsgHeartbeat    = "heartbeat"     // agent -> server
	MsgInventory    = "inventory"     // agent -> server
	MsgJob          = "job"           // server -> agent
	MsgJobCancel    = "job_cancel"    // server -> agent: kill a running job
	MsgJobResult    = "job_result"    // agent -> server
	MsgRemoteStart  = "remote_start"  // server -> agent: open a remote session
	MsgRemoteStatus = "remote_status" // agent -> server: remote session state

	MsgRemoteQuery       = "remote_query"        // server -> agent: read a remote-provider value on demand
	MsgRemoteQueryResult = "remote_query_result" // agent -> server: reply to a remote_query
	MsgRunCheck          = "run_check"           // server -> agent: execute a monitoring check
	MsgCheckResult       = "check_result"        // agent -> server: raw check outcome

	MsgPatchScan    = "patch_scan"    // server -> agent: scan for missing OS patches
	MsgPatchReport  = "patch_report"  // agent -> server: list of missing patches
	MsgPatchInstall = "patch_install" // server -> agent: install approved patches
	MsgPatchResult  = "patch_result"  // agent -> server: per-patch install outcome
	MsgReboot       = "reboot"        // server -> agent: reboot the host
)

// Patch classifications (normalized across OSes).
const (
	PatchSecurity  = "security"
	PatchCritical  = "critical"
	PatchImportant = "important"
	PatchModerate  = "moderate"
	PatchOptional  = "optional"
	PatchOther     = "other"
)

// PatchScanMsg asks the agent to scan for missing OS patches and reply with a
// PatchReportMsg.
type PatchScanMsg struct{}

// PatchItem is one missing patch reported by the agent.
type PatchItem struct {
	Identifier     string   `json:"identifier"` // KB id / package name / advisory id
	Title          string   `json:"title"`
	Classification string   `json:"classification"`
	KB             string   `json:"kb,omitempty"`
	CVEs           []string `json:"cves,omitempty"`
	RebootRequired bool     `json:"reboot_required"`
	Version        string   `json:"version,omitempty"` // available version
}

// PatchReportMsg is the agent's current set of missing patches.
type PatchReportMsg struct {
	OS      string      `json:"os"`
	Patches []PatchItem `json:"patches"`
}

// PatchInstallMsg tells the agent to install patches by identifier (empty list =
// every patch the agent currently sees as available).
type PatchInstallMsg struct {
	RequestID   string   `json:"request_id"`
	Identifiers []string `json:"identifiers"`
}

// PatchResultItem is the per-patch install outcome.
type PatchResultItem struct {
	Identifier     string `json:"identifier"`
	Success        bool   `json:"success"`
	RebootRequired bool   `json:"reboot_required"`
	Error          string `json:"error,omitempty"`
}

// PatchResultMsg reports the outcome of a PatchInstallMsg.
type PatchResultMsg struct {
	RequestID      string            `json:"request_id"`
	Results        []PatchResultItem `json:"results"`
	RebootRequired bool              `json:"reboot_required"`
}

// RebootMsg asks the agent to reboot the host (optionally after a delay).
type RebootMsg struct {
	DelaySeconds int `json:"delay_seconds,omitempty"`
}

// Envelope wraps every WebSocket frame.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// RegisterReq is the first message an agent sends. On first enrollment it carries
// an EnrollToken; afterwards it carries its AgentID + AgentToken.
type RegisterReq struct {
	EnrollToken string `json:"enroll_token,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	AgentToken  string `json:"agent_token,omitempty"`
	Hostname    string `json:"hostname"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Version     string `json:"version"`
}

// RegisterResp returns the durable identity the agent should persist locally.
type RegisterResp struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

// JobMsg is a job pushed to the agent for execution.
type JobMsg struct {
	JobID  string `json:"job_id"`
	Type   string `json:"type"`
	Shell  string `json:"shell"`
	Script string `json:"script"`
}

// JobCancelMsg asks the agent to kill a running job's process.
type JobCancelMsg struct {
	JobID string `json:"job_id"`
}

// JobResultMsg is returned by the agent after running a job.
type JobResultMsg struct {
	JobID    string `json:"job_id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Success  bool   `json:"success"`
}

// InventoryMsg carries a snapshot of agent state.
type InventoryMsg struct {
	Data json.RawMessage `json:"data"`
}

// RemoteStartMsg tells the agent to connect a remote module to the relay using
// the given session token (native provider). Kind selects which module:
//   - "desktop" (or empty): the media sidecar (screen capture + input)
//   - "files": the media sidecar in file-transfer-only mode — NO screen capture
//   - "terminal": an elevated interactive shell on a PTY, run in the agent itself
type RemoteStartMsg struct {
	SessionID  string `json:"session_id"`
	Token      string `json:"token"`
	RelayURL   string `json:"relay_url"`
	Kind       string `json:"kind,omitempty"`
	AllowFiles *bool  `json:"allow_files,omitempty"`
}

// Remote session kinds (RemoteStartMsg.Kind).
const (
	RemoteKindDesktop  = "desktop"
	RemoteKindFiles    = "files"
	RemoteKindTerminal = "terminal"
)

// Remote query kinds (RemoteQueryMsg.Kind).
const (
	RemoteQueryRustDeskID = "rustdesk_id" // read the locally-installed RustDesk peer id
)

// RemoteQueryMsg asks the agent to read a remote-provider value on demand (e.g.
// the RustDesk peer id when a technician clicks "Open with RustDesk"). The agent
// replies with a RemoteQueryResultMsg carrying the same RequestID.
type RemoteQueryMsg struct {
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
}

// RemoteQueryResultMsg is the agent's reply to a RemoteQueryMsg. Exactly one of
// Value / Error is meaningful.
type RemoteQueryResultMsg struct {
	RequestID string `json:"request_id"`
	Value     string `json:"value,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RunCheckMsg asks the agent to execute a monitoring check. The agent returns a
// raw value/output; the server applies thresholds and computes the status.
type RunCheckMsg struct {
	CheckID string          `json:"check_id"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
}

// CheckResultMsg is the raw outcome of a check on the endpoint.
type CheckResultMsg struct {
	CheckID string   `json:"check_id"`
	Value   *float64 `json:"value,omitempty"` // numeric checks: free %, load %, or exit code
	Output  string   `json:"output"`
	Success bool     `json:"success"` // boolean checks: reachable / running / exit==0
}
