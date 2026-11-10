package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// Real authenticated router/store, with CI-only PostgreSQL and no listener.
func TestPlatformServiceChecksCreateEditE2E(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	a, err := f.store.CreateAgent(ctx, "service-linux", "linux", "amd64", "0.10.0", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{models.CheckLinuxService, models.CheckMacService} {
		body := `{"agent_id":"` + a.ID + `","type":"` + kind + `","name":"Service","config":{"service":"worker"}}`
		result := f.do(http.MethodPost, "/api/checks", body)
		if result.Code != http.StatusCreated {
			t.Fatalf("create %s=%d %s", kind, result.Code, result.Body.String())
		}
		var c models.Check
		if err := json.Unmarshal(result.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		var cfg map[string]string
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		want := "worker.service"
		if kind == models.CheckMacService {
			want = "system/worker"
		}
		if cfg["service"] != want {
			t.Fatalf("not normalized: %+v", cfg)
		}
		if kind == models.CheckMacService {
			update := f.do(http.MethodPut, "/api/checks/"+c.ID, `{"type":"macservice","name":"On demand","config":{"service":"gui/501/worker","state":"loaded"}}`)
			if update.Code != http.StatusOK {
				t.Fatalf("edit=%d %s", update.Code, update.Body.String())
			}
			if err := json.Unmarshal(update.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(c.Config, &cfg); err != nil || cfg["state"] != "loaded" {
				t.Fatalf("lost on-demand setting: %+v %v", cfg, err)
			}
		}
		bad := f.do(http.MethodPost, "/api/checks", `{"agent_id":"`+a.ID+`","type":"`+kind+`","name":"Bad","config":{"service":"worker;id"}}`)
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("unsafe config accepted: %d", bad.Code)
		}
	}
}
