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
	CheckUnknown = "unknown"
	CheckPassing = "passing"
	CheckWarning = "warning"
	CheckFailing = "failing"
)

// Durable check execution statuses.
const (
	CheckExecutionPending   = "pending"
	CheckExecutionCompleted = "completed"
	CheckExecutionTimedOut  = "timed_out"
	CheckExecutionCanceled  = "canceled"
)

// Task schedule kinds.
const (
	ScheduleInterval = "interval"
	ScheduleDaily    = "daily"
	ScheduleWeekly   = "weekly"
)

// Policy scope types.
const (
	ScopeClient = "client"
	ScopeSite   = "site"
	ScopeGroup  = "group"
	ScopeAgent  = "agent"
)

// Alert statuses & severities.
const (
	AlertNew        = "new"
	AlertAcked      = "acked"
	AlertSuppressed = "suppressed"
	AlertResolved   = "resolved"

	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

const (
	AlertSourceCheck  = "check"
	AlertSourceTask   = "task"
	AlertSourceAgent  = "agent"
	AlertSourceSystem = "system"
)

const (
	AlertEventOpened       = "opened"
	AlertEventOccurrence   = "occurrence"
	AlertEventEscalated    = "escalated"
	AlertEventAcknowledged = "acknowledged"
	AlertEventResolved     = "resolved"
	AlertEventReopened     = "reopened"
	AlertEventSuppressed   = "suppressed"
	AlertEventUnsuppressed = "unsuppressed"
)

const (
	SuppressionSilence     = "silence"
	SuppressionMaintenance = "maintenance"
)

// User is a console operator.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Email is optional: console-only accounts stay valid without one. It doubles
	// as the notification address, so a user without it simply receives no mail.
	Email       string    `json:"email"`
	Permissions []string  `json:"permissions,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	TOTPEnabled bool      `json:"totp_enabled"`
	// DisabledAt withdraws access without deleting the account, so what the user
	// did stays on the record. Nil means active; the timestamp answers "when was
	// this revoked", which is the question an incident review asks.
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
}

// Disabled reports whether this account may still authenticate.
func (u User) Disabled() bool { return u.DisabledAt != nil }

// UserScope restricts a user's access to a client/site/group/agent subtree. A user
// with no scopes has full access. Applies to tech/readonly; admins are full.
type UserScope struct {
	ScopeType string `json:"scope_type"` // client | site | group | agent
	ScopeID   string `json:"scope_id"`
}

// APITokenInfo is a stored API token (without the secret).
type APITokenInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil = never expires
}

// DeviceGroup is a department under a site (Company > Site > Department).
// SiteID binds it to the hierarchy so a group never spans companies/sites.
type DeviceGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SiteID    *string   `json:"site_id,omitempty"`
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

// ScriptCategory is a category as it actually exists: a name some scripts share.
// There is no category table — the name lives on each script — so a category is
// only ever reported alongside how many scripts keep it alive.
type ScriptCategory struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Policy bundles checks and tasks that are inherited by agents in scope.
type Policy struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// PolicyAssignment binds a policy to a client, site, group, or agent.
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
	FailureCount  int             `json:"failure_count"`
	RecoveryCount int             `json:"recovery_count"`
	Hysteresis    float64         `json:"hysteresis"`
	CooldownSec   int             `json:"cooldown_sec"`
	TimeoutSec    int             `json:"timeout_sec"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
	Instances     []CheckInstance `json:"instances,omitempty"`
	Summary       *CheckSummary   `json:"summary,omitempty"`
}

// CheckResult is one execution of a check on an agent.
type CheckResult struct {
	ID          string    `json:"id"`
	CheckID     string    `json:"check_id"`
	AgentID     string    `json:"agent_id"`
	ExecutionID *string   `json:"execution_id,omitempty"`
	Status      string    `json:"status"`
	Value       *float64  `json:"value,omitempty"`
	Output      string    `json:"output"`
	RanAt       time.Time `json:"ran_at"`
}

