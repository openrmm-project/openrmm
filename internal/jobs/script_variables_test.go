package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/scriptvars"
)

type variableStore struct {
	*fakeStore
	blobs            map[string]string
	secret           string
	readErr, saveErr bool
}

func (f *variableStore) ResolveScriptVariables(_ context.Context, _ *config.Config, agentID string, names []string) (map[string]string, []string, error) {
	env := scriptvars.Builtins(scriptvars.Device{AgentID: agentID, Hostname: "Test PC"})
	if len(names) == 0 {
		return env, nil, nil
	}
	if f.secret == "" {
		return nil, nil, errors.New("variable unset")
	}
	for _, name := range names {
		env[name] = f.secret
	}
	return env, []string{f.secret}, nil
}
func (f *variableStore) SaveJobRedactions(_ context.Context, cfg *config.Config, id string, secrets []string) error {
	if f.saveErr {
		return errors.New("DB unavailable")
	}
	blob, err := scriptvars.Seal(cfg, secrets)
	f.blobs[id] = blob
	return err
}
func (f *variableStore) JobRedactions(_ context.Context, id string) (string, error) {
	if f.readErr {
		return "", errors.New("DB unavailable")
	}
	return f.blobs[id], nil
}

type variableSender struct{ frames [][]byte }

func (f *variableSender) Send(_ string, frame []byte) bool {
	f.frames = append(f.frames, frame)
	return true
}
func variableService() (*Service, *variableStore, *variableSender) {
	fs := &variableStore{fakeStore: newFakeStore(), blobs: map[string]string{}, secret: "synthetic-customer-key"}
	sender := &variableSender{}
	return New(fs, sender).WithSettings(&config.Config{SettingsEncryptionKey: "synthetic-instance-key"}), fs, sender
}
func TestScriptVariablesResolveOnlyAtDispatchAndMaskStoredOutput(t *testing.T) {
	ctx := context.Background()
	svc, fs, sender := variableService()
	sc := &models.Script{ID: "script-1", Shell: "sh", Body: "printf '%s' \"$CUSTOM_KEY\"", Variables: []string{"CUSTOM_KEY"}}
	j, err := svc.ProposeScriptWithParams(ctx, "a1", sc, nil, "test", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.frames) != 0 || len(fs.blobs) != 0 {
		t.Fatal("proposal dispatched or persisted plaintext")
	}
	fs.secret = "rotated-customer-key"
	if _, err := svc.Approve(ctx, j.ID, "approver"); err != nil {
		t.Fatal(err)
	}
	var msg protocol.JobMsg
	var envelope protocol.Envelope
	if err := json.Unmarshal(sender.frames[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Vars["CUSTOM_KEY"] != fs.secret || msg.Vars["RMM_AGENT_ID"] != "a1" {
		t.Fatal("runtime values missing")
	}
	raw, _ := json.Marshal(fs.jobs[j.ID])
	if strings.Contains(string(raw), fs.secret) || strings.Contains(fs.blobs[j.ID], fs.secret) {
		t.Fatal("secret persisted in clear")
	}
	if err := svc.OnResult(ctx, "a1", protocol.JobResultMsg{JobID: j.ID, Success: true, Stdout: fs.secret, Stderr: "error " + fs.secret}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fs.jobs[j.ID].Stdout+fs.jobs[j.ID].Stderr, fs.secret) {
		t.Fatal("secret in job history")
	}
}
func TestScriptVariablesFailClosedOnRedactionReadOrWrite(t *testing.T) {
	for _, mode := range []string{"read", "decrypt", "write"} {
		t.Run(mode, func(t *testing.T) {
			svc, fs, sender := variableService()
			j, err := svc.ProposeScriptWithParams(context.Background(), "a1", &models.Script{Shell: "sh", Body: "echo", Variables: []string{"CUSTOM_KEY"}}, nil, "test", "user")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "write" {
				fs.saveErr = true
			}
			if _, err := svc.Approve(context.Background(), j.ID, "approver"); err != nil {
				t.Fatal(err)
			}
			if mode == "write" {
				if len(sender.frames) != 0 {
					t.Fatal("sent without redaction snapshot")
				}
				return
			}
			if mode == "read" {
				fs.readErr = true
			} else {
				fs.blobs[j.ID] = "enc:v1:broken"
			}
			if err := svc.OnResult(context.Background(), "a1", protocol.JobResultMsg{JobID: j.ID, Success: true, Stdout: fs.secret}); err != nil {
				t.Fatal(err)
			}
			if fs.jobs[j.ID].Stdout != "" || !strings.Contains(fs.jobs[j.ID].Stderr, "withheld") {
				t.Fatal("redaction failure leaked output")
			}
		})
	}
	// Control: an ordinary result is kept when it has no injected secrets.
	svc, fs, _ := variableService()
	j, _ := svc.Propose(context.Background(), "a1", "sh", "echo hi", "d", "user", false)
	if err := svc.OnResult(context.Background(), "a1", protocol.JobResultMsg{JobID: j.ID, Success: true, Stdout: "ordinary output"}); err != nil {
		t.Fatal(err)
	}
	if fs.jobs[j.ID].Stdout != "ordinary output" {
		t.Fatal("ordinary output lost")
	}
}
func TestScriptVariablesRequireCapableAgent(t *testing.T) {
	svc, fs, sender := variableService()
	fs.versions["a1"] = "0.10.0"
	if _, err := svc.ProposeScriptWithParams(context.Background(), "a1", &models.Script{Shell: "sh", Body: "echo", Variables: []string{"CUSTOM_KEY"}}, nil, "test", "user"); err == nil {
		t.Fatal("old agent accepted")
	}
	if len(fs.jobs) != 0 {
		t.Fatal("failed proposal created a job")
	}
	j, err := svc.Propose(context.Background(), "a1", "sh", "echo", "control", "user", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.frames) != 1 || j.Status != models.JobSent {
		t.Fatal("ordinary old-agent run regressed")
	}
}
