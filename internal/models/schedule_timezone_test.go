package models

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeTimezone(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "",
		"UTC":                            "UTC",
		" Europe/Lisbon ":                "Europe/Lisbon",
		"America/New_York":               "America/New_York",
		"America/Argentina/Buenos_Aires": "America/Argentina/Buenos_Aires",
		"Etc/GMT+5":                      "Etc/GMT+5",
		"Asia/Kuala_Lumpur":              "Asia/Kuala_Lumpur",
	} {
		if got, err := NormalizeTimezone(in); err != nil || got != want {
			t.Errorf("NormalizeTimezone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"Local", "local", "../../etc/passwd", "/etc/localtime", "America/Nowhere", "New York", "America//New_York", strings.Repeat("A", 65)} {
		if _, err := NormalizeTimezone(bad); err == nil {
			t.Errorf("NormalizeTimezone(%q) was accepted", bad)
		}
	}
}

func TestScheduleLocationFallsBackToUTC(t *testing.T) {
	for _, tz := range []string{"", "UTC", "Mars/Olympus", "Local", "../x"} {
		if loc := ScheduleLocation(tz); loc != time.UTC {
			t.Errorf("ScheduleLocation(%q) = %v, want UTC", tz, loc)
		}
	}
	if loc := ScheduleLocation("America/New_York"); loc.String() != "America/New_York" {
		t.Errorf("ScheduleLocation(America/New_York) = %v", loc)
	}
}

func TestValidateScheduleChecksTheZone(t *testing.T) {
	task := &Task{ScheduleKind: ScheduleDaily, RunAtMinute: 60, Timezone: " America/Chicago "}
	if err := ValidateSchedule(task); err != nil || task.Timezone != "America/Chicago" {
		t.Fatalf("valid zone: %v, %q", err, task.Timezone)
	}
	if err := ValidateSchedule(&Task{ScheduleKind: ScheduleDaily, RunAtMinute: 60, Timezone: "Local"}); err == nil {
		t.Fatal("the server's own zone was accepted")
	}
}
