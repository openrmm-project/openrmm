package models

import (
	"strings"
	"testing"
)

// ValidateSchedule decides when — and whether — a scheduled task ever fires.
// Only the interval branch was ever exercised, which left the two schedule kinds
// with real arithmetic in them (a weekday bitmask, a list of minutes-since-
// midnight) resting on nothing.
func TestValidateSchedule(t *testing.T) {
	tests := []struct {
		name    string
		task    Task
		wantErr string // "" = accepted
	}{
		{"interval accepted", Task{ScheduleKind: ScheduleInterval, IntervalSec: 3600}, ""},
		{"interval must be positive", Task{ScheduleKind: ScheduleInterval}, "interval_sec must be positive"},
		{"interval rejects negative", Task{ScheduleKind: ScheduleInterval, IntervalSec: -1}, "interval_sec must be positive"},

		// Minutes since midnight, UTC: 0 is midnight and 1439 is 23:59, so both
		// ends of the day must be accepted rather than treated as off-by-one.
		{"daily at midnight", Task{ScheduleKind: ScheduleDaily, RunAtMinute: 0}, ""},
		{"daily at the last minute of the day", Task{ScheduleKind: ScheduleDaily, RunAtMinute: 1439}, ""},
		{"daily rejects the minute past midnight-1440", Task{ScheduleKind: ScheduleDaily, RunAtMinute: 1440}, "run_at_minute out of range"},
		{"daily rejects negative", Task{ScheduleKind: ScheduleDaily, RunAtMinute: -1}, "run_at_minute out of range"},

		{"weekly accepted", Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0b0000010, RunAtMinutes: []int{540}}, ""},
		{"weekly every day", Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0x7F, RunAtMinutes: []int{0, 1439}}, ""},
		{"weekly needs a day", Task{ScheduleKind: ScheduleWeekly, RunAtMinutes: []int{540}}, "at least one day of week"},
		{"weekly needs a time", Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0b0000010}, "at least one time of day"},
		{"weekly rejects a time past the day", Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0b0000010, RunAtMinutes: []int{1440}}, "run_at_minutes out of range"},
		{"weekly rejects a negative time", Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0b0000010, RunAtMinutes: []int{-1}}, "run_at_minutes out of range"},
		{"weekly rejects a bad time among good ones", Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0x7F, RunAtMinutes: []int{60, 9999, 120}}, "run_at_minutes out of range"},

		{"unknown kind", Task{ScheduleKind: "hourly"}, `unknown schedule_kind "hourly"`},
		{"empty kind", Task{}, `unknown schedule_kind ""`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := tc.task
			err := ValidateSchedule(&task)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("rejected a valid schedule: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("accepted an invalid schedule, want %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The weekly branch does not merely read DaysOfWeek, it rewrites it: bits above
// the seven weekdays are masked off. That matters because the value is persisted
// and later matched against time.Weekday — a stray high bit would be stored and
// silently never match, and a value made up ONLY of high bits must be rejected
// rather than quietly becoming "no days".
func TestValidateScheduleMasksDaysOfWeek(t *testing.T) {
	task := Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0xFF, RunAtMinutes: []int{540}}
	if err := ValidateSchedule(&task); err != nil {
		t.Fatalf("a full week plus stray bits should be accepted: %v", err)
	}
	if task.DaysOfWeek != 0x7F {
		t.Errorf("days_of_week = %#b, want the stray bits masked off (%#b)", task.DaysOfWeek, 0x7F)
	}

	// Only bits outside the weekday range: masking leaves nothing, and "nothing"
	// is not a schedule.
	strayOnly := Task{ScheduleKind: ScheduleWeekly, DaysOfWeek: 0x80, RunAtMinutes: []int{540}}
	if err := ValidateSchedule(&strayOnly); err == nil {
		t.Fatal("a schedule whose only days were out-of-range bits was accepted")
	}
}

// The check counterpart, in the same place for the same reason: both are the
// shared validation the REST handlers and the AI tools go through.
func TestValidateCheckRejectsUnsupportedTypes(t *testing.T) {
	if SupportedCheckType("quantum") {
		t.Error("an unknown check type was reported supported")
	}
	for _, known := range []string{CheckDiskSpace, CheckMemory, CheckCPULoad, CheckPing, CheckWinService, CheckScript} {
		if !SupportedCheckType(known) {
			t.Errorf("%q should be a supported check type", known)
		}
	}
}
