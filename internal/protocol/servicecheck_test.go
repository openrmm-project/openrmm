package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeServiceCheck(t *testing.T) {
	for _, tt := range []struct{ kind, raw, service, state string }{
		{CheckLinuxService, `{"service":" sshd "}`, "sshd.service", ""},
		{CheckLinuxService, `{"service":"worker@blue.service"}`, "worker@blue.service", ""},
		{CheckMacService, `{"service":"com.example.worker"}`, "system/com.example.worker", "running"},
		{CheckMacService, `{"service":"system/com.example.worker","state":"loaded"}`, "system/com.example.worker", "loaded"},
		{CheckMacService, `{"service":"gui/501/com.example.worker"}`, "gui/501/com.example.worker", "running"},
		{CheckMacService, `{"service":"user/0/com.example.worker"}`, "user/0/com.example.worker", "running"},
	} {
		t.Run(tt.service, func(t *testing.T) {
			got, err := NormalizeServiceCheck(tt.kind, json.RawMessage(tt.raw))
			if err != nil || got.Service != tt.service || got.State != tt.state {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestServiceCheckRejectsUnsafeOrAmbiguousConfig(t *testing.T) {
	for _, kind := range []string{CheckLinuxService, CheckMacService} {
		for _, raw := range []string{``, `{}`, `null`, `[]`, `{"service":null}`, `{"service":12}`, `{"service":"safe","command":"bad"}`, `{"service":"safe"} {}`, `{"service":"safe","state":"stopped"}`, strings.Repeat(" ", 4097) + `{}`} {
			if _, err := NormalizeServiceCheck(kind, json.RawMessage(raw)); err == nil {
				t.Errorf("%s accepted invalid config %q", kind, raw[:min(len(raw), 100)])
			}
		}
		for _, name := range []string{"", "--all", "-bad", "a b", "a\nb", "a\x00b", "a;id", "$(id)", "a*", "a?", "[abc]", "../service", "/etc/worker", "a\\b", "a\"b", "a|b", strings.Repeat("a", 241)} {
			raw, _ := json.Marshal(ServiceCheckConfig{Service: name})
			if _, err := NormalizeServiceCheck(kind, raw); err == nil {
				t.Errorf("%s accepted unsafe service %q", kind, name)
			}
		}
	}
	for _, name := range []string{"test.socket", "test.timer", "test.target", "test.mount"} {
		raw, _ := json.Marshal(ServiceCheckConfig{Service: name})
		if _, err := NormalizeServiceCheck(CheckLinuxService, raw); err == nil {
			t.Errorf("non-service unit %q accepted", name)
		}
	}
	for _, name := range []string{"system/", "gui/x/test", "gui/12345678901/test", "user/-1/test", "pid/501/test", "system/a/b", "system/../test"} {
		raw, _ := json.Marshal(ServiceCheckConfig{Service: name})
		if _, err := NormalizeServiceCheck(CheckMacService, raw); err == nil {
			t.Errorf("unsafe launchd target %q accepted", name)
		}
	}
	if _, err := NormalizeServiceCheck(CheckLinuxService, []byte(`{"service":"sshd","state":"loaded"}`)); err == nil {
		t.Fatal("Linux accepted a macOS option")
	}
	if _, err := NormalizeServiceCheck("script", []byte(`{"service":"sshd"}`)); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
