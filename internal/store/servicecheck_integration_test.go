package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// PostgreSQL runs in CI. The same policy spans three OS families so dispatch,
// result authorization, and UI coverage cannot disagree about applicability.
func TestPlatformServiceCheckApplicabilityIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "service platforms")
	ctx := context.Background()
	agents := []*models.Agent{f.agent}
	for _, goos := range []string{"darwin", "windows"} {
		a, err := f.store.CreateAgent(ctx, goos+"-service", goos, "amd64", "0.10.0", "hash", &f.site.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, a)
	}
	policy, err := f.store.CreatePolicy(ctx, "Mixed fleet services", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AssignPolicy(ctx, policy.ID, models.ScopeSite, f.site.ID); err != nil {
		t.Fatal(err)
	}
	checks := []*models.Check{}
	for _, kind := range []string{models.CheckLinuxService, models.CheckMacService, models.CheckPing} {
		raw := `{"service":"worker"}`
		if kind == models.CheckPing {
			raw = `{"host":"localhost"}`
		}
		c := &models.Check{PolicyID: &policy.ID, Type: kind, Name: kind, Config: json.RawMessage(raw), IntervalSec: 300, TimeoutSec: 30, FailureCount: 2, RecoveryCount: 2, Enabled: true}
		if err := models.ValidateCheck(c); err != nil {
			t.Fatal(err)
		}
		c, err = f.store.CreateCheck(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		checks = append(checks, c)
	}
	for _, c := range checks {
		instances, err := f.store.CheckInstances(ctx, c.ID, "", false)
		if err != nil {
			t.Fatal(err)
		}
		expectedCount := 0
		for _, a := range agents {
			want := models.ServiceCheckMatchesOS(c.Type, a.OS)
			if want {
				expectedCount++
			}
			applies, err := f.store.CheckAppliesToAgent(ctx, c.ID, a.ID)
			if err != nil || applies != want {
				t.Fatalf("inbound %s/%s=%v, want %v: %v", c.Type, a.OS, applies, want, err)
			}
			effective, err := f.store.ChecksForAgent(ctx, a.ID, &f.site.ID, &f.client.ID)
			if err != nil {
				t.Fatal(err)
			}
			present := false
			for _, row := range effective {
				present = present || row.ID == c.ID
			}
			if present != want {
				t.Fatalf("dispatch %s/%s=%v", c.Type, a.OS, present)
			}
		}
		if len(instances) != expectedCount {
			t.Fatalf("coverage for %s=%d, want %d", c.Type, len(instances), expectedCount)
		}
	}
	// Control: an unassigned device never acquires the check through OS alone.
	outside, err := f.store.CreateAgent(ctx, "outside-linux", "linux", "amd64", "0.10.0", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if applies, err := f.store.CheckAppliesToAgent(ctx, checks[0].ID, outside.ID); err != nil || applies {
		t.Fatalf("unassigned check accepted: %v %v", applies, err)
	}
	// Scoped coverage cannot reveal the other platform's device.
	u, err := f.store.CreateUserWithScopes(ctx, "service-scoped", "hash", models.RoleTech, []models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: agents[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if instances, err := f.store.CheckInstances(ctx, checks[1].ID, u.ID, true); err != nil || len(instances) != 0 {
		t.Fatalf("scoped coverage=%v %v", instances, err)
	}
	// An old incident becomes inapplicable when its definition changes platform.
	obs := observationForFixture(f, models.SeverityError, time.Now())
	obs.SourceID = &checks[0].ID
	opened, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	checks[0].Type = models.CheckMacService
	checks[0].Config = json.RawMessage(`{"service":"system/worker","state":"running"}`)
	if err := f.store.UpdateCheck(ctx, checks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResolveInapplicableCheckAlerts(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.GetAlert(ctx, opened.Alert.ID)
	if err != nil || got.Status != models.AlertResolved {
		t.Fatalf("inapplicable incident=%+v %v", got, err)
	}
}
