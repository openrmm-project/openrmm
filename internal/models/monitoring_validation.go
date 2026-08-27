// Validation for monitoring definitions.
//
// These live beside the models rather than next to the REST handlers because
// there are now two ways into these rows: the console through the API, and the
// AI tools through the store. Validation living in only one of them would let
// the other create a definition the agent cannot run, or one the server would
// silently treat as healthy.
package models

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

func SupportedCheckType(checkType string) bool {
	switch checkType {
	case CheckDiskSpace, CheckMemory, CheckCPULoad,
		CheckPing, CheckWinService, CheckScript:
		return true
	default:
		return false
	}
}

func finitePercent(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 100
}

// validateCheck is shared by create/update so API clients cannot create a
// definition the agent cannot run or the server would silently treat healthy.
func ValidateCheck(c *Check) error {
	c.Type = strings.TrimSpace(c.Type)
	c.Name = strings.TrimSpace(c.Name)
	if !SupportedCheckType(c.Type) {
		return fmt.Errorf("unsupported check type %q", c.Type)
	}
	if c.Name == "" || len(c.Name) > 200 {
		return fmt.Errorf("name is required and must be at most 200 characters")
	}
	if c.IntervalSec < 30 || c.IntervalSec > 86400 {
		return fmt.Errorf("interval_sec must be between 30 and 86400")
	}
	if c.TimeoutSec < 5 || c.TimeoutSec > 86400 {
		return fmt.Errorf("timeout_sec must be between 5 and 86400")
	}
	if c.FailureCount < 1 || c.FailureCount > 100 || c.RecoveryCount < 1 || c.RecoveryCount > 100 {
		return fmt.Errorf("failure_count and recovery_count must be between 1 and 100")
	}
	if math.IsNaN(c.Hysteresis) || math.IsInf(c.Hysteresis, 0) || c.Hysteresis < 0 || c.Hysteresis > 100 {
		return fmt.Errorf("hysteresis must be between 0 and 100")
	}
	if c.CooldownSec < 0 || c.CooldownSec > 604800 {
		return fmt.Errorf("cooldown_sec must be between 0 and 604800")
	}
	if len(c.Config) == 0 {
		c.Config = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(c.Config, &object); err != nil || object == nil {
		return fmt.Errorf("config must be a JSON object")
	}

	switch c.Type {
	case CheckDiskSpace, CheckMemory:
		if !finitePercent(c.WarnThreshold) || !finitePercent(c.ErrThreshold) || *c.ErrThreshold >= *c.WarnThreshold {
			return fmt.Errorf("low-water checks require 0-100 thresholds with err_threshold below warn_threshold")
		}
	case CheckCPULoad:
		if !finitePercent(c.WarnThreshold) || !finitePercent(c.ErrThreshold) || *c.ErrThreshold <= *c.WarnThreshold {
			return fmt.Errorf("CPU checks require 0-100 thresholds with err_threshold above warn_threshold")
		}
	case CheckPing:
		var cfg struct {
			Host string `json:"host"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err != nil || strings.TrimSpace(cfg.Host) == "" {
			return fmt.Errorf("ping config.host is required")
		}
		c.WarnThreshold, c.ErrThreshold = nil, nil
	case CheckWinService:
		var cfg struct {
			Service string `json:"service"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err != nil || strings.TrimSpace(cfg.Service) == "" {
			return fmt.Errorf("winservice config.service is required")
		}
		c.WarnThreshold, c.ErrThreshold = nil, nil
	case CheckScript:
		var cfg struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err != nil || !SupportedShell(cfg.Shell) {
			return fmt.Errorf("script config.shell is required and must be supported")
		}
		if strings.TrimSpace(cfg.Script) == "" {
			return fmt.Errorf("script config.script is required")
		}
		c.WarnThreshold, c.ErrThreshold = nil, nil
	}
	return nil
}

func ValidateSchedule(t *Task) error {
	switch t.ScheduleKind {
	case ScheduleInterval:
		if t.IntervalSec <= 0 {
			return fmt.Errorf("interval_sec must be positive")
		}
	case ScheduleDaily:
		if t.RunAtMinute < 0 || t.RunAtMinute > 1439 {
			return fmt.Errorf("run_at_minute out of range")
		}
	case ScheduleWeekly:
		t.DaysOfWeek &= 0x7F // keep the 7 weekday bits (Sun..Sat)
		if t.DaysOfWeek == 0 {
			return fmt.Errorf("weekly schedule needs at least one day of week")
		}
		if len(t.RunAtMinutes) == 0 {
			return fmt.Errorf("weekly schedule needs at least one time of day")
		}
		for _, m := range t.RunAtMinutes {
			if m < 0 || m > 1439 {
				return fmt.Errorf("run_at_minutes out of range")
			}
		}
	default:
		return fmt.Errorf("unknown schedule_kind %q", t.ScheduleKind)
	}
	return nil
}
