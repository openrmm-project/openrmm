package models

import (
	"encoding/json"
	"testing"
)

func TestValidatePlatformServiceChecks(t *testing.T) {
	for _, kind := range []string{CheckLinuxService, CheckMacService} {
		threshold := 50.0
		c := Check{Type: kind, Name: "Service", Config: json.RawMessage(`{"service":"worker"}`), IntervalSec: 300, TimeoutSec: 30, FailureCount: 2, RecoveryCount: 2, WarnThreshold: &threshold, ErrThreshold: &threshold}
		if !SupportedCheckType(kind) {
			t.Fatalf("unsupported %s", kind)
		}
		if err := ValidateCheck(&c); err != nil {
			t.Fatal(err)
		}
		if c.WarnThreshold != nil || c.ErrThreshold != nil {
			t.Fatal("state check retained numeric thresholds")
		}
		var cfg map[string]string
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		if kind == CheckLinuxService && cfg["service"] != "worker.service" {
			t.Fatal("missing systemd normalization")
		}
		if kind == CheckMacService && (cfg["service"] != "system/worker" || cfg["state"] != "running") {
			t.Fatal("missing launchd defaults")
		}
		c.Config = json.RawMessage(`{"service":"$(id)"}`)
		if err := ValidateCheck(&c); err == nil {
			t.Fatal("shared API/model validation accepted a command")
		}
	}
}

func TestServiceCheckPlatformAndVersion(t *testing.T) {
	for _, tt := range []struct {
		kind, os string
		want     bool
	}{
		{CheckLinuxService, "Linux", true}, {CheckLinuxService, "windows", false}, {CheckLinuxService, "darwin", false},
		{CheckMacService, "darwin", true}, {CheckMacService, "macOS", true}, {CheckMacService, "linux", false},
		{CheckMacService, "", false}, {CheckWinService, "windows", true}, {CheckScript, "linux", true},
	} {
		if got := ServiceCheckMatchesOS(tt.kind, tt.os); got != tt.want {
			t.Errorf("%s/%s=%v", tt.kind, tt.os, got)
		}
	}
	for _, tt := range []struct {
		version string
		want    bool
	}{{"0.9.5", false}, {"0.10.0", true}, {"v0.10.1", true}, {"unknown", false}, {"0.10.0-rc.1", false}, {"1.0.0", true}} {
		if got := AgentVersionAtLeast(tt.version, MinAgentVersionServiceChecks); got != tt.want {
			t.Errorf("version %s=%v", tt.version, got)
		}
	}
	if IsPlatformServiceCheck(CheckWinService) || !IsPlatformServiceCheck(CheckMacService) || !IsPlatformServiceCheck(CheckLinuxService) {
		t.Fatal("platform check classification changed")
	}
}