// CheckExecution is one durably tracked dispatch. The execution id correlates
// server dispatch, the agent's persisted result, and the acknowledgement that
// lets the agent remove that result from its outbox.
type CheckExecution struct {
	ID           string     `json:"id"`
	CheckID      string     `json:"check_id"`
	AgentID      string     `json:"agent_id"`
	Status       string     `json:"status"`
	DispatchedAt time.Time  `json:"dispatched_at"`
	DeadlineAt   time.Time  `json:"deadline_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// CheckState is the current per-device state of a direct or policy check.
type CheckState struct {
	CheckID              string     `json:"check_id"`
	AgentID              string     `json:"agent_id"`
	Status               string     `json:"status"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	ConsecutiveSuccesses int        `json:"consecutive_successes"`
	LastValue            *float64   `json:"last_value,omitempty"`
	LastOutput           string     `json:"last_output"`
	LastResultAt         *time.Time `json:"last_result_at,omitempty"`
	LastDispatchAt       *time.Time `json:"last_dispatch_at,omitempty"`
	ReconciledAt         *time.Time `json:"reconciled_at,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// CheckInstance makes a policy check honest in the UI: one definition can have
// many independently healthy or failing target devices.
type CheckInstance struct {
	AgentID      string     `json:"agent_id"`
	Hostname     string     `json:"hostname"`
	Status       string     `json:"status"`
	Value        *float64   `json:"value,omitempty"`
	Output       string     `json:"output"`
	LastResultAt *time.Time `json:"last_result_at,omitempty"`
}

type CheckSummary struct {
	Total   int `json:"total"`
	Unknown int `json:"unknown"`
	Passing int `json:"passing"`
	Warning int `json:"warning"`
	Failing int `json:"failing"`
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
	DaysOfWeek   int       `json:"days_of_week"`   // weekly: bitmask, bit N = time.Weekday N (0=Sun)
	RunAtMinutes []int     `json:"run_at_minutes"` // weekly: times of day, minutes since midnight (UTC)
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// Notification is one queued outbound message. It is a row rather than an
// in-process job so a server restart — which tends to happen exactly when
// things are going wrong — does not drop the alert nobody has read yet.
type Notification struct {
	ID         string  `json:"id"`
	EventType  string  `json:"event_type"`
	AlertID    *string `json:"alert_id,omitempty"`
	AgentID    *string `json:"agent_id,omitempty"`
	UserID     *string `json:"user_id,omitempty"`
	Recipient  string  `json:"recipient"`
	Subject    string  `json:"subject"`
	Body       string  `json:"body"`
	Attempts   int     `json:"attempts"`
	DedupeKey  string  `json:"dedupe_key,omitempty"`
	LeaseToken *string `json:"lease_token,omitempty"`
}

// Alert is a notification raised by a failing check/task or an offline agent.
type Alert struct {
	ID                  string     `json:"id"`
	AgentID             *string    `json:"agent_id,omitempty"`
	SourceType          string     `json:"source_type"`
	SourceID            *string    `json:"source_id,omitempty"`
	ConditionKey        string     `json:"condition_key"`
	AgentSnapshotID     *string    `json:"agent_snapshot_id,omitempty"`
	AgentHostname       string     `json:"agent_hostname"`
	SiteID              *string    `json:"site_id,omitempty"`
	ClientID            *string    `json:"client_id,omitempty"`
	GroupID             *string    `json:"group_id,omitempty"`
	Severity            string     `json:"severity"`
	Message             string     `json:"message"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"created_at"`
	FirstSeenAt         time.Time  `json:"first_seen_at"`
	LastSeenAt          time.Time  `json:"last_seen_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
	OccurrenceCount     int64      `json:"occurrence_count"`
	ObservedValue       *float64   `json:"observed_value,omitempty"`
	ThresholdValue      *float64   `json:"threshold_value,omitempty"`
	Output              string     `json:"output"`
	Version             int64      `json:"version"`
	AckedAt             *time.Time `json:"acked_at,omitempty"`
	AckedBy             string     `json:"acked_by,omitempty"`
	ResolvedBy          string     `json:"resolved_by,omitempty"`
	ResolutionKind      string     `json:"resolution_kind,omitempty"`
	ResolutionNote      string     `json:"resolution_note,omitempty"`
	ReopenedFrom        *string    `json:"reopened_from,omitempty"`
	LastNotifiedAt      *time.Time `json:"last_notified_at,omitempty"`
	NotificationPending bool       `json:"notification_pending,omitempty"`
	NotificationKind    string     `json:"notification_kind,omitempty"`
	NotificationSeq     int64      `json:"notification_seq,omitempty"`
	SilencedUntil       *time.Time `json:"silenced_until,omitempty"`
	SilencedBy          string     `json:"silenced_by,omitempty"`
	SilenceReason       string     `json:"silence_reason,omitempty"`
}

