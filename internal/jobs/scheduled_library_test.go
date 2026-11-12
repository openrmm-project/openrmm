package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// A scheduled task (or "run now") that points at a library script runs that
// script as the library holds it: bound to its id and its variable names, with
// the values resolved only at dispatch. These are the refusals and the one path
// that dispatches, without a database.
func TestScheduledLibraryScriptRunsBoundAndRefusesWhatCannotRun(t *testing.T) {
	ctx := context.Background()
	task := "task-1"
	library := &models.Script{ID: "lib-1", Name: "Install vendor", Shell: "sh", Body: `printf '%s' "$CUSTOM_KEY"`, Variables: []string{"CUSTOM_KEY"}}

	svc, fs, sender := variableService()
	j, err := svc.RunAutomated(ctx, "a1", library.Shell, library.Body, "task: Install vendor", &task, library)
	if err != nil {
		t.Fatal(err)
	}
	if j.ScriptID == nil || *j.ScriptID != "lib-1" || strings.Join(j.Variables, ",") != "CUSTOM_KEY" || j.TaskID == nil || *j.TaskID != task {
		t.Fatalf("job not bound to its library script: %+v", j)
	}
	if j.RequiresApproval || j.CreatedBy != "scheduler" {
		t.Fatalf("a scheduled run is pre-approved by the task: %+v", j)
	}
	if len(sender.frames) != 1 {
		t.Fatalf("dispatched %d frames", len(sender.frames))
	}
	var envelope protocol.Envelope
	var msg protocol.JobMsg
	if err := json.Unmarshal(sender.frames[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Vars["CUSTOM_KEY"] != fs.secret || len(msg.SecretVars) != 1 {
		t.Fatal("the variable value did not reach the agent at dispatch")
	}
	if strings.Contains(fs.jobs[j.ID].Script, fs.secret) || strings.Contains(strings.Join(fs.audits, " "), fs.secret) {
		t.Fatal("the secret value was stored with the job or the audit")
	}

	for name, tc := range map[string]struct {
		version string
		script  *models.Script
		want    string
	}{
		"agent too old for variables": {"0.10.0", library, "0.11.0"},
		"interactive parameters": {"", &models.Script{ID: "lib-2", Shell: "sh", Body: "echo $1",
			Params: []models.ScriptParam{{Name: "who", Kind: "string"}}}, "interactive parameters"},
		"cmd with variables":  {"", &models.Script{ID: "lib-3", Shell: models.ShellCmd, Body: "echo %CUSTOM_KEY%", Variables: []string{"CUSTOM_KEY"}}, ""},
		"pending AI proposal": {"", &models.Script{ID: "lib-4", Shell: "sh", Body: "echo", ProposedBy: "mcp:claude", Pending: true}, "awaiting review"},
		"unsupported shell":   {"", &models.Script{ID: "lib-5", Shell: "fish", Body: "echo"}, "unsupported shell"},
	} {
		svc, fs, sender := variableService()
		if tc.version != "" {
			fs.versions["a1"] = tc.version
		}
		_, err := svc.RunAutomated(ctx, "a1", tc.script.Shell, tc.script.Body, "task", &task, tc.script)
		if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
		if name == "pending AI proposal" && !errors.Is(err, ErrScriptPending) {
			t.Errorf("pending proposal: %v is not ErrScriptPending", err)
		}
		if len(fs.jobs) != 0 || len(sender.frames) != 0 {
			t.Errorf("%s: a refused run created %d jobs and sent %d frames", name, len(fs.jobs), len(sender.frames))
		}
	}
}
