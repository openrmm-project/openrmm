package models

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	// The server image carries no zoneinfo, and a schedule must resolve its
	// IANA zone wherever the server runs. The agent does not import this
	// package, so only the server pays for the database.
	_ "time/tzdata"
)

// DefaultTimezone is the zone of a schedule that names none: the one every
// task and patch window used before schedules carried a zone of their own.
const DefaultTimezone = "UTC"

// timezonePattern admits IANA zone names ("UTC", "America/New_York",
// "America/Argentina/Buenos_Aires", "Etc/GMT+5"), nothing that reads as a
// path.
var timezonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// NormalizeTimezone checks the zone a schedule's times of day are in. ""
// stays "": a new schedule then gets DefaultTimezone, and an edit keeps the
// zone it had. "Local" is refused: it is whatever zone the server process
// happens to run in, which is what schedules must not depend on.
func NormalizeTimezone(tz string) (string, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return "", nil
	}
	if len(tz) > 64 || strings.EqualFold(tz, "Local") || !timezonePattern.MatchString(tz) {
		return "", fmt.Errorf("timezone must be an IANA time zone name, such as America/New_York or UTC")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("unknown timezone %q: use an IANA time zone name, such as America/New_York or UTC", tz)
	}
	return tz, nil
}

var scheduleLocations sync.Map // zone name -> *time.Location

// ScheduleLocation is the location a stored schedule's times of day are read
// in. An empty or unknown zone reads as UTC, so one bad row cannot stop the
// scheduler.
func ScheduleLocation(tz string) *time.Location {
	if tz == "" || tz == DefaultTimezone {
		return time.UTC
	}
	if loc, ok := scheduleLocations.Load(tz); ok {
		return loc.(*time.Location)
	}
	if _, err := NormalizeTimezone(tz); err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	scheduleLocations.Store(tz, loc)
	return loc
}
