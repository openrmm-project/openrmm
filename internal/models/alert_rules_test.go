package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServerRuleConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, raw string
		valid           bool
	}{
		{"connectivity default", CheckConnectivity, "{}", true},
		{"laptops ignore", CheckConnectivity, "{\"mode\":\"ignore\"}", true},
		{"one day delay", CheckConnectivity, "{\"after_min\":1440,\"severity\":\"info\"}", true},
		{"too short", CheckConnectivity, "{\"after_min\":2}", false},
		{"too long", CheckConnectivity, "{\"after_min\":10081}", false},
		{"unknown mode", CheckConnectivity, "{\"mode\":\"silence\"}", false},
		{"unknown severity", CheckConnectivity, "{\"severity\":\"critical\"}", false},
		{"null object", CheckConnectivity, "null", false},
		{"array", CheckConnectivity, "[]", false},
		{"typo", CheckConnectivity, "{\"after_mins\":30}", false},
		{"extra document", CheckConnectivity, "{}{}", false},
		{"jobs all", CheckJobFailure, "{\"scope\":\"all\"}", true},
		{"jobs tasks", CheckJobFailure, "{\"scope\":\"tasks\"}", true},
		{"bad job scope", CheckJobFailure, "{\"scope\":\"everything\"}", false},
		{"patch default", CheckPatchFailure, "{}", true},
		{"immediate reboot", CheckRebootPending, "{\"after_days\":0}", true},
		{"reboot boundary", CheckRebootPending, "{\"after_days\":90}", true},
		{"reboot negative", CheckRebootPending, "{\"after_days\":-1}", false},
		{"reboot too long", CheckRebootPending, "{\"after_days\":91}", false},
		{"version patch", CheckAgentOutdated, "{\"behind\":\"patch\"}", true},
		{"version bad level", CheckAgentOutdated, "{\"behind\":\"build\"}", false},
		{"agent check is not server rule", CheckScript, "{}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseServerRuleConfig(tc.kind, json.RawMessage(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if err == nil && cfg.Mode == "" {
				t.Fatal("missing normalized mode")
			}
		})
	}
	if _, err := ParseServerRuleConfig(CheckConnectivity, json.RawMessage(strings.Repeat(" ", 16385))); err == nil {
		t.Fatal("oversized config accepted")
	}
}

func TestServerRulesNormalizeWithoutAgentExecutionSettings(t *testing.T) {
	for _, kind := range ServerRuleTypes {
		c := Check{Type: kind, Name: "Rule", Config: json.RawMessage("{\"mode\":\"ignore\"}"), IntervalSec: -1}
		if err := ValidateCheck(&c); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if c.IntervalSec != 300 || c.TimeoutSec != 180 || c.FailureCount != 1 || c.RecoveryCount != 1 {
			t.Fatalf("%s did not normalize agent-only fields: %+v", kind, c)
		}
		if !IsServerRule(c.Type) || !SupportedCheckType(c.Type) {
			t.Fatal("server type missing")
		}
	}
	if err := ValidateCheck(&Check{Type: CheckScript, Name: "script", Config: json.RawMessage("{}")}); err == nil {
		t.Fatal("agent checks bypassed their normal validation")
	}
}
