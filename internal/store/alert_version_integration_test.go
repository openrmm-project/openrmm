package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// An alert's version guards what an operator decides on. Another occurrence of
// the same condition must not invalidate the version the console holds, or a
// still-firing alert (an offline device is observed every scheduler tick) could
// not be acknowledged, silenced or resolved; a real change still must.
func TestAlertVersionIgnoresRepeatOccurrencesIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "alert-version")
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)

	opened, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityWarning, t0))
	if err != nil {
		t.Fatal(err)
	}
	v := opened.Alert.Version
	ack, err := f.store.TransitionAlert(ctx, opened.Alert.ID, AlertAction{Action: "ack", Actor: "tech", ExpectedVersion: &v})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	held := ack.Version

	// Two more occurrences at the same severity, with a different value and
	// output: nothing the operator decided on moved.
	for i := 1; i <= 2; i++ {
		obs := observationForFixture(f, models.SeverityWarning, t0.Add(time.Duration(i)*time.Minute))
		value := 97.0 + float64(i)
		obs.ObservedValue, obs.Output = &value, "new output"
		got, err := f.store.ObserveAlert(ctx, obs)
		if err != nil {
			t.Fatal(err)
		}
		if got.Alert.Version != held || got.Alert.Status != models.AlertAcked || got.Alert.OccurrenceCount != int64(1+i) {
			t.Fatalf("occurrence %d: version %d (held %d), status %s, count %d", i, got.Alert.Version, held, got.Alert.Status, got.Alert.OccurrenceCount)
		}
	}
	// The acknowledged alert can still be silenced with the version the console
	// read before those occurrences.
	until := time.Now().Add(time.Hour)
	silenced, err := f.store.TransitionAlert(ctx, opened.Alert.ID, AlertAction{Action: "suppress", Actor: "tech", Note: "known", ExpectedVersion: &held, SilenceUntil: &until})
	if err != nil {
		t.Fatalf("silencing an acknowledged, still-firing alert with the version the console held: %v", err)
	}
	if silenced.Status != models.AlertSuppressed {
		t.Fatalf("status after silence = %s", silenced.Status)
	}

	// Control: an escalation is a real change. It bumps the version, and an
	// action on the stale one is refused.
	esc, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityError, t0.Add(5*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if esc.Alert.Version <= silenced.Version {
		t.Fatalf("an escalation kept version %d", esc.Alert.Version)
	}
	stale := silenced.Version
	if _, err := f.store.TransitionAlert(ctx, opened.Alert.ID, AlertAction{Action: "resolve", Actor: "tech", ExpectedVersion: &stale}); !errors.Is(err, ErrConflict) {
		t.Fatalf("resolve with a version from before an escalation: %v, want a conflict", err)
	}
	fresh := esc.Alert.Version
	if got, err := f.store.TransitionAlert(ctx, opened.Alert.ID, AlertAction{Action: "resolve", Actor: "tech", Note: "fixed", ExpectedVersion: &fresh}); err != nil || got.Status != models.AlertResolved {
		t.Fatalf("resolve with the current version: %v %+v", err, got)
	}
}
