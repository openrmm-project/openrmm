package scheduler

import "testing"

func TestAgentVersionRuleLevels(t *testing.T) {
	for _, tc := range []struct {
		agent, server, level string
		want                 bool
	}{
		{"0.9.5", "0.10.0", "minor", true},
		{"0.9.5", "0.9.6", "minor", false},
		{"0.9.5", "0.9.6", "patch", true},
		{"1.9.9", "2.0.0", "major", true},
		{"2.0.0", "1.9.9", "patch", false},
		{"v0.9.5+build", "0.9.5", "patch", false},
	} {
		a, ok := parseVersion(tc.agent)
		if !ok {
			t.Fatal(tc.agent)
		}
		b, ok := parseVersion(tc.server)
		if !ok {
			t.Fatal(tc.server)
		}
		if got := behind(a, b, tc.level); got != tc.want {
			t.Fatalf("%+v: got %v", tc, got)
		}
	}
	for _, value := range []string{"dev", "main", "1.2", "1.a.2", "-1.2.3", "1.2.3.4"} {
		if _, ok := parseVersion(value); ok {
			t.Fatalf("invalid version accepted: %q", value)
		}
	}
}
