package workflows

// Minimal 5-field cron ("minute hour dom mon dow") in UTC, the same syntax
// GitHub Actions accepts on schedule triggers: *, lists, ranges and steps.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronField [64]bool // index 0-63 covers every field range used below

type cronSpec struct {
	min, hour, dom, mon, dow cronField
	domStar, dowStar         bool
}

var (
	cronMonths = map[string]int{
		"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
		"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
	}
	cronDOWs = map[string]int{
		"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
	}
)

func parseCronAtom(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	return strconv.Atoi(s)
}

// parseCronField parses one field into a set of matching values.
func parseCronField(f string, lo, hi, shift int, names map[string]int) (cronField, bool, error) {
	var set cronField
	star := strings.TrimSpace(f) == "*"
	for _, part := range strings.Split(f, ",") {
		step := 1
		hasStep := false
		if i := strings.IndexByte(part, '/'); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return set, false, fmt.Errorf("invalid step %q", part[i+1:])
			}
			step, hasStep = s, true
			part = part[:i]
		}
		var lo2, hi2 int
		switch {
		case part == "*" || part == "":
			lo2, hi2 = lo, hi
		default:
			if i := strings.IndexByte(part, '-'); i >= 0 {
				a, err1 := parseCronAtom(part[:i], names)
				b, err2 := parseCronAtom(part[i+1:], names)
				if err1 != nil || err2 != nil {
					return set, false, fmt.Errorf("invalid range %q", part)
				}
				lo2, hi2 = a, b
			} else {
				v, err := parseCronAtom(part, names)
				if err != nil {
					return set, false, fmt.Errorf("invalid value %q", part)
				}
				if hasStep {
					lo2, hi2 = v, hi // "30/5" means 30-59 stepped, per cron
				} else {
					lo2, hi2 = v, v
				}
			}
		}
		if lo2 < lo || hi2 > hi || lo2 > hi2 {
			return set, false, fmt.Errorf("value %q out of range %d-%d", part, lo, hi)
		}
		for v := lo2; v <= hi2; v += step {
			set[v+shift] = true
		}
	}
	return set, star, nil
}

// NextCron returns the first instant strictly after `after` (UTC) matching the
// expression. An empty schedule — e.g. Feb 31 — errors after a 4-year scan.
func NextCron(expr string, after time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron expects 5 fields, got %d in %q", len(fields), expr)
	}
	var s cronSpec
	var err error
	if s.min, _, err = parseCronField(fields[0], 0, 59, 0, nil); err != nil {
		return time.Time{}, err
	}
	if s.hour, _, err = parseCronField(fields[1], 0, 23, 0, nil); err != nil {
		return time.Time{}, err
	}
	if s.dom, s.domStar, err = parseCronField(fields[2], 1, 31, 0, nil); err != nil {
		return time.Time{}, err
	}
	if s.mon, _, err = parseCronField(fields[3], 1, 12, 0, cronMonths); err != nil {
		return time.Time{}, err
	}
	// dow 7 is Sunday per POSIX — fold bit 7 into bit 0 before clearing bit 7.
	if s.dow, s.dowStar, err = parseCronField(fields[4], 0, 7, 0, cronDOWs); err != nil {
		return time.Time{}, err
	}
	if s.dow[7] {
		s.dow[0] = true
		s.dow[7] = false
	}

	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(4, 0, 0)
	for t.Before(limit) {
		if !s.mon[t.Month()] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if !s.min[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cron %q has no matching instant within 4 years", expr)
}

// matchDay applies the POSIX rule: when both dom and dow are restricted, a day
// matches if either does; restricted-only fields must match on their own.
func (s *cronSpec) matchDay(t time.Time) bool {
	domOK := s.dom[t.Day()]
	dowOK := s.dow[int(t.Weekday())]
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}
