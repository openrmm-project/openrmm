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
	for _, vars := range []map[string]string{{"PATH": "bad"}, {"RMM_UNKNOWN": "bad"}, {"OPENRMM_PARAM_X": "bad"}, {"CUSTOM_KEY": "has\nnewline"}} {
		if _, err := varEnv("sh", vars); err == nil {
			t.Fatal("invalid environment accepted")
		}
	}
}
func TestScriptVariableEnvironmentPreservesUserCaseAndLongValues(t *testing.T) {
	longName := "myKey_" + strings.Repeat("Ab", 4096)
	longValue := strings.Repeat("v", 65536)
	env, err := varEnv("sh", map[string]string{longName: longValue, "smallKey": "x"})
	if err != nil || len(env) != 2 || env[0] != longName+"="+longValue || env[1] != "smallKey=x" {
		t.Fatal("case or field length changed")
	}
	for _, vars := range []map[string]string{{"Path": "unsafe"}, {"myKey": "first", "MYKEY": "second"}} {
		if _, err := varEnv("powershell", vars); err == nil {
			t.Fatal("reserved or duplicate name accepted")
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
	code, out, stderr = runScriptCtxEnvSecrets(context.Background(), "sh", `printf '%s' "$customerKey"`, []string{"customerKey=q7X"}, []string{"q7X"})
	if code != 0 || strings.Contains(out+stderr, "q7X") || out != "***" {
		t.Fatal("mixed-case variable or short secret masking failed")
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
	code, out, stderr = runScriptCtxEnvSecrets(context.Background(), "powershell", `Write-Output $env:customerKey`, []string{"customerKey=q7X"}, []string{"q7X"})
	if code != 0 || strings.Contains(out+stderr, "q7X") || !strings.Contains(out, "***") {
		t.Fatal("PowerShell mixed-case variable or short secret masking failed")
	}
	code, out, stderr = runScriptCtxEnv(context.Background(), "cmd", "echo OPENRMM_CMD_CONTROL", nil)
	if code != 0 || !strings.Contains(out, "OPENRMM_CMD_CONTROL") {
		t.Fatal("ordinary CMD execution regressed")
	}
}

// A variable never replaces one the device's own environment defines,
// whatever the case: the child environment is the agent's followed by the
// variables, and the last one would win.
func TestScriptVariablesNeverReplaceTheDeviceEnvironment(t *testing.T) {
	// Names the denylist does not know: only the device environment rules them out.
	base := []string{"Corp_Share=\\\\fileserver\\installers", "PATH=/usr/bin", "LAB_SETTING=device", "=C:=C:\\"}
	for _, name := range []string{"CORP_SHARE", "LAB_SETTING_X", "lab_setting"} {
		_, err := varEnvOver("powershell", map[string]string{name: "\\\\attacker\\share"}, base)
		if name == "LAB_SETTING_X" {
			if err != nil {
				t.Fatalf("a new name was refused: %v", err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "already defines") {
			t.Fatalf("%s replaced the device environment: %v", name, err)
		}
	}
	env, err := varEnvOver("sh", map[string]string{"HUNTRESS_ACCOUNT_KEY": "k", "RMM_AGENT_HOSTNAME": "PC 01"}, append(base, "RMM_AGENT_HOSTNAME=agent"))
	if err != nil || strings.Join(env, ",") != "HUNTRESS_ACCOUNT_KEY=k,RMM_AGENT_HOSTNAME=PC 01" {
		t.Fatalf("ordinary variables and built-ins: %v %v", env, err)
	}
}

// End to end through the job runner: a colliding name fails the job before the
// script runs; the same job with a free name runs with the value.
func TestScriptVariableCollisionFailsTheJobBeforeTheScriptRuns(t *testing.T) {
	t.Setenv("LAB_DEVICE_SETTING", "device-value")
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	previous := runJobScriptVars
	t.Cleanup(func() { runJobScriptVars = previous })
	ran := 0
	var gotEnv []string
	runJobScriptVars = func(_ context.Context, _ string, _ string, env []string, _ []string) (int, string, string) {
		ran++
		gotEnv = env
		return 0, "ok", ""
	}
	a.runJob(protocol.JobMsg{JobID: "collide", Type: "run_script", Shell: "sh", Script: "echo", Vars: map[string]string{"lab_device_setting": "attacker"}})
	res := queuedResult(t, a)
	if ran != 0 || res.ExitCode == 0 || !strings.Contains(res.Stderr, "already defines") {
		t.Fatalf("colliding job: ran=%d exit=%d stderr=%q", ran, res.ExitCode, res.Stderr)
	}
	a.runJob(protocol.JobMsg{JobID: "free", Type: "run_script", Shell: "sh", Script: "echo", Vars: map[string]string{"CUSTOMER_KEY": "k"}})
	res = queuedResult(t, a)
	if ran != 1 || res.ExitCode != 0 || !strings.Contains(strings.Join(gotEnv, "\n"), "CUSTOMER_KEY=k") {
		t.Fatalf("free job: ran=%d exit=%d env=%v", ran, res.ExitCode, gotEnv)
	}
}
