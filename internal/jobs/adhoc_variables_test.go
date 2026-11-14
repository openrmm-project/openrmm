package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// An ad hoc job asked for with script variables (the API allows it only to a
// caller who could bind them to a saved script) waits for approval, carries the
// names and never a value, receives the values only at dispatch, and has them
// masked in its stored result — like a library run.
func TestAdHocJobWithVariablesWaitsAndResolvesAtDispatch(t *testing.T) {
	ctx := context.Background()
	svc, fs, sender := variableService()
	j, err := svc.ProposeWithVariables(ctx, "a1", "sh", `printf '%s' "$CUSTOM_KEY"`, "edited copy", "admin", []string{"CUSTOM_KEY"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != models.JobAwaitingApproval || !j.RequiresApproval || j.ScriptID != nil || strings.Join(j.Variables, ",") != "CUSTOM_KEY" {
		t.Fatalf("job: %+v", j)
	}
	if len(sender.frames) != 0 || len(fs.blobs) != 0 {
		t.Fatal("dispatched or stored a secret before approval")
	}
	if raw, _ := json.Marshal(fs.jobs[j.ID]); strings.Contains(string(raw), fs.secret) {
		t.Fatal("the job row holds the value")
	}
	if !strings.Contains(strings.Join(fs.audits, " "), "job.propose") {
		t.Fatalf("not audited: %v", fs.audits)
	}
	if _, err := svc.Approve(ctx, j.ID, "approver"); err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	var msg protocol.JobMsg
	if len(sender.frames) != 1 || json.Unmarshal(sender.frames[0], &envelope) != nil || json.Unmarshal(envelope.Data, &msg) != nil {
		t.Fatalf("frames: %d", len(sender.frames))
	}
	if msg.Vars["CUSTOM_KEY"] != fs.secret || msg.Vars["RMM_AGENT_ID"] != "a1" {
		t.Fatalf("vars at dispatch: %v", msg.Vars)
	}
	if err := svc.OnResult(ctx, "a1", protocol.JobResultMsg{JobID: j.ID, Success: true, Stdout: "key=" + fs.secret}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fs.jobs[j.ID].Stdout, fs.secret) {
		t.Fatal("secret in the stored result")
	}
}

// What a library run with variables refuses, an ad hoc one refuses too, before
// any job exists: an agent older than 0.11, a value that does not resolve for
// the device, and CMD.
func TestAdHocJobWithVariablesRefusesWhatALibraryRunRefuses(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(*variableStore) (string, string){
		"old agent": func(fs *variableStore) (string, string) { fs.versions["a1"] = "0.10.0"; return "sh", "echo" },
		"no value":  func(fs *variableStore) (string, string) { fs.secret = ""; return "sh", "echo" },
		"cmd":       func(*variableStore) (string, string) { return "cmd", "echo %CUSTOM_KEY%" },
		"bad shell": func(*variableStore) (string, string) { return "zsh", "echo" },
		"no device": func(fs *variableStore) (string, string) { fs.agents["a1"] = false; return "sh", "echo" },
	} {
		svc, fs, sender := variableService()
		shell, script := setup(fs)
		if _, err := svc.ProposeWithVariables(ctx, "a1", shell, script, "x", "admin", []string{"CUSTOM_KEY"}, false); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if len(fs.jobs) != 0 || len(sender.frames) != 0 {
			t.Fatalf("%s: a refused job was created or sent", name)
		}
	}
	// No names at all is an ordinary approval-gated job.
	svc, _, sender := variableService()
	j, err := svc.ProposeWithVariables(ctx, "a1", "sh", "echo hi", "plain", "admin", nil, true)
	if err != nil || j.Status != models.JobAwaitingApproval || len(j.Variables) != 0 || len(sender.frames) != 0 {
		t.Fatalf("plain: %+v %v", j, err)
	}
}

// A person the API lets skip approval (the instance allows it; jobs.manage and
// settings.manage) sends a job with variables at once: the values still resolve
// only at dispatch, and the result is masked. The same holds for a library run
// with variables or parameters. Refusals still create nothing.
func TestJobsWithVariablesSkipApprovalOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(*Service) (*models.Job, error){
		"ad hoc": func(svc *Service) (*models.Job, error) {
			return svc.ProposeWithVariables(ctx, "a1", "sh", `printf '%s' "$CUSTOM_KEY"`, "now", "admin", []string{"CUSTOM_KEY"}, false)
		},
		"library": func(svc *Service) (*models.Job, error) {
			return svc.RunScriptWithParams(ctx, "a1", &models.Script{ID: "s1", Shell: "sh", Body: `printf '%s' "$CUSTOM_KEY"`, Variables: []string{"CUSTOM_KEY"}}, nil, "library: s1", "admin", false)
		},
	} {
		svc, fs, sender := variableService()
		j, err := run(svc)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if j.RequiresApproval || j.Status == models.JobAwaitingApproval || len(sender.frames) != 1 {
			t.Fatalf("%s: not sent at once: %+v, %d frames", name, j, len(sender.frames))
		}
		var envelope protocol.Envelope
		var msg protocol.JobMsg
		if json.Unmarshal(sender.frames[0], &envelope) != nil || json.Unmarshal(envelope.Data, &msg) != nil || msg.Vars["CUSTOM_KEY"] != fs.secret {
			t.Fatalf("%s: vars at dispatch: %v", name, msg.Vars)
		}
		if raw, _ := json.Marshal(fs.jobs[j.ID]); strings.Contains(string(raw), fs.secret) {
			t.Fatalf("%s: the job row holds the value", name)
		}
		if err := svc.OnResult(ctx, "a1", protocol.JobResultMsg{JobID: j.ID, Success: true, Stdout: fs.secret}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fs.jobs[j.ID].Stdout, fs.secret) {
			t.Fatalf("%s: secret in the stored result", name)
		}
	}
	// The AI's path keeps approval.
	svc, _, sender := variableService()
	j, err := svc.ProposeScriptWithParams(ctx, "a1", &models.Script{ID: "s1", Shell: "sh", Body: "echo", Variables: []string{"CUSTOM_KEY"}}, nil, "ai", "ai:chat")
	if err != nil || !j.RequiresApproval || len(sender.frames) != 0 {
		t.Fatalf("ProposeScriptWithParams skipped approval: %+v %v", j, err)
	}
}
