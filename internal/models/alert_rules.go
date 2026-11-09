package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Server rules are check types the server evaluates from what it already knows,
// never sent to the agent (docs/24-alert-rules.md). One is in force per device
// and type; with none, the fleet default for the type applies.
const (
	CheckConnectivity  = "connectivity"
	CheckJobFailure    = "job_failure"
	CheckPatchFailure  = "patch_failure"
	CheckRebootPending = "reboot_pending"
	CheckAgentOutdated = "agent_outdated"
)

// ServerRuleTypes lists the server rule types, in the order the console shows
// them.
var ServerRuleTypes = []string{CheckConnectivity, CheckJobFailure, CheckPatchFailure, CheckRebootPending, CheckAgentOutdated}

// IsServerRule reports whether a check type is evaluated by the server.
func IsServerRule(checkType string) bool {
	for _, t := range ServerRuleTypes {
		if t == checkType {
			return true
		}
	}
	return false
}

// AlertSourceRule is the source of an alert a server rule other than
// connectivity raised; its source_id is the device and its condition_key the
// rule type (or, for job_failure, the task or script).
const AlertSourceRule = "rule"

// Server rule modes: alert, or say not to.
const (
	RuleModeAlert  = "alert"
	RuleModeIgnore = "ignore"
)

// ServerRuleConfig is a server rule's config. Fields a type does not use stay
// zero and are not stored.
type ServerRuleConfig struct {
	Mode      string `json:"mode"`
	Severity  string `json:"severity,omitempty"`
	AfterMin  int    `json:"after_min,omitempty"`  // connectivity: minutes without a heartbeat
	Scope     string `json:"scope,omitempty"`      // job_failure: tasks | all
	AfterDays int    `json:"after_days,omitempty"` // reboot_pending: days pending
	Behind    string `json:"behind,omitempty"`     // agent_outdated: major | minor | patch
}

// Alerts reports whether the rule raises alerts rather than saying not to.
func (c ServerRuleConfig) Alerts() bool { return c.Mode == RuleModeAlert }

// Limits of the server rules' settings.
const (
	ConnectivityMinMinutes = 3
	ConnectivityMaxMinutes = 7 * 24 * 60
	RebootPendingMaxDays   = 90
)

// ParseServerRuleConfig reads and checks a server rule's config, filling the
// type's defaults for what it leaves out.
func ParseServerRuleConfig(checkType string, raw json.RawMessage) (ServerRuleConfig, error) {
	var c ServerRuleConfig
	if len(raw) > 16384 {
		return c, fmt.Errorf("config is too large")
	}
	if len(raw) > 0 {
		object := bytes.TrimSpace(raw)
		if len(object) == 0 || object[0] != '{' {
			return c, fmt.Errorf("config must be a JSON object")
		}
		decoder := json.NewDecoder(bytes.NewReader(object))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			return c, fmt.Errorf("config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return c, fmt.Errorf("config must contain exactly one JSON object")
		}
	}
	if c.Mode == "" {
		c.Mode = RuleModeAlert
	}
	if c.Mode != RuleModeAlert && c.Mode != RuleModeIgnore {
		return c, fmt.Errorf("config.mode must be %q or %q", RuleModeAlert, RuleModeIgnore)
	}
	if c.Severity == "" {
		c.Severity = SeverityWarning
		if checkType == CheckConnectivity {
			c.Severity = SeverityError
		}
	}
	switch c.Severity {
	case SeverityInfo, SeverityWarning, SeverityError:
	default:
		return c, fmt.Errorf("config.severity must be info, warning or error")
	}
	switch checkType {
	case CheckConnectivity:
		c.Scope, c.AfterDays, c.Behind = "", 0, ""
		if c.AfterMin == 0 {
			c.AfterMin = ConnectivityMinMinutes
		}
		if c.AfterMin < ConnectivityMinMinutes || c.AfterMin > ConnectivityMaxMinutes {
			return c, fmt.Errorf("config.after_min must be between %d and %d", ConnectivityMinMinutes, ConnectivityMaxMinutes)
		}
	case CheckJobFailure:
		c.AfterMin, c.AfterDays, c.Behind = 0, 0, ""
		if c.Scope == "" {
			c.Scope = "tasks"
		}
		if c.Scope != "tasks" && c.Scope != "all" {
			return c, fmt.Errorf("config.scope must be tasks or all")
		}
	case CheckPatchFailure:
		c.AfterMin, c.Scope, c.AfterDays, c.Behind = 0, "", 0, ""
	case CheckRebootPending:
		c.AfterMin, c.Scope, c.Behind = 0, "", ""
		if c.AfterDays < 0 || c.AfterDays > RebootPendingMaxDays {
			return c, fmt.Errorf("config.after_days must be between 0 and %d", RebootPendingMaxDays)
		}
	case CheckAgentOutdated:
		c.AfterMin, c.Scope, c.AfterDays = 0, "", 0
		if c.Behind == "" {
			c.Behind = "minor"
		}
		if c.Behind != "major" && c.Behind != "minor" && c.Behind != "patch" {
			return c, fmt.Errorf("config.behind must be major, minor or patch")
		}
	default:
		return c, fmt.Errorf("%q is not a server rule type", checkType)
	}
	return c, nil
}

// Raw encodes the config as stored.
func (c ServerRuleConfig) Raw() json.RawMessage {
	b, _ := json.Marshal(c)
	return b
}

// GoverningRule is the server rule in force for one device and type: a check,
// or the fleet default (CheckID empty).
type GoverningRule struct {
	MonitoringScopeSince time.Time        `json:"-"`
	Type                 string           `json:"type"`
	Config               ServerRuleConfig `json:"config"`
	CheckID              string           `json:"check_id,omitempty"`
	Name                 string           `json:"name,omitempty"`
	PolicyID             string           `json:"policy_id,omitempty"`
	PolicyName           string           `json:"policy_name,omitempty"`
	ScopeType            string           `json:"scope_type,omitempty"` // client | site | group | agent | device (owned by the device)
}

// AlertRuleDefault is the fleet default of one server rule type.
type AlertRuleDefault struct {
	Type      string           `json:"type"`
	Config    ServerRuleConfig `json:"config"`
	UpdatedBy string           `json:"updated_by"`
}
