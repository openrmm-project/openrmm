package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/devicelimit"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

func deviceLimitServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return &Server{cfg: &config.Config{}, store: newTestStore(t, url)}, ctx
}

func limitInfo(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.deviceLimitInfo(rec, httptest.NewRequest(http.MethodGet, "/api/devices/limit", nil))
	var out map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("limit info: %d %s", rec.Code, rec.Body.String())
	}
	return out
}

// The enrolment handshake refuses a device past the plan's limit with a
// message the installer prints, and GET /api/devices/limit tells the console.
func TestEnrolmentStopsAtTheDeviceLimitIntegration(t *testing.T) {
	s, ctx := deviceLimitServer(t)
	tok, err := s.store.CreateEnrollToken(ctx, "api-devlimit", "limit", 10, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	enrol := func(host string) error {
		_, _, _, err := s.registerAgent(ctx, &protocol.RegisterReq{EnrollToken: tok.Token, Hostname: host, OS: "linux", Arch: "amd64", Version: "0.8.0"})
		return err
	}

	// No limit known: nothing is refused and the console says so.
	if err := enrol("pc-1"); err != nil {
		t.Fatal(err)
	}
	if info := limitInfo(t, s); info["limit"] != float64(0) || info["level"] != "none" || info["used"] != float64(1) {
		t.Fatalf("no limit: %v", info)
	}

	// A notice period: the limit is shown but not yet enforced.
	from := time.Now().Add(7 * 24 * time.Hour)
	s.deviceLimit.set(devicelimit.State{Limit: 2, EnforceFrom: from, UpgradeURL: "https://openrmm.io/portal"})
	if err := enrol("pc-2"); err != nil {
		t.Fatal(err)
	}
	if err := enrol("pc-3"); err != nil {
		t.Fatalf("during the notice period nothing is refused: %v", err)
	}
	info := limitInfo(t, s)
	if info["level"] != "limit" || info["enforced"] != false || info["enforce_from"] == nil || info["upgrade_url"] != "https://openrmm.io/portal" {
		t.Fatalf("notice period: %v", info)
	}

	// Enforced: the plan is over its limit, so the next enrolment is refused.
	s.deviceLimit.set(devicelimit.State{Limit: 3})
	err = enrol("pc-4")
	if err == nil || !strings.Contains(err.Error(), "device limit reached") || !strings.Contains(err.Error(), "3 devices") {
		t.Fatalf("refused with the installer's message, got %v", err)
	}
	// A stranger without a token learns nothing about the plan.
	if _, _, _, err := s.registerAgent(ctx, &protocol.RegisterReq{EnrollToken: "guess", Hostname: "x", OS: "linux", Arch: "amd64", Version: "0.8.0"}); err == nil ||
		strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "3 devices") {
		t.Fatalf("an invalid token must not reveal the limit, got %v", err)
	}
	if info := limitInfo(t, s); info["enforced"] != true || info["used"] != float64(3) {
		t.Fatalf("enforced: %v", info)
	}

	// The alert follows the count: an error at the limit, resolved below 80%.
	s.observeDeviceLimit(ctx, 3, devicelimit.State{Limit: 3})
	alerts, err := s.store.ListAlerts(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range alerts {
		if a.SourceType == models.AlertSourceSystem && a.Status != "resolved" && strings.Contains(a.Message, "the plan is full") {
			found = a.Severity == models.SeverityError
		}
	}
	if !found {
		t.Fatalf("an error alert is open at the limit: %+v", alerts)
	}
	s.observeDeviceLimit(ctx, 1, devicelimit.State{Limit: 3})
	alerts, _ = s.store.ListAlerts(ctx, "", 100)
	for _, a := range alerts {
		if a.SourceType == models.AlertSourceSystem && strings.Contains(a.Message, "devices in use") && a.Status != "resolved" {
			t.Fatalf("the alert is resolved below 80%%: %+v", a)
		}
	}
}
