package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A customer's cmd or PowerShell script printing accented text comes back
// intact. The text uses only letters that OEM pages 437 and 850 both hold, so it
// survives whichever page the console starts on. Before, the agent read that
// page as 1252 whenever it had no console of its own, as under the service.
func TestConsoleScriptOutputKeepsAccents(t *testing.T) {
	const want = "Olá, café, ç"
	for shell, script := range map[string]string{
		"powershell": `"` + want + `"`,
		"cmd":        "echo " + want,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		code, stdout, stderr := runScriptCtx(ctx, shell, script)
		cancel()
		if code != 0 {
			t.Fatalf("%s: exit = %d, stderr = %q", shell, code, stderr)
		}
		if !strings.Contains(stdout, want) {
			t.Errorf("%s: stdout = %q, want it to contain %q", shell, stdout, want)
		}
	}
}

// The agent's own PowerShell asks for UTF-8, so text no single code page holds
// (Portuguese, Russian, Japanese, Chinese and Korean at once) survives the JSON
// the agent parses. In a console page it would come back as question marks.
func TestAgentPowerShellIsLossless(t *testing.T) {
	const want = "Instalação · Установка · 更新プログラム · 安装更新 · 업데이트"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := agentPowerShell(ctx, `@{Message='`+want+`'} | ConvertTo-Json -Compress`)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Message string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("%v: output %q", err, out)
	}
	if got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
}
