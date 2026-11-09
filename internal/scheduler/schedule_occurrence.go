package scheduler

import "time"

// scheduledOccurrence resolves a wall-clock slot consistently across DST.
// time.Date alone can choose either occurrence of a repeated time, or map a
// missing time to before the spring jump. Use the first occurrence; a missing
// time waits for the first actual instant whose local clock has passed it.
func scheduledOccurrence(day time.Time, minute int, loc *time.Location) time.Time {
	if minute < 0 || minute > 1439 {
		return time.Time{}
	}
	y, month, d := day.In(loc).Date()
	wanted := time.Date(y, month, d, minute/60, minute%60, 0, 0, time.UTC)
	ordinary := time.Date(y, month, d, minute/60, minute%60, 0, 0, loc)
	var candidates [2]time.Time
	for i, shift := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		_, offset := ordinary.Add(shift).Zone()
		candidates[i] = time.Unix(wanted.Unix()-int64(offset), 0).In(loc)
	}
	var first time.Time
	for _, candidate := range candidates {
		if scheduleWallTime(candidate).Equal(wanted) && (first.IsZero() || candidate.Before(first)) {
			first = candidate
		}
	}
	if !first.IsZero() {
		return first
	}

	// A forward jump straddles the two instants computed with its old and new
	// offsets. Search that bounded interval in seconds, rather than scanning
	// every minute of the day for each task and each configured time slot.
	lo, hi := candidates[0].Unix(), candidates[1].Unix()
	if lo > hi {
		lo, hi = hi, lo
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		if scheduleWallTime(time.Unix(mid, 0).In(loc)).Before(wanted) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	resolved := time.Unix(lo, 0).In(loc)
	ry, rm, rd := resolved.Date()
	if ry != y || rm != month || rd != d || scheduleWallTime(resolved).Before(wanted) {
		return time.Time{}
	}
	return resolved
}

// scheduleWallTime compares calendar fields without applying a UTC offset.
func scheduleWallTime(t time.Time) time.Time {
	y, month, d := t.Date()
	return time.Date(y, month, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}
