package cron

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return v }
	cases := []struct{ expr, from, want string }{
		{"*/15 * * * *", "2026-09-21 10:07", "2026-09-21 10:15"},
		{"0 9 * * *", "2026-09-21 09:00", "2026-09-22 09:00"},
		{"30 3 * * *", "2026-09-21 12:00", "2026-09-22 03:30"},
		{"0 4 * * sun", "2026-09-21 12:00", "2026-09-27 04:00"}, // 2026-09-21 is a Monday
		{"0 0 1 * *", "2026-09-21 12:00", "2026-10-01 00:00"},
		{"0 12 15 * mon", "2026-09-21 12:00", "2026-09-28 12:00"}, // dom OR dow: next Monday
		{"5/20 8-9 * * *", "2026-09-21 08:06", "2026-09-21 08:25"},
		{"@daily", "2026-09-21 12:00", "2026-09-22 00:00"},
		{"@every 90m", "2026-09-21 12:00", "2026-09-21 13:30"},
		{"0 0 29 2 *", "2026-09-21 12:00", "2028-02-29 00:00"},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got := s.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s from %s: got %s want %s", c.expr, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "@every 5s", "a b c d e"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
