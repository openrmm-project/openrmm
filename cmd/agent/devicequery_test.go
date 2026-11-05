package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/protocol"
)

func TestParseSystemctlUnits(t *testing.T) {
	out := []byte("cron.service loaded active running Regular background program processing daemon\n" +
		"ssh.service loaded inactive dead OpenBSD Secure Shell server\n" +
		"user@1000.service loaded active running User Manager for UID 1000\n" +
		"not-a-service.socket loaded active listening Socket\n")
	rows := parseSystemctlUnits(out)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Name != "cron" || !rows[0].Running || rows[0].State != "active/running" {
		t.Fatalf("cron = %+v", rows[0])
	}
	if rows[1].Name != "ssh" || rows[1].Running {
		t.Fatalf("ssh = %+v", rows[1])
	}
}

func TestParseLaunchctlList(t *testing.T) {
	rows := parseLaunchctlList([]byte("PID\tStatus\tLabel\n123\t0\tcom.apple.Finder\n-\t78\tcom.example.stopped\n"))
	if len(rows) != 2 || !rows[0].Running || rows[1].Running || rows[0].Name != "com.apple.Finder" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestParseWindowsServicesAcceptsOneOrMany(t *testing.T) {
	one, err := parseWindowsServices([]byte(`{"Name":"Spooler","DisplayName":"Print Spooler","Status":"Running","StartType":"Automatic"}`))
	if err != nil || len(one) != 1 || !one[0].Running {
		t.Fatalf("one = %+v %v", one, err)
	}
	many, err := parseWindowsServices([]byte(`[{"Name":"a","Status":"Stopped"},{"Name":"b","Status":"Running"}]`))
	if err != nil || len(many) != 2 || many[0].Running || !many[1].Running {
		t.Fatalf("many = %+v %v", many, err)
	}
}

func TestParseWindowsEventsShortensMessages(t *testing.T) {
	long := strings.Repeat("x", 500)
	rows, err := parseWindowsEvents([]byte(`{"Time":"2026-10-30T10:00:00Z","Log":"System","Source":"Disk","Id":7,"Level":"Error","Message":"first line\r\nsecond ` + long + `"}`))
	if err != nil || len(rows) != 1 || rows[0].Message != "first line" || rows[0].ID != 7 {
		t.Fatalf("rows = %+v %v", rows, err)
	}
	if rows, err := parseWindowsEvents(nil); err != nil || rows != nil {
		t.Fatalf("no events: %+v %v", rows, err)
	}
}

func TestParseJournalReadsTextAndByteMessages(t *testing.T) {
	out := []byte(`{"__REALTIME_TIMESTAMP":"1761818400000000","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"3","MESSAGE":"disk error"}` + "\n" +
		`{"__REALTIME_TIMESTAMP":"1761818460000000","_COMM":"app","PRIORITY":"2","MESSAGE":[104,105]}` + "\n" +
		`not json` + "\n")
	rows := parseJournal(out)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Source != "kernel" || rows[0].Level != "error" || rows[0].Message != "disk error" || rows[0].Time == "" {
		t.Fatalf("first = %+v", rows[0])
	}
	if rows[1].Source != "app" || rows[1].Message != "hi" || rows[1].Level != "critical" {
		t.Fatalf("second = %+v", rows[1])
	}
}

func TestParamEnvTurnsParametersIntoVariables(t *testing.T) {
	env, err := paramEnv("powershell", map[string]string{"service": "Spooler", "mode": "fast"})
	if err != nil || strings.Join(env, ";") != "OPENRMM_PARAM_MODE=fast;OPENRMM_PARAM_SERVICE=Spooler" {
		t.Fatalf("env = %v %v", env, err)
	}
	for name, params := range map[string]map[string]string{
		"bad name":   {"Service": "x"},
		"newline":    {"service": "a\nRestart-Computer"},
		"nul":        {"service": "a\x00b"},
		"too long":   {"service": strings.Repeat("a", 201)},
		"name shell": {"x;y": "a"},
	} {
		if _, err := paramEnv("bash", params); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := paramEnv("cmd", map[string]string{"x": "a"}); err == nil {
		t.Fatal("cmd took parameters")
	}
}

// A parameterised script reaches its value through the environment, and the
// value is never part of the script text.
func TestRunScriptCtxEnvPassesParametersAsData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	env, err := paramEnv("sh", map[string]string{"name": "$(touch /tmp/should-not-exist); echo pwned"})
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runScriptCtxEnv(context.Background(), "sh", `printf '%s' "$OPENRMM_PARAM_NAME"`, env)
	if code != 0 || stdout != "$(touch /tmp/should-not-exist); echo pwned" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
}

func TestDeviceQueryRefusesWhatTheCatalogDoesNotHold(t *testing.T) {
	for _, script := range []string{
		`not json`,
		`{"query":"files","params":{"path":"/etc/shadow"}}`,
		`{"query":"processes","params":{"limit":"1000"}}`,
		`{"query":"services","params":{"name":"x; rm -rf /"}}`,
		`{"query":"disk","params":{"extra":"1"}}`,
	} {
		if code, _, stderr := deviceQuery(context.Background(), script); code == 0 || stderr == "" {
			t.Errorf("%s: code=%d", script, code)
		}
	}
}

// The portable queries run for real here: they read the test machine.
func TestDeviceQueriesRunOnThisMachine(t *testing.T) {
	for _, q := range []string{
		`{"query":"disk"}`, `{"query":"system"}`, `{"query":"network"}`,
		`{"query":"processes","params":{"sort":"memory","limit":"3"}}`,
	} {
		code, stdout, stderr := deviceQuery(context.Background(), q)
		if code != 0 {
			t.Fatalf("%s: code=%d stderr=%s", q, code, stderr)
		}
		var out struct {
			Query  string          `json:"query"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || len(out.Result) == 0 {
			t.Fatalf("%s: %v %s", q, err, stdout)
		}
	}
	code, stdout, _ := deviceQuery(context.Background(), `{"query":"processes","params":{"limit":"2"}}`)
	var procs struct {
		Result struct {
			Processes []procRow `json:"processes"`
		} `json:"result"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &procs) != nil || len(procs.Result.Processes) > 2 {
		t.Fatalf("limit not applied: %s", stdout)
	}
}

// The job type decides how a job runs: a device query never reaches a shell,
// and a script with parameters gets them as environment variables.
func TestRunJobRoutesQueriesAndParameters(t *testing.T) {
	a := newDedupeAgent(t, filepath.Join(t.TempDir(), "state.json"))
	shellRuns := stubJobScript(t, nil)

	prevQuery := runDeviceQuery
	var gotQuery string
	runDeviceQuery = func(_ context.Context, script string) (int, string, string) {
		gotQuery = script
		return 0, `{"ok":true}`, ""
	}
	t.Cleanup(func() { runDeviceQuery = prevQuery })

	prevEnv := runJobScriptEnv
	var gotEnv []string
	runJobScriptEnv = func(_ context.Context, shell, script string, env []string) (int, string, string) {
		gotEnv = env
		return 0, "ran", ""
	}
	t.Cleanup(func() { runJobScriptEnv = prevEnv })

	a.handleFrame(frameOf(t, protocol.MsgJob, protocol.JobMsg{JobID: "q1", Type: protocol.JobTypeDeviceQuery, Script: `{"query":"disk"}`}))
	if res := queuedResult(t, a); !res.Success || res.Stdout != `{"ok":true}` || gotQuery != `{"query":"disk"}` {
		t.Fatalf("query result = %+v (script %q)", res, gotQuery)
	}
	a.handleFrame(frameOf(t, protocol.MsgJob, protocol.JobMsg{JobID: "p1", Type: "run_script", Shell: "powershell",
		Script: "Restart-Service $env:OPENRMM_PARAM_SERVICE", Params: map[string]string{"service": "Spooler"}}))
	if res := queuedResult(t, a); !res.Success || strings.Join(gotEnv, ",") != "OPENRMM_PARAM_SERVICE=Spooler" {
		t.Fatalf("param result = %+v env=%v", res, gotEnv)
	}
	a.handleFrame(frameOf(t, protocol.MsgJob, protocol.JobMsg{JobID: "p2", Type: "run_script", Shell: "cmd",
		Script: "echo %OPENRMM_PARAM_X%", Params: map[string]string{"x": "a"}}))
	if res := queuedResult(t, a); res.Success || !strings.Contains(res.Stderr, "cmd scripts cannot take parameters") {
		t.Fatalf("cmd with params = %+v", res)
	}
	if shellRuns.Load() != 0 {
		t.Fatal("a query or a parameterised script went through the plain shell runner")
	}
}
