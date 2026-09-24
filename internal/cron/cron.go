// Package cron parses standard 5-field cron expressions (plus @every/@daily style
// shortcuts) and computes next fire times. No dependencies.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	min, hour, dom, month, dow uint64
	domStar, dowStar           bool
	every                      time.Duration // @every d
}

var names = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func parseField(f string, lo, hi int) (mask uint64, star bool, err error) {
	if f == "*" || f == "?" {
		for i := lo; i <= hi; i++ {
			mask |= 1 << uint(i)
		}
		return mask, true, nil
	}
	for _, part := range strings.Split(f, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, e := strconv.Atoi(s)
			if e != nil || n <= 0 {
				return 0, false, fmt.Errorf("bad step %q", s)
			}
			step, part = n, base
		}
		a, b := lo, hi
		if part != "*" && part != "" {
			r1, r2, isRange := strings.Cut(part, "-")
			v1, e := val(r1)
			if e != nil {
				return 0, false, e
			}
			a, b = v1, v1
			if isRange {
				if b, e = val(r2); e != nil {
					return 0, false, e
				}
			} else if step > 1 { // "5/15" means 5-max step 15
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false, fmt.Errorf("value out of range [%d-%d]: %q", lo, hi, part)
		}
		for i := a; i <= b; i += step {
			mask |= 1 << uint(i)
		}
	}
	return mask, false, nil
}

func val(s string) (int, error) {
	if n, ok := names[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return n, nil
}

// Parse parses "m h dom mon dow", "@hourly|@daily|@weekly|@monthly|@yearly", or "@every 90m".
func Parse(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(expr, "@every "):
		d, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expr, "@every ")))
		if err != nil || d < time.Minute {
			return nil, fmt.Errorf("@every needs a duration of at least 1m")
		}
		return &Schedule{every: d}, nil
	case expr == "@hourly":
		expr = "0 * * * *"
	case expr == "@daily" || expr == "@midnight":
		expr = "0 0 * * *"
	case expr == "@weekly":
		expr = "0 0 * * 0"
	case expr == "@monthly":
		expr = "0 0 1 * *"
	case expr == "@yearly" || expr == "@annually":
		expr = "0 0 1 1 *"
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron expression needs 5 fields (min hour day month weekday), got %d", len(f))
	}
	var s Schedule
	var err error
	if s.min, _, err = parseField(f[0], 0, 59); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if s.hour, _, err = parseField(f[1], 0, 23); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if s.dom, s.domStar, err = parseField(f[2], 1, 31); err != nil {
		return nil, fmt.Errorf("day: %w", err)
	}
	if s.month, _, err = parseField(f[3], 1, 12); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if s.dow, s.dowStar, err = parseField(strings.ReplaceAll(f[4], "7", "0"), 0, 6); err != nil {
		return nil, fmt.Errorf("weekday: %w", err)
	}
	return &s, nil
}

func (s *Schedule) dayOK(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow // standard cron: either matches when both are restricted
}

// Next returns the first fire time strictly after t (in t's location).
func (s *Schedule) Next(t time.Time) time.Time {
	if s.every > 0 {
		return t.Add(s.every)
	}
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !s.dayOK(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case s.min&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