type AlertEvent struct {
	ID            string          `json:"id"`
	AlertID       string          `json:"alert_id"`
	EventType     string          `json:"event_type"`
	Actor         string          `json:"actor"`
	Severity      *string         `json:"severity,omitempty"`
	Message       string          `json:"message"`
	ObservedValue *float64        `json:"observed_value,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     time.Time       `json:"created_at"`
}

type AlertSuppression struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	ScopeType  string     `json:"scope_type"`
	ScopeID    string     `json:"scope_id"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     time.Time  `json:"ends_at"`
	Reason     string     `json:"reason"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	CanceledAt *time.Time `json:"canceled_at,omitempty"`
	CanceledBy string     `json:"canceled_by,omitempty"`
}

type AlertCounts struct {
	New        int `json:"new"`
	Acked      int `json:"acked"`
	Suppressed int `json:"suppressed"`
	Resolved   int `json:"resolved"`
	Open       int `json:"open"`
	Total      int `json:"total"`
}

// ===================== Patch management =====================

// Patch classifications + agent_patches statuses + reboot policy values.
const (
	PatchClassSecurity  = "security"
	PatchClassCritical  = "critical"
	PatchClassImportant = "important"
	PatchClassModerate  = "moderate"
	PatchClassOptional  = "optional"
	PatchClassOther     = "other"

	PatchStatusMissing    = "missing"
	PatchStatusApproved   = "approved"
	PatchStatusInstalling = "installing"
	PatchStatusInstalled  = "installed"
	PatchStatusFailed     = "failed"
	PatchStatusExcluded   = "excluded"

	RebootNever      = "never"
	RebootIfRequired = "if_required"
	RebootAlways     = "always"
)

// Patch is one distinct patch in the fleet-wide catalog.
type Patch struct {
	ID             string    `json:"id"`
	OS             string    `json:"os"`
	Identifier     string    `json:"identifier"`
	Title          string    `json:"title"`
	Classification string    `json:"classification"`
	KB             string    `json:"kb"`
	CVEs           []string  `json:"cves"`
	RebootRequired bool      `json:"reboot_required"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
}

// AgentPatch is a patch's state on one device (joined with catalog fields for
// list endpoints).
type AgentPatch struct {
	AgentID        string     `json:"agent_id"`
	PatchID        string     `json:"patch_id"`
	Status         string     `json:"status"`
	AvailableVer   string     `json:"available_version"`
	DetectedAt     time.Time  `json:"detected_at"`
	InstalledAt    *time.Time `json:"installed_at,omitempty"`
	Error          string     `json:"error,omitempty"`
	Hostname       string     `json:"hostname,omitempty"`
	Identifier     string     `json:"identifier,omitempty"`
	Title          string     `json:"title,omitempty"`
	Classification string     `json:"classification,omitempty"`
	KB             string     `json:"kb,omitempty"`
	CVEs           []string   `json:"cves,omitempty"`
	RebootRequired bool       `json:"reboot_required,omitempty"`
}

// PatchPolicy bundles auto-approval, exclusions, a maintenance window and a
// reboot rule, assigned to a client/site/agent scope.
type PatchPolicy struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Enabled            bool      `json:"enabled"`
	AutoApprove        []string  `json:"auto_approve"`
	AutoApproveAgeDays int       `json:"auto_approve_age_days"`
	Exclusions         []string  `json:"exclusions"`
	WindowDays         []int     `json:"window_days"`
	WindowStartMin     int       `json:"window_start_min"`
	WindowEndMin       int       `json:"window_end_min"`
	Reboot             string    `json:"reboot"`
	CreatedAt          time.Time `json:"created_at"`
}

// PatchPolicyAssignment binds a patch policy to a scope.
type PatchPolicyAssignment struct {
	ID        string    `json:"id"`
	PolicyID  string    `json:"policy_id"`
	ScopeType string    `json:"scope_type"`
	ScopeID   string    `json:"scope_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ComplianceRow is a per-device patch-compliance summary.
type ComplianceRow struct {
	AgentID       string     `json:"agent_id"`
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"`
	Client        string     `json:"client"`
	Site          string     `json:"site"`
	Department    string     `json:"department"`
	MissingTotal  int        `json:"missing_total"`
	MissingSec    int        `json:"missing_security"`
	Approved      int        `json:"approved"`
	Failed        int        `json:"failed"`
	RebootPending bool       `json:"reboot_pending"`
	LastScan      *time.Time `json:"last_scan,omitempty"`
	Status        string     `json:"status"` // compliant|at_risk|critical|unknown
}
