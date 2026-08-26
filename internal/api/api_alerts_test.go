package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

func apiFloat64Ptr(v float64) *float64 { return &v }

func validCheckForTest(checkType string) models.Check {
	return models.Check{
		Type: checkType, Name: "health", Config: json.RawMessage(`{}`),
		IntervalSec: 30, FailureCount: 2, RecoveryCount: 2,
		Hysteresis: 2, CooldownSec: 300, TimeoutSec: 30, Enabled: true,
	}
}

func TestValidateCheckRejectsDefinitionsThatCannotBeEvaluatedSafely(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*models.Check)
		problem string
	}{
		{"unsupported type", func(c *models.Check) { c.Type = "future" }, "unsupported"},
		{"blank name", func(c *models.Check) { c.Name = " " }, "name is required"},
		{"short interval", func(c *models.Check) { c.IntervalSec = 29 }, "interval_sec"},
		{"short timeout", func(c *models.Check) { c.TimeoutSec = 4 }, "timeout_sec"},
		{"zero failure count", func(c *models.Check) { c.FailureCount = 0 }, "failure_count"},
		{"negative hysteresis", func(c *models.Check) { c.Hysteresis = -1 }, "hysteresis"},
		{"invalid JSON", func(c *models.Check) { c.Config = json.RawMessage(`[]`) }, "JSON object"},
		{"disk thresholds reversed", func(c *models.Check) {
			c.Type, c.WarnThreshold, c.ErrThreshold = models.CheckDiskSpace, apiFloat64Ptr(10), apiFloat64Ptr(20)
		}, "low-water"},
		{"cpu thresholds reversed", func(c *models.Check) {
			c.Type, c.WarnThreshold, c.ErrThreshold = models.CheckCPULoad, apiFloat64Ptr(90), apiFloat64Ptr(80)
		}, "CPU"},
		{"ping without host", func(c *models.Check) {
			c.Type, c.Config = models.CheckPing, json.RawMessage(`{"host":" "}`)
		}, "config.host"},
		{"service without name", func(c *models.Check) {
			c.Type, c.Config = models.CheckWinService, json.RawMessage(`{}`)
		}, "config.service"},
		{"script unsupported shell", func(c *models.Check) {
			c.Type, c.Config = models.CheckScript, json.RawMessage(`{"shell":"cmd.exe","script":"exit 0"}`)
		}, "shell"},
		{"script missing body", func(c *models.Check) {
			c.Type, c.Config = models.CheckScript, json.RawMessage(`{"shell":"bash","script":" "}`)
		}, "script"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := validCheckForTest(models.CheckPing)
			check.Config = json.RawMessage(`{"host":"localhost"}`)
			tt.mutate(&check)
			err := models.ValidateCheck(&check)
			if err == nil || !strings.Contains(err.Error(), tt.problem) {
				t.Fatalf("models.ValidateCheck() error = %v, want problem containing %q", err, tt.problem)
			}
		})
	}
}

func TestValidateCheckAcceptsSupportedDefinitions(t *testing.T) {
	tests := []models.Check{
		func() models.Check {
			c := validCheckForTest(models.CheckDiskSpace)
			c.WarnThreshold, c.ErrThreshold = apiFloat64Ptr(20), apiFloat64Ptr(10)
			return c
		}(),
		func() models.Check {
			c := validCheckForTest(models.CheckMemory)
			c.WarnThreshold, c.ErrThreshold = apiFloat64Ptr(25), apiFloat64Ptr(15)
			return c
		}(),
		func() models.Check {
			c := validCheckForTest(models.CheckCPULoad)
			c.WarnThreshold, c.ErrThreshold = apiFloat64Ptr(80), apiFloat64Ptr(90)
			return c
		}(),
		func() models.Check {
			c := validCheckForTest(models.CheckPing)
			c.Config = json.RawMessage(`{"host":"localhost"}`)
			return c
		}(),
		func() models.Check {
			c := validCheckForTest(models.CheckWinService)
			c.Config = json.RawMessage(`{"service":"Spooler"}`)
			return c
		}(),
		func() models.Check {
			c := validCheckForTest(models.CheckScript)
			c.Config = json.RawMessage(`{"shell":"bash","script":"exit 0"}`)
			return c
		}(),
	}
	for i := range tests {
		if err := models.ValidateCheck(&tests[i]); err != nil {
			t.Errorf("case %d rejected: %v", i, err)
		}
	}
}

func TestAlertCursorRoundTripAndValidation(t *testing.T) {
	want := models.Alert{ID: uuid.NewString(), CreatedAt: time.Now().UTC().Round(0)}
	at, id, err := parseAlertCursor(alertCursor(&want))
	if err != nil {
		t.Fatal(err)
	}
	if id != want.ID || at == nil || !at.Equal(want.CreatedAt) {
		t.Fatalf("cursor decoded to at=%v id=%q, want at=%v id=%q", at, id, want.CreatedAt, want.ID)
	}
	for _, raw := range []string{"%%%", "bm8tc2VwYXJhdG9y", "MjAyNi0wOC0xNFQwMDowMDowMFp8bm90LXV1aWQ"} {
		if _, _, err := parseAlertCursor(raw); err == nil {
			t.Errorf("cursor %q should be rejected", raw)
		}
	}
}

func TestListAlertsRejectsInvalidFiltersBeforeDatabaseAccess(t *testing.T) {
	tests := []string{
		"agent_id=not-a-uuid",
		"site_id=not-a-uuid",
		"client_id=not-a-uuid",
		"group_id=not-a-uuid",
		"limit=0",
		"status=not-a-status",
		"q=" + strings.Repeat("x", 201),
	}
	for _, query := range tests {
		t.Run(query[:min(len(query), 30)], func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/alerts?"+query, nil)
			res := httptest.NewRecorder()
			new(Server).listAlerts(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestDecodeAlertActionValidatesOptimisticVersion(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"expected_version":0}`))
	if _, err := decodeAlertAction(req); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("error = %v, want positive-version validation", err)
	}
}
