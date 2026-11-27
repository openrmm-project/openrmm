package protocol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestDockerSnapshotBounds(t *testing.T) {
	valid := DockerContainer{ID: strings.Repeat("a", 64), Name: "web", Image: "example/web:1", State: "running", Health: "none"}
	cases := []struct {
		name   string
		change func(*DockerSnapshot)
	}{
		{"status", func(s *DockerSnapshot) { s.Status = "healthy" }},
		{"raw error", func(s *DockerSnapshot) { s.ErrorCode = "password=secret" }},
		{"failed inventory", func(s *DockerSnapshot) { s.Status = DockerUnavailable }},
		{"duplicate", func(s *DockerSnapshot) { s.Containers = append(s.Containers, valid) }},
		{"oversized", func(s *DockerSnapshot) { s.Containers = make([]DockerContainer, MaxDockerContainers+1) }},
		{"identity", func(s *DockerSnapshot) { s.Containers[0].ID = "--host=remote" }},
		{"name", func(s *DockerSnapshot) { s.Containers[0].Name = "web;id" }},
		{"label", func(s *DockerSnapshot) { s.Containers[0].ComposeService = "secret\x00" }},
		{"cpu", func(s *DockerSnapshot) { v := math.Inf(1); s.Containers[0].CPUPercent = &v }},
		{"memory", func(s *DockerSnapshot) { v := int64(-1); s.Containers[0].MemoryUsedBytes = &v }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DockerSnapshot{Status: DockerAvailable, Containers: []DockerContainer{valid}}
			tc.change(&s)
			if s.Validate() == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	if err := (DockerSnapshot{Status: DockerAvailable, Containers: []DockerContainer{valid}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (DockerSnapshot{Status: DockerAvailable, Containers: []DockerContainer{}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDockerCheckConfiguration(t *testing.T) {
	c, err := NormalizeDockerCheck(json.RawMessage(`{"container":" web-1.2 "}`))
	if err != nil || c.Container != "web-1.2" || c.Mode != "running" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, raw := range []string{`{}`, `{"container":"x*"}`, `{"container":"web","mode":"start"}`, `{"container":"web","command":"rm"}`, `{"container":"web"} {}`, `[]`} {
		if _, err := NormalizeDockerCheck(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
