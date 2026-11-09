package scheduler

import (
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// A daily task at 11:40 in America/New_York runs at 11:40 there: 16:40 UTC in
// winter, 15:40 UTC in summer. It used to run at 11:40 UTC, five hours early.
func TestDailyTaskRunsAtItsZonesTime(t *testing.T) {
	s := &Scheduler{}
	ny := &models.Task{ScheduleKind: models.ScheduleDaily, RunAtMinute: 11*60 + 40, Timezone: "America/New_York"}
	for _, c := range []struct {
		name string
		last time.Time
		now  time.Time
		want bool
	}{
		{"11:40 UTC is 06:40 in New York: not yet (the reported bug)", time.Time{}, utc(2026, 11, 9, 11, 40), false},
		{"a minute before 11:40 EST", time.Time{}, utc(2026, 11, 9, 16, 39), false},
		{"11:40 EST", time.Time{}, utc(2026, 11, 9, 16, 40), true},
		{"11:40 EDT in summer", time.Time{}, utc(2026, 7, 1, 15, 40), true},
		{"a minute before 11:40 EDT", time.Time{}, utc(2026, 7, 1, 15, 39), false},
		// 23:45 on the 9th in New York is already the 10th in UTC: the same
		// local day as the run at 11:40, so it does not run again.
		{"once per local day", utc(2026, 11, 9, 16, 40), utc(2026, 11, 10, 4, 45), false},
		{"the next local day", utc(2026, 11, 9, 16, 40), utc(2026, 11, 10, 16, 40), true},
	} {
		if got := s.taskDue(ny, c.last, c.now); got != c.want {
			t.Errorf("%s: due=%v, want %v", c.name, got, c.want)
		}
	}
	// A task with no zone is UTC, as every task was before zones existed.
	legacy := &models.Task{ScheduleKind: models.ScheduleDaily, RunAtMinute: 11*60 + 40}
	if !s.taskDue(legacy, time.Time{}, utc(2026, 11, 9, 11, 40)) || s.taskDue(legacy, time.Time{}, utc(2026, 11, 9, 11, 39)) {
		t.Error("a task without a zone no longer runs at its UTC time")
	}
}

// Daylight saving: a time the spring-forward day skips runs when that day's
// clock passes it, and a time the fall-back day repeats runs once.
func TestDailyTaskAcrossDaylightSaving(t *testing.T) {
	s := &Scheduler{}
	// 2027-03-14, New York: 02:00 EST jumps to 03:00 EDT. 02:30 never exists.
	skipped := &models.Task{ScheduleKind: models.ScheduleDaily, RunAtMinute: 2*60 + 30, Timezone: "America/New_York"}
	lastRun := utc(2027, 3, 13, 7, 30) // 02:30 EST the day before
	if s.taskDue(skipped, lastRun, utc(2027, 3, 14, 6, 59)) {
		t.Error("ran before the skipped time")
	}
	if !s.taskDue(skipped, lastRun, utc(2027, 3, 14, 7, 0)) {
		t.Error("did not run at 03:00 EDT on the day 02:30 does not exist")
	}
	if s.taskDue(skipped, utc(2027, 3, 14, 7, 0), utc(2027, 3, 14, 7, 30)) {
		t.Error("ran twice on the spring-forward day")
	}
	// 2026-11-01, New York: 02:00 EDT falls back to 01:00 EST. 01:30 happens twice.
	repeated := &models.Task{ScheduleKind: models.ScheduleDaily, RunAtMinute: 60 + 30, Timezone: "America/New_York"}
	if !s.taskDue(repeated, utc(2026, 10, 31, 5, 30), utc(2026, 11, 1, 5, 30)) {
		t.Error("did not run at the first 01:30")
	}
	if s.taskDue(repeated, utc(2026, 11, 1, 5, 30), utc(2026, 11, 1, 6, 30)) {
		t.Error("ran again at the second 01:30")
	}
}

// Weekly tasks read the weekday in their zone too: Sunday 22:00 in New York
// is Monday 03:00 UTC.
func TestWeeklyTaskUsesItsZonesWeekday(t *testing.T) {
	s := &Scheduler{}
	sunday := &models.Task{ScheduleKind: models.ScheduleWeekly, DaysOfWeek: 1 << uint(time.Sunday), RunAtMinutes: []int{22 * 60}, Timezone: "America/New_York"}
	lastWeek := utc(2026, 11, 2, 3, 0)
	if s.taskDue(sunday, lastWeek, utc(2026, 11, 9, 2, 59)) {
		t.Error("ran before Sunday 22:00 in New York")
	}
	if !s.taskDue(sunday, lastWeek, utc(2026, 11, 9, 3, 0)) {
		t.Error("did not run at Sunday 22:00 in New York (Monday 03:00 UTC)")
	}
	if s.taskDue(sunday, lastWeek, utc(2026, 11, 8, 22, 0)) {
		t.Error("ran at Sunday 22:00 UTC, which is 17:00 in New York")
	}
	// São Paulo has no daylight saving: Monday 09:00 there is 12:00 UTC.
	monday := &models.Task{ScheduleKind: models.ScheduleWeekly, DaysOfWeek: 1 << uint(time.Monday), RunAtMinutes: []int{9 * 60}, Timezone: "America/Sao_Paulo"}
	if s.taskDue(monday, utc(2026, 11, 2, 12, 0), utc(2026, 11, 9, 11, 59)) || !s.taskDue(monday, utc(2026, 11, 2, 12, 0), utc(2026, 11, 9, 12, 0)) {
		t.Error("Monday 09:00 in São Paulo is not 12:00 UTC")
	}
}

// A patch window is open by its policy's clock, not the server's.
func TestPatchWindowUsesItsPolicysZone(t *testing.T) {
	business := models.PatchPolicy{WindowStartMin: 9 * 60, WindowEndMin: 17 * 60, Reboot: models.RebootAlways, Timezone: "America/New_York"}
	if open, _ := windowState([]models.PatchPolicy{business}, utc(2026, 7, 1, 10, 0)); open {
		t.Error("open at 10:00 UTC, which is 06:00 in New York")
	}
	if open, reb := windowState([]models.PatchPolicy{business}, utc(2026, 7, 1, 14, 0)); !open || !reb {
		t.Error("closed at 14:00 UTC, which is 10:00 in New York")
	}
	// Tuesday 22:00 in New York is Wednesday 02:00 UTC.
	tuesdayNight := models.PatchPolicy{WindowDays: []int{int(time.Tuesday)}, WindowStartMin: 20 * 60, WindowEndMin: 23 * 60, Timezone: "America/New_York"}
	if open, _ := windowState([]models.PatchPolicy{tuesdayNight}, utc(2026, 7, 1, 2, 0)); !open {
		t.Error("a Tuesday-night window in New York is closed on Tuesday night there")
	}
	// No zone is UTC, as before.
	legacy := models.PatchPolicy{WindowStartMin: 9 * 60, WindowEndMin: 17 * 60}
	if open, _ := windowState([]models.PatchPolicy{legacy}, utc(2026, 7, 1, 10, 0)); !open {
		t.Error("a policy without a zone is no longer read in UTC")
	}
}
