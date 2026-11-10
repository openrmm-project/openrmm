package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/settings"
)

// CI-only PostgreSQL; real authenticated router, persistence and live managers.
// An invalid default must reject the whole request, including write-only fields.
func TestRemoteConfigurationProviderContractE2E(t *testing.T) {
	f := newPolicyAPIFixture(t)
	cfg := &config.Config{APIRateLimit: 10000, SettingsEncryptionKey: "synthetic-config-contract-test-key"}
	h := hub.New()
	chat := ai.NewEngine(ai.NewClient("", "", ""), f.store, nil, nil, cfg)
	manager := remote.NewManager(f.store, h, "", "", "", false, false)
	f.handler = New(cfg, f.store, h, nil, manager, remote.NewRelay(), chat, nil, nil).Router()
	get := func() map[string]any {
		t.Helper()
		r := f.do(http.MethodGet, "/api/config", "")
		if r.Code != http.StatusOK {
			t.Fatalf("GET config=%d", r.Code)
		}
		var out struct {
			Remote map[string]any `json:"remote"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if _, exposed := out.Remote["meshcentral_token_key"]; exposed {
			t.Fatal("GET config exposed a token field")
		}
		return out.Remote
	}
	providers := get()["providers"]
	if !reflect.DeepEqual(providers, []any{"native", "meshcentral"}) {
		t.Fatalf("unexpected GET provider contract: %v", providers)
	}
	for _, provider := range []string{"native", "meshcentral"} {
		body := `{"remote":{"default":"` + provider + `","meshcentral_url":"https://mesh.example.invalid","meshcentral_enabled":true,"rustdesk_enabled":true,"meshcentral_token_key":"synthetic-mesh-token"}}`
		r := f.do(http.MethodPut, "/api/config", body)
		if r.Code != http.StatusOK {
			t.Fatalf("valid default %s=%d", provider, r.Code)
		}
		got := get()
		if got["default"] != provider || got["rustdesk_enabled"] != true || got["meshcentral_token_set"] != true {
			t.Fatalf("valid configuration did not persist: provider=%s", provider)
		}
	}
	for _, provider := range []string{"terminal", "rustdesk"} {
		before := get()
		secretBefore, err := settings.Effective(context.Background(), f.store, cfg)
		if err != nil {
			t.Fatal(err)
		}
		r := f.do(http.MethodPut, "/api/config", `{"remote":{"default":"`+provider+`","meshcentral_url":"https://must-not-persist.example.invalid","meshcentral_token_key":"must-not-save-test-token"}}`)
		if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "invalid remote provider") {
			t.Fatalf("invalid provider %s=%d", provider, r.Code)
		}
		secretAfter, err := settings.Effective(context.Background(), f.store, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if secretBefore.MeshCentralTokenKey != secretAfter.MeshCentralTokenKey {
			t.Fatal("invalid default changed stored token")
		}
		if !reflect.DeepEqual(before, get()) {
			t.Fatal("invalid provider partially applied the request")
		}
	}
	secretBefore, err := settings.Effective(context.Background(), f.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A blank token preserves the existing secret and does not enable a new one.
	r := f.do(http.MethodPut, "/api/config", `{"remote":{"default":"native","rustdesk_enabled":true,"meshcentral_token_key":""}}`)
	if r.Code != http.StatusOK || get()["meshcentral_token_set"] != true {
		t.Fatal("blank token did not preserve existing value")
	}
	secretAfter, err := settings.Effective(context.Background(), f.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if secretBefore.MeshCentralTokenKey != secretAfter.MeshCentralTokenKey {
		t.Fatal("blank token changed stored secret")
	}
}
