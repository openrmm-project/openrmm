package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

func frameOf(t *testing.T, typ string, payload any) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func nothingQueued(t *testing.T, a *agent) {
	t.Helper()
	select {
	case frame := <-a.send:
		t.Fatalf("queued %s", frame)
	case <-time.After(100 * time.Millisecond):
	}
}

// Whatever the server sends, a frame the agent cannot read is dropped: no
// panic, no reply, nothing run.
func TestHandleFrameDropsWhatItCannotRead(t *testing.T) {
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	runs := stubJobScript(t, nil)
	for _, raw := range [][]byte{
		[]byte(`not json`),
		[]byte(`{"type":"job","data":"not an object"}`),
		[]byte(`{"type":"something new","data":{}}`),
		frameOf(t, protocol.MsgCredentialRotate, protocol.CredentialRotateMsg{}),
		frameOf(t, protocol.MsgCredentialRotationCancel, protocol.CredentialRotationCancelMsg{}),
	} {
		a.handleFrame(raw)
	}
	nothingQueued(t, a)
	if runs.Load() != 0 {
		t.Fatal("a script ran from an unreadable frame")
	}
}

func TestHandleFrameRunsAJobAndReportsIt(t *testing.T) {
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	runs := stubJobScript(t, nil)
	a.handleFrame(frameOf(t, protocol.MsgJob, protocol.JobMsg{JobID: "job-1", Shell: "bash", Script: "echo hi"}))
	res := queuedResult(t, a)
	if res.JobID != "job-1" || !res.Success || res.ExitCode != 0 || res.Stdout != "done" {
		t.Fatalf("result = %+v", res)
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}
}

// job_cancel from the server stops the job's process: the result says so, and
// a canceled job never reads as a success.
func TestHandleFrameCancelsARunningJob(t *testing.T) {
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	started := make(chan struct{})
	previous := runJobScript
	runJobScript = func(ctx context.Context, shell, script string) (int, string, string) {
		close(started)
		<-ctx.Done()
		return 0, "partial", ""
	}
	t.Cleanup(func() { runJobScript = previous })

	a.handleFrame(frameOf(t, protocol.MsgJob, protocol.JobMsg{JobID: "job-2", Shell: "bash", Script: "sleep 600"}))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never started")
	}
	a.handleFrame(frameOf(t, protocol.MsgJobCancel, protocol.JobCancelMsg{JobID: "job-2"}))
	res := queuedResult(t, a)
	if res.JobID != "job-2" || res.Success || res.ExitCode != -1 || !strings.Contains(res.Stderr, "[canceled by operator]") {
		t.Fatalf("result = %+v", res)
	}
	// Canceling a job that is not running is a no-op.
	a.cancelJob("no-such-job")
}

func TestHandleFramePatchInstallWithoutIdentifiersIsRefused(t *testing.T) {
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	a.handleFrame(frameOf(t, protocol.MsgPatchInstall, protocol.PatchInstallMsg{RequestID: "r1"}))
	nothingQueued(t, a)
}
