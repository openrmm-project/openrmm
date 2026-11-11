package scriptvars

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

func TestUserNames(t *testing.T) {
	for _, name := range []string{"HUNTRESS_ACCOUNT_KEY", "key", "my_ApiKey", "_privateKey", "key_" + strings.Repeat("Ab", 4096)} {
		if err := ValidUserName(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"", "rmm_agent_id", "RMM_AGENT_ID", "OpenRmm_PARAM_X", "Path", "PATH", "1KEY", "has space", "bad-name", "a=b", "a\x00b"} {
		if err := ValidUserName(name); err == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	names, err := Normalize([]string{"myKey", "AnotherKey", "myKey"})
	if err != nil || strings.Join(names, ",") != "AnotherKey,myKey" {
		t.Fatalf("names=%v err=%v", names, err)
	}
	if _, err := Normalize([]string{"myKey", "MYKEY"}); err == nil {
		t.Fatal("ambiguous cross-platform names accepted")
	}
}

func TestSecretValueBounds(t *testing.T) {
	for _, secret := range []bool{false, true} {
		for _, value := range []string{"x", "short", strings.Repeat("v", 65536)} {
			if err := ValidValue(secret, value); err != nil {
				t.Fatal(err)
			}
		}
		for _, value := range []string{"", "bad\nvalue", "bad\rvalue", "bad\x00value"} {
			if err := ValidValue(secret, value); err == nil {
				t.Fatal("invalid environment value accepted")
			}
		}
	}
}

func TestShortSecretsAreRedactedWithoutOutputGrowth(t *testing.T) {
	for _, secret := range []string{"x", "abc", "secret7"} {
		input := "value=" + secret + " end"
		got := Redact(input, []string{secret})
		if strings.Contains(got, secret) || len(got) > len(input) {
			t.Fatal("short secret leaked or expanded output")
		}
	}
	input := strings.Repeat("*x", 8192)
	got := Redact(input, []string{"*", "x", "a-longer-secret"})
	twice := Redact(got, []string{"*", "x", "a-longer-secret"})
	if len(got) > len(input) || len(twice) > len(got) || strings.Contains(got, "x") {
		t.Fatal("repeated redaction expands output")
	}
	if Redact("plain output", []string{""}) != "plain output" {
		t.Fatal("empty snapshot entry rewrote output")
	}
	if Redact("public output", []string{"private"}) != "public output" {
		t.Fatal("non-secret control changed")
	}
}

func TestEnvironmentPayloadBudget(t *testing.T) {
	if err := ValidEnvironmentSize(map[string]string{"key": strings.Repeat("x", 65536)}); err != nil {
		t.Fatal(err)
	}
	if err := ValidEnvironmentSize(map[string]string{"a": strings.Repeat("x", MaxEnvironmentBytes)}); err == nil {
		t.Fatal("aggregate payload budget removed")
	}
}

func TestRedactRemovesTheLongerSecretFirst(t *testing.T) {
	got := Redact("key=abcdefghij extra=abcdefghijXX", []string{"abcdefghij", "abcdefghijXX"})
	if got != "key=******** extra=********" {
		t.Fatalf("redacted = %q", got)
	}
}

func TestSealRoundTrip(t *testing.T) {
	cfg := &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}
	blob, err := Seal(cfg, []string{"huntress-account-key"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blob, "huntress") {
		t.Fatalf("ciphertext contains the secret: %s", blob)
	}
	back, err := Open(cfg, blob)
	if err != nil || len(back) != 1 || back[0] != "huntress-account-key" {
		t.Fatalf("open = %v, %v", back, err)
	}
}

func TestEncryptedRedactionsRejectPlaintextAndCorruption(t *testing.T) {
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-key"}
	for _, blob := range []string{`["synthetic-secret"]`, "enc:v1:bad"} {
		if _, err := Open(cfg, blob); err == nil {
			t.Fatal("unsealed redactions accepted")
		}
	}
	blob, err := Seal(cfg, []string{"synthetic-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(&config.Config{SettingsEncryptionKey: "wrong-key"}, blob); err == nil {
		t.Fatal("wrong key accepted")
	}
}

// Names an interpreter or loader reads would let whoever sets a value decide
// what runs (a .NET profiler in PowerShell, a module path, the dynamic linker),
// or move where a script looks for installers. They are reserved whatever the
// case; ordinary names that merely look alike stay available.
func TestNamesThatControlProgramLoadingAreReserved(t *testing.T) {
	for _, name := range []string{
		"PSModulePath", "psmodulepath", "PSExecutionPolicyPreference", "COR_PROFILER", "COR_ENABLE_PROFILING",
		"cor_profiler_path", "CORECLR_PROFILER", "ComPlus_ETWEnabled", "DOTNET_STARTUP_HOOKS", "APPDOMAIN_MANAGER_ASM",
		"LD_AUDIT", "DYLD_LIBRARY_PATH", "PYTHONWARNINGS", "PythonHome", "PERL5LIB", "PERLLIB", "RUBYOPT",
		"NODE_EXTRA_CA_CERTS", "GLIBC_TUNABLES", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_CONFIG_GLOBAL",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "PS4", "SHELLOPTS", "TMPDIR", "ProgramFiles", "APPDATA", "LocalAppData", "USERPROFILE",
	} {
		if err := ValidUserName(name); err == nil {
			t.Errorf("reserved name accepted: %s", name)
		}
	}
	for _, name := range []string{"HUNTRESS_ACCOUNT_KEY", "GIT_TOKEN", "GITHUB_TOKEN", "CORP_ID", "LDAP_PASSWORD", "NODEPING_KEY", "PSA_TOKEN", "customerKey"} {
		if err := ValidUserName(name); err != nil {
			t.Errorf("ordinary name refused: %s: %v", name, err)
		}
	}
}
