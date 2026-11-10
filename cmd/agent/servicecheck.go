package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

const maxServiceCapture = 64 << 10

type serviceCommand func(context.Context, string, ...string) ([]byte, error)

func runServiceCheck(ctx context.Context, kind string, raw json.RawMessage) (bool, string) {
	return serviceCheck(ctx, runtime.GOOS, kind, raw, serviceCommandOutput)
}

func serviceCheck(ctx context.Context, goos, kind string, raw json.RawMessage, run serviceCommand) (bool, string) {
	cfg, err := protocol.NormalizeServiceCheck(kind, raw)
	if err != nil {
		return false, "Invalid service check configuration."
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch kind {
	case protocol.CheckLinuxService:
		if goos != "linux" {
			return false, "This service check requires Linux with systemd."
		}
		out, err := run(ctx, "/usr/bin/systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState", "--", cfg.Service)
		if err != nil {
			return false, serviceReadError(ctx, "systemd")
		}
		properties := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && (key == "LoadState" || key == "ActiveState" || key == "SubState") {
				if _, exists := properties[key]; exists {
					return false, "Ambiguous systemd service state."
				}
				properties[key] = value
			}
		}
		loaded, active := properties["LoadState"], properties["ActiveState"]
		if loaded != "loaded" || active != "active" {
			return false, fmt.Sprintf("Service %s is not active or could not be loaded.", cfg.Service)
		}
		return true, fmt.Sprintf("Service %s is active.", cfg.Service)
	case protocol.CheckMacService:
		if goos != "darwin" {
			return false, "This service check requires macOS with launchd."
		}
		out, err := run(ctx, "/bin/launchctl", "print", cfg.Service)
		if err != nil {
			return false, serviceReadError(ctx, "launchd")
		}
		// Never return launchctl's raw output: it can contain environment secrets.
		// Only the single root-level state belongs to the requested service.
		lines := strings.Split(string(out), "\n")
		if len(lines) == 0 || strings.TrimSpace(lines[0]) != cfg.Service+" = {" {
			return false, "Unrecognized launchd service response."
		}
		state := ""
		stateSeen := false
		for _, line := range lines {
			if strings.HasPrefix(line, "\tstate = ") {
				if stateSeen {
					return false, "Ambiguous launchd service state."
				}
				stateSeen = true
				state = strings.TrimSpace(strings.TrimPrefix(line, "\tstate = "))
			}
		}
		if state == "" {
			return false, "The launchd service state is unavailable."
		}
		if cfg.State == "loaded" {
			return true, fmt.Sprintf("Service %s is loaded.", cfg.Service)
		}
		if state != "running" {
			return false, fmt.Sprintf("Service %s is loaded but is not running.", cfg.Service)
		}
		return true, fmt.Sprintf("Service %s is running.", cfg.Service)
	}
	return false, "Unsupported service check."
}

func serviceReadError(ctx context.Context, manager string) string {
	if ctx.Err() != nil {
		return "Service status query timed out or was cancelled."
	}
	return "Unable to read service status from " + manager + "; verify the service name, manager availability and access."
}

type serviceCapture struct {
	buf      bytes.Buffer
	exceeded bool
	cancel   context.CancelFunc
}

func (b *serviceCapture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxServiceCapture - b.buf.Len()
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.exceeded = true
		b.cancel()
	} else {
		b.buf.Write(p)
	}
	return n, nil
}

func serviceCommandOutput(ctx context.Context, program string, args ...string) ([]byte, error) {
	if program == "/usr/bin/systemctl" {
		if _, err := os.Stat(program); err != nil {
			program = "/bin/systemctl"
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	buf := &serviceCapture{cancel: cancel}
	cmd.Stdout = buf
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if buf.exceeded {
		return nil, fmt.Errorf("service output budget exceeded")
	}
	return buf.buf.Bytes(), err
}
