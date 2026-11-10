package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestServiceCheckFixedCommandsAndSanitizedState(t *testing.T) {
	for _, tt := range []struct {
		name, goos, kind, raw, stdout, program string
		args                                   []string
		healthy                                bool
	}{
		{"Linux active", "linux", protocol.CheckLinuxService, `{"service":"sshd"}`, "LoadState=loaded\nActiveState=active\nSubState=running\n", "/usr/bin/systemctl", []string{"show", "--no-pager", "--property=LoadState,ActiveState,SubState", "--", "sshd.service"}, true},
		{"Linux oneshot", "linux", protocol.CheckLinuxService, `{"service":"worker.service"}`, "LoadState=loaded\nActiveState=active\nSubState=exited\n", "/usr/bin/systemctl", []string{"show", "--no-pager", "--property=LoadState,ActiveState,SubState", "--", "worker.service"}, true},
		{"macOS running", "darwin", protocol.CheckMacService, `{"service":"com.test.worker"}`, "system/com.test.worker = {\n\tstate = running\n\tenvironment = {\n\t\tAPI_KEY = never-forward-this\n\t}\n}\n", "/bin/launchctl", []string{"print", "system/com.test.worker"}, true},
		{"macOS on demand", "darwin", protocol.CheckMacService, `{"service":"gui/501/com.test.worker","state":"loaded"}`, "gui/501/com.test.worker = {\n\tstate = not running\n}\n", "/bin/launchctl", []string{"print", "gui/501/com.test.worker"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			ok, out := serviceCheck(context.Background(), tt.goos, tt.kind, []byte(tt.raw), func(ctx context.Context, program string, args ...string) ([]byte, error) {
				called = true
				if program != tt.program || !reflect.DeepEqual(args, tt.args) {
					t.Fatalf("command %s %q", program, args)
				}
				if deadline, present := ctx.Deadline(); !present || time.Until(deadline) > 10*time.Second {
					t.Fatal("missing query deadline")
				}
				return []byte(tt.stdout), nil
			})
			if !called || ok != tt.healthy || strings.Contains(out, "never-forward-this") {
				t.Fatalf("healthy=%v output=%q", ok, out)
			}
		})
	}
}

func TestServiceCheckFailsClosedWithoutLeakingOutput(t *testing.T) {
	for _, tt := range []struct {
		kind, goos, raw, out string
		err                  error
	}{
		{protocol.CheckLinuxService, "linux", `{"service":"sshd"}`, "ActiveState=active\nsecret-value", nil},
		{protocol.CheckLinuxService, "linux", `{"service":"sshd"}`, "LoadState=loaded\nActiveState=inactive\nsecret-value", nil},
		{protocol.CheckLinuxService, "linux", `{"service":"sshd"}`, "LoadState=loaded\nActiveState=failed\nsecret-value", nil},
		{protocol.CheckLinuxService, "linux", `{"service":"sshd"}`, "LoadState=loaded\nActiveState=active\nActiveState=failed\nsecret-value", nil},
		{protocol.CheckLinuxService, "linux", `{"service":"sshd"}`, "secret-value", errors.New("secret-value")},
		{protocol.CheckMacService, "darwin", `{"service":"worker"}`, "system/other = {\n\tstate = running\nsecret-value", nil},
		{protocol.CheckMacService, "darwin", `{"service":"worker"}`, "system/worker = {\n\tenvironment = {\n\t\tstate = running\n\t\tSECRET = secret-value\n\t}\n}", nil},
		{protocol.CheckMacService, "darwin", `{"service":"worker"}`, "system/worker = {\n\tstate = running\n\tstate = not running\nsecret-value", nil},
		{protocol.CheckMacService, "darwin", `{"service":"worker"}`, "system/worker = {\n\tstate = \n\tstate = running\nsecret-value", nil},
		{protocol.CheckMacService, "darwin", `{"service":"worker"}`, "system/worker = {\n\tstate = not running\nsecret-value", nil},
		{protocol.CheckMacService, "darwin", `{"service":"worker","state":"loaded"}`, "secret-value", errors.New("secret-value")},
	} {
		ok, out := serviceCheck(context.Background(), tt.goos, tt.kind, []byte(tt.raw), func(context.Context, string, ...string) ([]byte, error) { return []byte(tt.out), tt.err })
		if ok || strings.Contains(out, "secret-value") {
			t.Fatalf("accepted or leaked service response: %v %q", ok, out)
		}
	}
	for _, tt := range []struct{ goos, kind, raw string }{
		{"windows", protocol.CheckLinuxService, `{"service":"sshd"}`},
		{"linux", protocol.CheckMacService, `{"service":"worker"}`},
		{"darwin", protocol.CheckMacService, `{"service":"worker;id"}`},
		{"linux", protocol.CheckLinuxService, `{"service":"--all"}`},
	} {
		ok, _ := serviceCheck(context.Background(), tt.goos, tt.kind, []byte(tt.raw), func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("invalid input reached command execution")
			return nil, nil
		})
		if ok {
			t.Fatal("invalid input passed")
		}
	}
}

func TestServiceCheckCancellationAndOutputBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, out := serviceCheck(ctx, "linux", protocol.CheckLinuxService, []byte(`{"service":"sshd"}`), func(ctx context.Context, _ string, _ ...string) ([]byte, error) { return nil, ctx.Err() })
	if ok || !strings.Contains(out, "cancelled") {
		t.Fatalf("cancellation=%v %q", ok, out)
	}
	cancelled := false
	buf := &serviceCapture{cancel: func() { cancelled = true }}
	n, err := buf.Write([]byte(strings.Repeat("x", maxServiceCapture-1)))
	if err != nil || n != maxServiceCapture-1 || cancelled {
		t.Fatal("ordinary output cancelled")
	}
	n, err = buf.Write([]byte("overflow"))
	if err != nil || n != 8 || !cancelled || !buf.exceeded || buf.buf.Len() != maxServiceCapture {
		t.Fatal("capture limit not enforced")
	}
	buf.Write([]byte("more"))
	if buf.buf.Len() != maxServiceCapture {
		t.Fatal("capture grew past limit")
	}
}

// CI-only smoke reads existing system services; it never changes service state.
func TestServiceCheckNativeReadOnlyCI(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("native service query runs only in platform CI")
	}
	var kind, raw string
	switch runtime.GOOS {
	case "linux":
		kind, raw = protocol.CheckLinuxService, `{"service":"systemd-journald.service"}`
	case "darwin":
		kind, raw = protocol.CheckMacService, `{"service":"com.apple.notifyd","state":"loaded"}`
	default:
		t.Skip("service manager smoke requires Linux or macOS")
	}
	ok, out := runServiceCheck(context.Background(), kind, json.RawMessage(raw))
	if !ok {
		t.Fatalf("native read-only check: %s", out)
	}
}

func TestServiceCheckRunCheckProducesDurableSanitizedResult(t *testing.T) {
	for _, kind := range []string{protocol.CheckLinuxService, protocol.CheckMacService} {
		a := &agent{send: make(chan []byte, 2), checkOutbox: map[string]protocol.CheckResultMsg{}, runningChecks: map[string]struct{}{}, checkOutboxPath: t.TempDir() + "/check-outbox"}
		// Malformed input must fail in the new handler, before any native command.
		a.runCheck(protocol.RunCheckMsg{CheckID: "check-1", ExecutionID: "exec-1", Type: kind, Config: json.RawMessage(`{"service":"worker;secret-value"}`), TimeoutSec: 30})
		got := decodeQueuedCheckResult(t, <-a.send)
		if got.Success || got.Output != "Invalid service check configuration." || got.ExecutionID != "exec-1" {
			t.Fatalf("run_check result=%+v", got)
		}
		restarted := &agent{checkOutbox: map[string]protocol.CheckResultMsg{}, checkOutboxPath: a.checkOutboxPath}
		restarted.loadCheckOutbox()
		if persisted, ok := restarted.checkOutbox["exec-1"]; !ok || persisted.Output != got.Output {
			t.Fatal("new service result did not persist for delivery")
		}
	}
}
