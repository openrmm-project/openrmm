package models

import (
	"encoding/json"
	"time"
)

// Roles.
const (
	RoleAdmin    = "admin"
	RoleTech     = "tech"
	RoleReadOnly = "readonly"
)

// Check types.
const (
	CheckDiskSpace  = "diskspace"
	CheckMemory     = "memory"
	CheckCPULoad    = "cpuload"
	CheckPing       = "ping"
	CheckWinService = "winservice"
	CheckScript     = "script"
)

// Check / result statuses.
const (
	CheckPassing = "passing"
	CheckWarning = "warning"
	CheckFailing = "failing"
)

// Task schedule kinds.
const (
	ScheduleInterval = "interval"
	ScheduleDaily    = "daily"
)

// Policy scope types.
const (
	ScopeClient = "client"
	ScopeSite   = "site"
	ScopeAgent  = "agent"
)

// Alert statuses & severities.
const (
	AlertNew      = "new"
	AlertAcked    = "acked"
	AlertResolved = "resolved"

	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// User is a console operator.
type User struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	TOTPEnabled bool      `json:"totp_enabled"`
}

// UserScope restricts a user's access to a client/site/agent subtree. A user
// with no scopes has full access. Applies to tech/readonly; admins are full.
type UserScope struct {
	ScopeType string `json:"scope_type"` // client | site | agent
	ScopeID   string `json:"scope_id"`
}

// APITokenInfo is a stored API token (without the secret).
type APITokenInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// DeviceGroup is a logical grouping of agents (cross-site).
type DeviceGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Client is the top of the org hierarchy (Client -> Site -> Agent).
type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Site belongs to a client and groups agents.
type Site struct {
	ID        string    `json:"id"`
	ClientID  string    `json:"client_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Script is a reusable script in the library.
type Script struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Shell       string    `json:"shell"`
	Body        string    `json:"body"`
	DefaultArgs string    `json:"default_args"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Policy bundles checks and tasks that are inherited by agents in scope.
type Policy struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// PolicyAssignment binds a policy to a client, site, or agent.
type PolicyAssignment struct {
	ID        string    `json:"id"`
	PolicyID  string    `json:"policy_id"`
	ScopeType string    `json:"scope_type"`
	ScopeID   string    `json:"scope_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Check is a monitoring definition owned by an agent or a policy.
type Check struct {
	ID            string          `json:"id"`
	AgentID       *string         `json:"agent_id,omitempty"`
	PolicyID      *string         `json:"policy_id,omitempty"`
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	Config        json.RawMessage `json:"config"`
	WarnThreshold *float64        `json:"warn_threshold,omitempty"`
	ErrThreshold  *float64        `json:"err_threshold,omitempty"`
	IntervalSec   int             `json:"interval_sec"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
}

// CheckResult is one execution of a check on an agent.
type CheckResult struct {
	ID      string    `json:"id"`
	CheckID string    `json:"check_id"`
	AgentID string    `json:"agent_id"`
	Status  string    `json:"status"`
	Value   *float64  `json:"value,omitempty"`
	Output  string    `json:"output"`
	RanAt   time.Time `json:"ran_at"`
}

// Task is scheduled automation owned by an agent or a policy.
type Task struct {
	ID           string    `json:"id"`
	AgentID      *string   `json:"agent_id,omitempty"`
	PolicyID     *string   `json:"policy_id,omitempty"`
	Name         string    `json:"name"`
	ScriptID     *string   `json:"script_id,omitempty"`
	Shell        string    `json:"shell"`
	Script       string    `json:"script"`
	ScheduleKind string    `json:"schedule_kind"`
	IntervalSec  int       `json:"interval_sec"`
	RunAtMinute  int       `json:"run_at_minute"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// Alert is a notification raised by a failing check/task or an offline agent.
type Alert struct {
	ID         string     `json:"id"`
	AgentID    *string    `json:"agent_id,omitempty"`
	SourceType string     `json:"source_type"`
	SourceID   *string    `json:"source_id,omitempty"`
	Severity   string     `json:"severity"`
	Message    string     `json:"message"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}
