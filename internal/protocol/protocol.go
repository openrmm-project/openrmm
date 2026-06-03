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
	MsgRunCheck     = "run_check"     // server -> agent: execute a monitoring check
	MsgCheckResult  = "check_result"  // agent -> server: raw check outcome
)

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

// RemoteStartMsg tells the agent to connect its remote-desktop module to the
// relay using the given session token (native provider).
type RemoteStartMsg struct {
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
	RelayURL  string `json:"relay_url"`
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
