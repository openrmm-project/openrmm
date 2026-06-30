package scheduler

import (
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestWindowState(t *testing.T) {
	// A fixed Wednesday at 10:00.
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	if now.Weekday() != time.Wednesday {
		t.Fatalf("fixture weekday changed: %v", now.Weekday())
	}
	anyDayNoReboot := models.PatchPolicy{WindowStartMin: 0, WindowEndMin: 1439, Reboot: models.RebootNever}
	wedWithReboot := models.PatchPolicy{WindowDays: []int{int(time.Wednesday)}, WindowStartMin: 540, WindowEndMin: 660, Reboot: models.RebootAlways}
	mondayOnly := models.PatchPolicy{WindowDays: []int{int(time.Monday)}, WindowStartMin: 0, WindowEndMin: 1439, Reboot: models.RebootIfRequired}
	earlyWindow := models.PatchPolicy{WindowStartMin: 0, WindowEndMin: 300, Reboot: models.RebootAlways} // 00:00–05:00

	tests := []struct {
		name              string
		pols              []models.PatchPolicy
		wantOpen, wantReb bool
	}{
		{"any day, no reboot", []models.PatchPolicy{anyDayNoReboot}, true, false},
		{"matching day + reboot", []models.PatchPolicy{wedWithReboot}, true, true},
		{"wrong day", []models.PatchPolicy{mondayOnly}, false, false},
		{"outside hours", []models.PatchPolicy{earlyWindow}, false, false},
		{"one open grants reboot", []models.PatchPolicy{mondayOnly, wedWithReboot}, true, true},
		{"none", nil, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			open, reb := windowState(tc.pols, now)
			if open != tc.wantOpen || reb != tc.wantReb {
				t.Fatalf("windowState = (%v,%v), want (%v,%v)", open, reb, tc.wantOpen, tc.wantReb)
			}
		})
	}
}
