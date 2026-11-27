package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	MsgDockerSnapshot      = "docker_snapshot"
	CheckDockerContainer   = "dockercontainer"
	DockerAvailable        = "available"
	DockerNotInstalled     = "not_installed"
	DockerUnavailable      = "unavailable"
	DockerUnsupported      = "unsupported"
	MaxDockerContainers    = 500
	MaxDockerSnapshotBytes = 2 << 20
	DockerFreshFor         = 90 * time.Second
)

// DockerSnapshot contains only an explicit inventory projection. Environment,
// commands, mount paths, logs and arbitrary labels never belong to this report.
type DockerSnapshot struct {
	Status     string            `json:"status"`
	ErrorCode  string            `json:"error_code,omitempty"`
	Containers []DockerContainer `json:"containers"`
}

type DockerContainer struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Image            string     `json:"image"`
	State            string     `json:"state"`
	Health           string     `json:"health"`
	CPUPercent       *float64   `json:"cpu_percent,omitempty"`
	MemoryUsedBytes  *int64     `json:"memory_used_bytes,omitempty"`
	MemoryLimitBytes *int64     `json:"memory_limit_bytes,omitempty"`
	RestartCount     int64      `json:"restart_count"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	ComposeProject   string     `json:"compose_project,omitempty"`
	ComposeService   string     `json:"compose_service,omitempty"`
}

var dockerID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var dockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

func ValidDockerID(id string) bool { return dockerID.MatchString(id) }

func dockerText(s string, max int) bool {
	return len(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

func (s DockerSnapshot) Validate() error {
	switch s.Status {
	case DockerAvailable, DockerNotInstalled, DockerUnavailable, DockerUnsupported:
	default:
		return fmt.Errorf("invalid Docker collection status")
	}
	switch s.ErrorCode {
	case "", "cli_missing", "engine_unavailable", "collection_failed", "collection_timeout", "limit_exceeded", "platform_unsupported", "disabled":
	default:
		return fmt.Errorf("invalid Docker error code")
	}
	if s.Status == DockerAvailable && s.ErrorCode != "" {
		return fmt.Errorf("available snapshot cannot contain an error")
	}
	if s.Status != DockerAvailable && len(s.Containers) != 0 {
		return fmt.Errorf("failed snapshot cannot replace inventory")
	}
	if len(s.Containers) > MaxDockerContainers {
		return fmt.Errorf("too many Docker containers")
	}
	seen := map[string]bool{}
	seenNames := map[string]bool{}
	for _, c := range s.Containers {
		if !ValidDockerID(c.ID) || seen[c.ID] || !dockerName.MatchString(c.Name) || seenNames[c.Name] || c.Image == "" || !dockerText(c.Image, 1024) || !dockerText(c.ComposeProject, 255) || !dockerText(c.ComposeService, 255) {
			return fmt.Errorf("invalid Docker container identity")
		}
		seen[c.ID] = true
		seenNames[c.Name] = true
		switch c.State {
		case "created", "running", "paused", "restarting", "removing", "exited", "dead":
		default:
			return fmt.Errorf("invalid Docker container state")
		}
		switch c.Health {
		case "none", "starting", "healthy", "unhealthy":
		default:
			return fmt.Errorf("invalid Docker health state")
		}
		if c.RestartCount < 0 || c.RestartCount > 1<<53-1 {
			return fmt.Errorf("invalid Docker restart count")
		}
		if c.CPUPercent != nil && (math.IsNaN(*c.CPUPercent) || math.IsInf(*c.CPUPercent, 0) || *c.CPUPercent < 0 || *c.CPUPercent > 1e8) {
			return fmt.Errorf("invalid Docker CPU value")
		}
		for _, v := range []*int64{c.MemoryUsedBytes, c.MemoryLimitBytes} {
			if v != nil && (*v < 0 || *v > 1<<53-1) {
				return fmt.Errorf("invalid Docker memory value")
			}
		}
		if c.StartedAt != nil && (c.StartedAt.Year() < 1970 || c.StartedAt.Year() > 9999) {
			return fmt.Errorf("invalid Docker start time")
		}
	}
	return nil
}

type DockerCheckConfig struct {
	Container string `json:"container"`
	Mode      string `json:"mode"`
}

func NormalizeDockerCheck(raw json.RawMessage) (DockerCheckConfig, error) {
	var c DockerCheckConfig
	if len(raw) == 0 || len(raw) > 4096 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return c, fmt.Errorf("Docker check config must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid Docker check config")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("Docker check config must contain one object")
	}
	c.Container = strings.TrimSpace(c.Container)
	if !dockerName.MatchString(c.Container) {
		return c, fmt.Errorf("config.container must be one exact Docker container name")
	}
	if c.Mode == "" {
		c.Mode = "running"
	}
	if c.Mode != "running" && c.Mode != "healthy" {
		return c, fmt.Errorf("config.mode must be running or healthy")
	}
	return c, nil
}
