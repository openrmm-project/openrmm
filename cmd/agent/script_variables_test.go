package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestScriptVariableEnvironmentValidation(t *testing.T) {
	for _, shell := range []string{"bash", "sh", "powershell", "python"} {
		env, err := varEnv(shell, map[string]string{"CUSTOM_KEY": "$(printf injected); & x", "RMM_AGENT_HOSTNAME": "PC 01"})
		if err != nil || strings.Join(env, "\n") != "CUSTOM_KEY=$(printf injected); & x\nRMM_AGENT_HOSTNAME=PC 01" {
			t.Fatalf("environment = %v, %v", env, err)
		}
	}
	for _, vars := range []map[string]string{{"PATH": "bad"}, {"RMM_UNKNOWN": "bad"}, {"OPENRMM_PARAM_X": "bad"}, {"bad": "x"}, {"CUSTOM_KEY": "has\nnewline"}, {"CUSTOM_KEY": strings.Repeat("x", 4097)}} {
		if _, err := varEnv("sh", vars); err == nil {
			t.Fatal("invalid environment accepted")
		}
	}
}
func TestScriptVariableOutputMaskedBeforeDurableOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := newDedupeAgent(t, path)
	previous := runJobScriptVars
	t.Cleanup(func() { runJobScriptVars = previous })
	const secret = "synthetic-customer-key"
	runJobScriptVars = func(context.Context, string, string, []string, []string) (int, string, string) {
		return 0, "out " + secret, "err " + secret
	}
	a.runJob(protocol.JobMsg{JobID: "vars", Type: "run_script", Shell: "sh", Script: "echo", Vars: map[string]string{"CUSTOM_KEY": secret}, SecretVars: []string{"CUSTOM_KEY"}})
	res := queuedResult(t, a)
	if strings.Contains(res.Stdout+res.Stderr, secret) {
		t.Fatal("wire result contains secret")
	}
	raw, err := os.ReadFile(a.outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("durable outbox contains secret")
	}
}
func TestScriptVariablesMaskBeforeOutputTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell control; Windows batch test below")
	}
	// The token crosses the old truncation boundary: redacting only in runJob
	// would have left a partial credential in the outbox.
	script := "printf '%1048572s' ''; printf '%s' \"$CUSTOM_KEY\""
	code, out, stderr := runScriptCtxEnvSecrets(context.Background(), "sh", script, []string{"CUSTOM_KEY=synthetic-secret"}, []string{"synthetic-secret"})
	if code != 0 {
		t.Fatal(stderr)
	}
	if strings.Contains(out, "synt") {
		t.Fatal("partial secret survived truncation")
	}
	code, out, stderr = runScriptCtxEnv(context.Background(), "sh", "printf '%s' \"$RMM_AGENT_HOSTNAME\"", []string{"RMM_AGENT_HOSTNAME=$(printf injected)"})
	if code != 0 || out != "$(printf injected)" {
		t.Fatalf("context was interpolated: %q %s", out, stderr)
	}
}

// Regression: on the initial commit this accepts the canary as environment
// data; CMD would subsequently interpret the ampersand during percent expansion.
func TestScriptVariablesRejectCMDBeforeExecution(t *testing.T) {
	for _, vars := range []map[string]string{{"CUSTOM_KEY": "value & echo OPENRMM_INJECTION_CANARY"}, {"RMM_CLIENT_NAME": "value & echo OPENRMM_INJECTION_CANARY"}} {
		if _, err := varEnv("cmd", vars); err == nil {
			t.Fatal("CMD variable injection accepted")
		}
	}
	env, err := varEnv("cmd", nil)
	if err != nil || len(env) != 0 {
		t.Fatal("ordinary CMD control refused")
	}
}
func TestScriptVariablesWindowsPowerShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell runner")
	}
	secret := "synthetic-key & echo OPENRMM_INJECTION_CANARY"
	code, out, stderr := runScriptCtxEnvSecrets(context.Background(), "powershell", `Write-Output $env:CUSTOM_KEY`, []string{"CUSTOM_KEY=" + secret}, []string{secret})
	if code != 0 || strings.Contains(out+stderr, secret) || !strings.Contains(out, "********") {
		t.Fatal("PowerShell environment/masking failed")
	}
	code, out, stderr = runScriptCtxEnv(context.Background(), "cmd", "echo OPENRMM_CMD_CONTROL", nil)
	if code != 0 || !strings.Contains(out, "OPENRMM_CMD_CONTROL") {
		t.Fatal("ordinary CMD execution regressed")
	}
}
