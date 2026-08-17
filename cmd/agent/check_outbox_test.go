package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestCheckExecutionTimeoutUsesDispatchValueAndSafeFallback(t *testing.T) {
	tests := []struct {
		seconds int
		want    time.Duration
	}{
		{0, 2 * time.Minute},
		{4, 2 * time.Minute},
		{5, 5 * time.Second},
		{86400, 24 * time.Hour},
		{86401, 2 * time.Minute},
	}
	for _, tt := range tests {
		if got := checkExecutionTimeout(tt.seconds); got != tt.want {
			t.Errorf("checkExecutionTimeout(%d) = %s, want %s", tt.seconds, got, tt.want)
		}
	}
}

func decodeQueuedCheckResult(t *testing.T, frame []byte) protocol.CheckResultMsg {
	t.Helper()
	var env protocol.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.MsgCheckResult {
		t.Fatalf("frame type = %q, want %q", env.Type, protocol.MsgCheckResult)
	}
	var result protocol.CheckResultMsg
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCheckResultOutboxSurvivesRestartAndStopsAfterAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-state.json.check-outbox")
	want := protocol.CheckResultMsg{
		CheckID: "check-1", ExecutionID: "execution-1", Output: "finished", Success: true,
	}
	first := &agent{
		send: make(chan []byte, 4), checkOutbox: map[string]protocol.CheckResultMsg{},
		checkOutboxPath: path,
	}
	first.reportCheckResult(want)
	if got := decodeQueuedCheckResult(t, <-first.send); !reflect.DeepEqual(got, want) {
		t.Fatalf("queued result = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("outbox permissions = %o, want 600", got)
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatalf("temporary replacement remains after save: %v", err)
	}

	restarted := &agent{
		send: make(chan []byte, 4), outbox: map[string]protocol.JobResultMsg{},
		checkOutbox: map[string]protocol.CheckResultMsg{}, checkOutboxPath: path,
	}
	restarted.loadCheckOutbox()
	if got := restarted.checkOutbox[want.ExecutionID]; !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded result = %#v, want %#v", got, want)
	}
	restarted.flushOutbox()
	if got := decodeQueuedCheckResult(t, <-restarted.send); !reflect.DeepEqual(got, want) {
		t.Fatalf("retried result = %#v, want %#v", got, want)
	}

	restarted.ackCheckResult(want.ExecutionID)
	afterAck := &agent{checkOutbox: map[string]protocol.CheckResultMsg{}, checkOutboxPath: path}
	afterAck.loadCheckOutbox()
	if len(afterAck.checkOutbox) != 0 {
		t.Fatalf("acked result remained in durable outbox: %#v", afterAck.checkOutbox)
	}
}

func TestCheckOutboxRejectsCorruptOrMismatchedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "check-outbox")
	sentinel := protocol.CheckResultMsg{CheckID: "safe", ExecutionID: "safe"}
	a := &agent{
		checkOutboxPath: path,
		checkOutbox:     map[string]protocol.CheckResultMsg{"safe": sentinel},
	}
	if err := os.WriteFile(path, []byte(`{"broken"`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.loadCheckOutbox()
	if got := a.checkOutbox["safe"]; !reflect.DeepEqual(got, sentinel) {
		t.Fatalf("corrupt file replaced live state with %#v", a.checkOutbox)
	}

	bad := map[string]protocol.CheckResultMsg{
		"wrong-key":   {CheckID: "check", ExecutionID: "different"},
		"empty-check": {ExecutionID: "empty-check"},
		"valid":       {CheckID: "check", ExecutionID: "valid", Output: "ok"},
	}
	b, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	a.loadCheckOutbox()
	if len(a.checkOutbox) != 1 || a.checkOutbox["valid"].ExecutionID != "valid" {
		t.Fatalf("validated outbox = %#v, want only the valid entry", a.checkOutbox)
	}
}

func TestDuplicateRunCheckResendsStoredResultWithoutExecution(t *testing.T) {
	stored := protocol.CheckResultMsg{
		CheckID: "check-1", ExecutionID: "execution-1", Output: "stored result", Success: true,
	}
	a := &agent{
		send:          make(chan []byte, 2),
		checkOutbox:   map[string]protocol.CheckResultMsg{stored.ExecutionID: stored},
		runningChecks: map[string]struct{}{},
	}
	a.runCheck(protocol.RunCheckMsg{
		CheckID: stored.CheckID, ExecutionID: stored.ExecutionID,
		Type: "a duplicate must never execute this unknown type",
	})
	got := decodeQueuedCheckResult(t, <-a.send)
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("duplicate dispatch returned %#v, want stored %#v", got, stored)
	}
}
