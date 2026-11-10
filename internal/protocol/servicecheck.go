package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	CheckLinuxService = "linuxservice"
	CheckMacService   = "macservice"
)

type ServiceCheckConfig struct {
	Service string `json:"service"`
	State   string `json:"state,omitempty"` // macOS: running (default) or loaded
}

var linuxServiceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@-]{0,239}$`)
var launchdLabel = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,239}$`)
var launchdUserDomain = regexp.MustCompile(`^(gui|user)/[0-9]{1,10}$`)

// NormalizeServiceCheck is shared by the API, AI/MCP and the receiving agent.
// Service names are single literal arguments, never commands or patterns.
func NormalizeServiceCheck(kind string, raw json.RawMessage) (ServiceCheckConfig, error) {
	var cfg ServiceCheckConfig
	if len(raw) == 0 || len(raw) > 4096 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return cfg, fmt.Errorf("service config must be an object of at most 4096 bytes")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid service config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return cfg, fmt.Errorf("service config must contain one object")
	}
	cfg.Service = strings.TrimSpace(cfg.Service)
	switch kind {
	case CheckLinuxService:
		if !linuxServiceName.MatchString(cfg.Service) {
			return cfg, fmt.Errorf("config.service must be one systemd service name")
		}
		if cfg.State != "" {
			return cfg, fmt.Errorf("config.state is only supported for macOS services")
		}
		if !strings.HasSuffix(cfg.Service, ".service") {
			for _, suffix := range []string{".socket", ".timer", ".mount", ".automount", ".target", ".path", ".device", ".scope", ".slice", ".swap"} {
				if strings.HasSuffix(cfg.Service, suffix) {
					return cfg, fmt.Errorf("config.service must identify a .service unit")
				}
			}
			cfg.Service += ".service"
		}
	case CheckMacService:
		parts := strings.Split(cfg.Service, "/")
		label := cfg.Service
		if len(parts) == 1 {
			cfg.Service = "system/" + label
		} else if len(parts) == 2 && parts[0] == "system" {
			label = parts[1]
		} else if len(parts) == 3 && launchdUserDomain.MatchString(parts[0]+"/"+parts[1]) {
			label = parts[2]
		} else {
			return cfg, fmt.Errorf("config.service must be a label or system/label, gui/UID/label or user/UID/label")
		}
		if !launchdLabel.MatchString(label) {
			return cfg, fmt.Errorf("config.service contains an invalid launchd label")
		}
		if cfg.State == "" {
			cfg.State = "running"
		}
		if cfg.State != "running" && cfg.State != "loaded" {
			return cfg, fmt.Errorf("config.state must be running or loaded")
		}
	default:
		return cfg, fmt.Errorf("unsupported service check type")
	}
	return cfg, nil
}
