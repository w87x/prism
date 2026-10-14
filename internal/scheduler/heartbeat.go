package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/settings"
)

// Heartbeat. One editable checklist ("is there a new episode of X?", "anything urgent in my mail?", "is the NAS still reachable?"),
// worked through every so often by a cheap model, and silent unless an item needs the user. It is a system cron under the hood, so
// it inherits everything crons already have — the routine model with escalation, the quiet-notice filter, promise review, the
// task list — plus a memory of what it already reported, so the same news is not repeated every half hour.

// HeartbeatName is the system cron's (and its tasks') name.
const HeartbeatName = "Heartbeat"

const heartbeatPrompt = `Heartbeat. This is a periodic look through the user's standing checklist, run quietly. For each item decide whether it needs the user's attention NOW, using your tools (search, fetch, mail, trackers, memory) only as far as the item requires. Items that are fine, unchanged or not yet due are not news.
If nothing needs attention, reply with exactly NO_REPLY. Otherwise write a short message with only the items that do: one line each, the essential detail, nothing about the rest. Never repeat what the user was already told (listed below, when there is anything) unless it has changed.

CHECKLIST:
`

// heartbeatExpr turns the interval in minutes into a cron expression.
func heartbeatExpr(everyMin int) string {
	switch {
	case everyMin <= 0:
		everyMin = 30
	case everyMin < 5:
		everyMin = 5
	}
	if everyMin < 60 {
		return fmt.Sprintf("*/%d * * * *", everyMin)
	}
	if h := everyMin / 60; h > 1 {
		return fmt.Sprintf("0 */%d * * *", min(h, 23))
	}
	return "0 * * * *"
}

// SaveHeartbeat stores the setting and brings the system cron in line with it (creating it on first use).
func (s *Service) SaveHeartbeat(ctx context.Context, hb settings.Heartbeat) error {
	hb.Checklist = strings.TrimSpace(hb.Checklist)
	if hb.Agent == "" {
		hb.Agent = "Atlas"
	}
	if hb.EveryMin <= 0 {
		hb.EveryMin = 30
	}
	if hb.Enabled && hb.Checklist == "" {
		return errors.New("write at least one checklist item first")
	}
	if _, err := s.Engine.Profiles.Get(ctx, hb.Agent); err != nil {
		return fmt.Errorf("agent %q does not exist", hb.Agent)
	}
	if err := s.Settings.Set(ctx, settings.KeyHeartbeat, hb); err != nil {
		return err
	}
	c := Cron{Name: HeartbeatName, Agent: hb.Agent, Expr: heartbeatExpr(hb.EveryMin), Prompt: heartbeatPrompt + hb.Checklist, Enabled: hb.Enabled && hb.Checklist != "", System: true}
	var id int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM crons WHERE system AND name=$1`, HeartbeatName).Scan(&id); err != nil {
		if hb.Checklist == "" {
			return nil // nothing to schedule yet
		}
		newID, err := s.SaveCron(ctx, c)
		if err != nil {
			return err
		}
		_, _ = s.DB.Exec(ctx, `UPDATE crons SET topic='', project_bank_id=NULL WHERE id=$1`, newID) // it reports in the main chat, not in a topic of its own
		return nil
	}
	c.ID = id
	_, err := s.SaveCron(ctx, c)
	return err
}

// RunHeartbeatNow fires one beat at once.
func (s *Service) RunHeartbeatNow(ctx context.Context) error {
	var id int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM crons WHERE system AND name=$1`, HeartbeatName).Scan(&id); err != nil {
		return errors.New("save the checklist first")
	}
	return s.RunCronNow(ctx, id)
}

// heartbeatRecent lists what recent beats told the user, so the next one does not say it again.
func (s *Service) heartbeatRecent(ctx context.Context) string {
	rows, err := s.DB.Query(ctx, `SELECT created_at, left(result, 500) FROM tasks WHERE from_kind='cron' AND from_name=$1 AND status='done' AND result<>'' AND result NOT ILIKE '%NO_REPLY%'
		ORDER BY id DESC LIMIT 4`, HeartbeatName)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var res string
		var t timeT
		if rows.Scan(&t, &res) != nil {
			continue
		}
		fmt.Fprintf(&sb, "- (%s) %s\n", t.Format("2 Jan 15:04"), strings.Join(strings.Fields(res), " "))
	}
	if sb.Len() == 0 {
		return ""
	}
	return "\n\nALREADY TOLD THE USER (do not repeat unless it changed):\n" + sb.String()
}

// HeartbeatBeat is one recent run, for the settings page.
type HeartbeatBeat struct {
	ID     int64  `json:"id"`
	At     string `json:"at"`
	Status string `json:"status"`
	Silent bool   `json:"silent"`
	Result string `json:"result"`
}

// HeartbeatState is the setting plus when it runs next and how the last beats went.
func (s *Service) HeartbeatState(ctx context.Context) map[string]any {
	hb := settings.Load(ctx, s.Settings, settings.KeyHeartbeat, settings.Heartbeat{EveryMin: 30, Agent: "Atlas"})
	if hb.EveryMin <= 0 {
		hb.EveryMin = 30
	}
	if hb.Agent == "" {
		hb.Agent = "Atlas"
	}
	out := map[string]any{"enabled": hb.Enabled, "every_min": hb.EveryMin, "agent": hb.Agent, "checklist": hb.Checklist, "beats": []HeartbeatBeat{}}
	var next *timeT
	if s.DB.QueryRow(ctx, `SELECT next_run FROM crons WHERE system AND name=$1 AND enabled`, HeartbeatName).Scan(&next) == nil && next != nil {
		out["next_run"] = next.Format("2006-01-02T15:04:05Z07:00")
	}
	rows, err := s.DB.Query(ctx, `SELECT id, created_at, status, left(result,300) FROM tasks WHERE from_kind='cron' AND from_name=$1 ORDER BY id DESC LIMIT 12`, HeartbeatName)
	if err == nil {
		defer rows.Close()
		beats := []HeartbeatBeat{}
		for rows.Next() {
			var b HeartbeatBeat
			var t timeT
			if rows.Scan(&b.ID, &t, &b.Status, &b.Result) == nil {
				b.At = t.Format("2006-01-02T15:04:05Z07:00")
				b.Silent = strings.Contains(strings.ToUpper(b.Result), "NO_REPLY")
				beats = append(beats, b)
			}
		}
		out["beats"] = beats
	}
	return out
}

type timeT = time.Time
