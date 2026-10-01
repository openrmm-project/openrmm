package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/protocol"
)

// The server re-sends a job still marked "sent" on every reconnect. These
// pin that the agent turns that at-least-once delivery into at-most-once
// execution.

func newDedupeAgent(t *testing.T, statePath string) *agent {
	t.Helper()
	a := &agent{
		send: make(chan []byte, 16), running: map[string]context.CancelFunc{},
		outbox: map[string]protocol.JobResultMsg{}, outboxPath: statePath + ".outbox",
		started: map[string]time.Time{}, startedPath: statePath + ".started",
	}
	a.loadOutbox()
	a.loadStarted()
	return a
}

func stubJobScript(t *testing.T, run func()) *atomic.Int64 {
	t.Helper()
	var runs atomic.Int64
	previous := runJobScript
	runJobScript = func(context.Context, string, string) (int, string, string) {
		runs.Add(1)
		if run != nil {
			run()
		}
		return 0, "done", ""
	}
	t.Cleanup(func() { runJobScript = previous })
	return &runs
}

func queuedResult(t *testing.T, a *agent) protocol.JobResultMsg {
	t.Helper()
	select {
	case frame := <-a.send:
		var env protocol.Envelope
		if err := json.Unmarshal(frame, &env); err != nil || env.Type != protocol.MsgJobResult {
			t.Fatalf("queued frame = %s, %v", frame, err)
		}
		var res protocol.JobResultMsg
		if err := json.Unmarshal(env.Data, &res); err != nil {
			t.Fatal(err)
		}
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("no job result was queued")
	}
	return protocol.JobResultMsg{}
}

func TestRepeatedDispatchWhileRunningRunsOnce(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	runs := stubJobScript(t, func() { entered <- struct{}{}; <-release })
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	job := protocol.JobMsg{JobID: "job-1", Shell: "bash", Script: "useradd x"}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); a.runJob(job) }()
	<-entered
	for i := 0; i < 3; i++ { // the reconnect storm
		a.runJob(job)
	}
	close(release)
	wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("job ran %d times, want once", got)
	}
	if res := queuedResult(t, a); res.JobID != "job-1" || !res.Success {
		t.Fatalf("result = %#v", res)
	}
}

func TestRepeatedDispatchBeforeAckDoesNotRerun(t *testing.T) {
	runs := stubJobScript(t, nil)
	state := filepath.Join(t.TempDir(), "state.json")
	a := newDedupeAgent(t, state)
	job := protocol.JobMsg{JobID: "job-2", Shell: "bash", Script: "reboot"}
	a.runJob(job)
	queuedResult(t, a)

	// Same process, and after a restart: the result is still unacked.
	a.runJob(job)
	newDedupeAgent(t, state).runJob(job)
	if got := runs.Load(); got != 1 {
		t.Fatalf("job ran %d times, want once", got)
	}

	// Once the server acks the result it records the job as finished; the
	// start record is gone with the outbox entry.
	a.ackResult(job.JobID)
	restarted := newDedupeAgent(t, state)
	if len(restarted.started) != 0 || len(restarted.outbox) != 0 {
		t.Fatalf("acked job left records behind: started=%v outbox=%v", restarted.started, restarted.outbox)
	}
}

func TestJobInterruptedByRestartIsReportedNotRerun(t *testing.T) {
	runs := stubJobScript(t, nil)
	state := filepath.Join(t.TempDir(), "state.json")
	crashed := newDedupeAgent(t, state)
	if !crashed.beginJob("job-3") { // started, then the process died
		t.Fatal("a new job was refused")
	}

	restarted := newDedupeAgent(t, state)
	restarted.runJob(protocol.JobMsg{JobID: "job-3", Shell: "bash", Script: "install"})
	if got := runs.Load(); got != 0 {
		t.Fatalf("interrupted job ran again (%d times)", got)
	}
	res := queuedResult(t, restarted)
	if res.JobID != "job-3" || res.Success || res.ExitCode != -1 || res.Stderr == "" {
		t.Fatalf("interrupted job result = %#v, want a reported failure", res)
	}
	if _, kept := restarted.outbox["job-3"]; !kept {
		t.Fatal("the failure result is not in the outbox, so it would not survive another restart")
	}

	// Control: a job never seen before still runs.
	restarted.runJob(protocol.JobMsg{JobID: "job-4", Shell: "bash", Script: "echo"})
	if got := runs.Load(); got != 1 {
		t.Fatalf("new job ran %d times, want once", got)
	}
}

func TestStaleStartRecordsAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := map[string]time.Time{
		"stale": time.Now().Add(-startedRetention - time.Hour),
		"fresh": time.Now().Add(-time.Hour),
	}
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".started", b, 0o600); err != nil {
		t.Fatal(err)
	}
	a := newDedupeAgent(t, path)
	if _, ok := a.started["stale"]; ok {
		t.Fatal("a start record past the retention window was kept")
	}
	if _, ok := a.started["fresh"]; !ok {
		t.Fatal("a recent start record was dropped")
	}
}
