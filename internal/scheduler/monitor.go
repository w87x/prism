package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"prism/internal/agent"
)

// A monitor is a watch with a time budget: checked every minute or so, ended automatically when its budget
// runs out, adjustable while it runs, and — when the thing it watches reports a percentage or "n/m" — able to
// estimate when it will finish. Monitors are ordinary intents of type "watch" with expires_at set, so they
// share the predicates, the tick loop and the Autonomy page with everything else.

type sample struct {
	T int64   `json:"t"` // unix seconds
	F float64 `json:"f"` // fraction done, 0..1
}

const maxSamples = 30

var (
	pctRe   = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
	ratioRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*/\s*(\d+(?:\.\d+)?)`)
)

// fractionOf reads how far along a progress text says something is: the last "NN%", else the last "a/b".
func fractionOf(progress string) (float64, bool) {
	if ms := pctRe.FindAllStringSubmatch(progress, -1); len(ms) > 0 {
		if v, err := strconv.ParseFloat(ms[len(ms)-1][1], 64); err == nil && v >= 0 && v <= 100 {
			return v / 100, true
		}
	}
	if ms := ratioRe.FindAllStringSubmatch(progress, -1); len(ms) > 0 {
		m := ms[len(ms)-1]
		a, e1 := strconv.ParseFloat(m[1], 64)
		b, e2 := strconv.ParseFloat(m[2], 64)
		if e1 == nil && e2 == nil && b > 0 && a <= b {
			return a / b, true
		}
	}
	return 0, false
}

// etaSeconds extrapolates the recent rate of progress to 100%. It needs at least two samples that moved
// forward; anything stalled, finished or absurdly far away (over a week) yields no estimate.
func etaSeconds(ss []sample) (int, bool) {
	if len(ss) < 2 {
		return 0, false
	}
	if len(ss) > 8 {
		ss = ss[len(ss)-8:]
	}
	first, last := ss[0], ss[len(ss)-1]
	dt := float64(last.T - first.T)
	if dt <= 0 || last.F >= 1 {
		return 0, false
	}
	slope := (last.F - first.F) / dt
	if slope <= 0 {
		return 0, false
	}
	eta := (1 - last.F) / slope
	if eta > 7*24*3600 {
		return 0, false
	}
	return int(eta), true
}

func parseSamples(b []byte) []sample {
	var ss []sample
	_ = json.Unmarshal(b, &ss)
	return ss
}

// recordSample appends the fraction found in a progress text (if any) to the intent's history.
func (s *Service) recordSample(ctx context.Context, id int64, prev []sample, progress string) []sample {
	f, ok := fractionOf(progress)
	if !ok {
		return prev
	}
	ss := append(append([]sample{}, prev...), sample{T: time.Now().Unix(), F: f})
	if len(ss) > maxSamples {
		ss = ss[len(ss)-maxSamples:]
	}
	b, _ := json.Marshal(ss)
	_, _ = s.DB.Exec(ctx, `UPDATE intents SET samples=$2 WHERE id=$1`, id, b)
	return ss
}

// ExtendIntent gives a monitor more time (or, with a negative amount, less) and wakes an expired one up.
func (s *Service) ExtendIntent(ctx context.Context, id int64, minutes int) error {
	if minutes == 0 {
		return nil
	}
	tag, err := s.DB.Exec(ctx, `UPDATE intents SET
			expires_at = GREATEST(now(), COALESCE(expires_at, now())) + make_interval(mins => $2),
			status = CASE WHEN status='expired' AND $2 > 0 THEN 'active' ELSE status END,
			next_due = CASE WHEN status='expired' AND $2 > 0 THEN now() ELSE next_due END
		WHERE id=$1 AND type='watch'`, id, minutes)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("no such monitor")
	}
	s.emit("intent.update", map[string]any{"id": id})
	return err
}

// SetAnnounce switches whether a monitor reports progress changes while it runs.
func (s *Service) SetAnnounce(ctx context.Context, id int64, on bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE intents SET announce=$2 WHERE id=$1`, id, on)
	s.emit("intent.update", map[string]any{"id": id})
	return err
}

// expire ends a monitor whose time budget ran out and tells its owner and the user.
func (s *Service) expire(ctx context.Context, i Intent) {
	_, _ = s.DB.Exec(ctx, `UPDATE intents SET status='expired' WHERE id=$1 AND status='active'`, i.ID)
	s.emit("intent.update", map[string]any{"id": i.ID})
	msg := fmt.Sprintf("Monitor “%s” ended: its time budget ran out. Last progress: %s.", i.Description, firstNonEmpty(i.Progress, "none recorded"))
	if s.Notify != nil {
		s.Notify("intent", "attention", "Monitor ended", msg)
	}
	s.Engine.Notify(ctx, agent.Notice{Agent: i.Owner, Level: "attention", Text: msg})
}

// humanDuration renders seconds the way people say them ("3m", "1h 20m").
func humanDuration(sec int) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", sec)
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// budget turns a "for N minutes" argument into an expiry time (default when 0, capped at 24 hours).
func budget(minutes, def int) *time.Time {
	t := time.Now().Add(time.Duration(clampMinutes(minutes, def)) * time.Minute)
	return &t
}

func clampMinutes(minutes, def int) int {
	if minutes <= 0 {
		minutes = def
	}
	return min(minutes, 24*60)
}

func timeLeft(i Intent) string {
	if i.ExpiresAt == nil {
		return "no time limit"
	}
	left := time.Until(*i.ExpiresAt)
	if left <= 0 {
		return "time is up"
	}
	return humanDuration(int(left.Seconds())) + " left"
}

// monitorNote is the " (est. 12m left, 3h left before expiry)" suffix shown in lists.
func monitorNote(i Intent) string {
	var parts []string
	if i.ETASeconds != nil {
		parts = append(parts, "finishes in about "+humanDuration(*i.ETASeconds))
	}
	if i.ExpiresAt != nil && i.Status == "active" {
		parts = append(parts, timeLeft(i))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}
