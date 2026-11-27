package models

import (
	"encoding/json"
	"testing"
)

func TestDockerCheckValidation(t *testing.T) {
	c := Check{Type: CheckDockerContainer, Name: "Web", Config: json.RawMessage(`{"container":"web"}`), IntervalSec: 30, TimeoutSec: 10, FailureCount: 2, RecoveryCount: 2}
	if err := ValidateCheck(&c); err != nil {
		t.Fatal(err)
	}
	if string(c.Config) != `{"container":"web","mode":"running"}` {
		t.Fatalf("config=%s", c.Config)
	}
	c.Config = json.RawMessage(`{"container":"web;stop"}`)
	if err := ValidateCheck(&c); err == nil {
		t.Fatal("unsafe name accepted")
	}
	if !ServiceCheckMatchesOS(CheckDockerContainer, "linux") || ServiceCheckMatchesOS(CheckDockerContainer, "windows") || ServiceCheckMatchesOS(CheckDockerContainer, "darwin") {
		t.Fatal("Docker platform matching")
	}
}
