// Package metrics records every model and tool call and aggregates them for the Usage dashboard.
// Records are kept for a few weeks (see Prune); nothing here is on the hot path: writes are fire-and-forget.
package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/llm"
)

// Retention is how long call records are kept.
const Retention = 35 * 24 * time.Hour

type Store struct{ DB *pgxpool.Pool }

func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// RecordLLM stores a finished model call (asynchronously; a slow database never delays an agent).
func (s *Store) RecordLLM(c llm.Call) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.DB.Exec(ctx, `INSERT INTO llm_calls(ts,model,agent,tokens_in,tokens_out,ms,wait_ms,ok,err,task_id,run_id,parent_run) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			c.TS, c.Model, c.Agent, c.In, c.Out, c.MS, c.WaitMS, c.OK, short(c.Err, 300), nilIfZero(c.TaskID), nilIfZero(c.RunID), nilIfZero(c.ParentRun))
	}()
}

// RecordTool stores an executed tool call.
func (s *Store) RecordTool(agent, tool string, ms int64, ok bool, errText string, taskID, runID, parentRun int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.DB.Exec(ctx, `INSERT INTO tool_calls(agent,tool,ms,ok,err,task_id,run_id,parent_run) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			agent, tool, ms, ok, short(errText, 300), nilIfZero(taskID), nilIfZero(runID), nilIfZero(parentRun))
	}()
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// Prune drops records older than Retention.
func (s *Store) Prune(ctx context.Context) {
	cut := time.Now().Add(-Retention)
	_, _ = s.DB.Exec(ctx, `DELETE FROM llm_calls WHERE ts < $1`, cut)
	_, _ = s.DB.Exec(ctx, `DELETE FROM tool_calls WHERE ts < $1`, cut)
}

// ── dashboard ───────────────────────────────────────────────────────────────

type Bucket struct {
	Label string `json:"label"` // "2026-09-21" or, for a 24 h view, "2026-09-21T14:00"
	Calls int64  `json:"calls"`
	Errs  int64  `json:"errors"`
	In    int64  `json:"tokens_in"`
	Out   int64  `json:"tokens_out"`
	Done  int64  `json:"tasks_done"`
	Fail  int64  `json:"tasks_failed"`
}

type Totals struct {
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	In        int64   `json:"tokens_in"`
	Out       int64   `json:"tokens_out"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
	TokPerSec float64 `json:"tok_per_s"`
	WaitAvgMS float64 `json:"wait_avg_ms"`
	WaitP95MS float64 `json:"wait_p95_ms"`
	TasksDone int64   `json:"tasks_done"`
	TasksFail int64   `json:"tasks_failed"`
	Since     *string `json:"recording_since,omitempty"` // when the first call in the range was recorded
}

type ModelRow struct {
	Model     string  `json:"model"`
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	In        int64   `json:"tokens_in"`
	Out       int64   `json:"tokens_out"`
	AvgMS     float64 `json:"avg_ms"`
	P95MS     float64 `json:"p95_ms"`
	TokPerSec float64 `json:"tok_per_s"`
}

type AgentRow struct {
	Agent  string `json:"agent"`
	Calls  int64  `json:"calls"`
	Errors int64  `json:"errors"`
	In     int64  `json:"tokens_in"`
	Out    int64  `json:"tokens_out"`
	Failed int64  `json:"tasks_failed"`
	Done   int64  `json:"tasks_done"`
}

type ToolRow struct {
	Tool   string  `json:"tool"`
	Calls  int64   `json:"calls"`
	Errors int64   `json:"errors"`
	AvgMS  float64 `json:"avg_ms"`
	P95MS  float64 `json:"p95_ms"`
}

type ErrorRow struct {
	TS    time.Time `json:"ts"`
	Kind  string    `json:"kind"` // llm | tool
	What  string    `json:"what"` // model or tool name
	Agent string    `json:"agent"`
	Err   string    `json:"error"`
}

type Dashboard struct {
	Days    int        `json:"days"`
	Hourly  bool       `json:"hourly"`
	TZ      string     `json:"tz"`
	Buckets []Bucket   `json:"buckets"`
	Totals  Totals     `json:"totals"`
	Models  []ModelRow `json:"models"`
	Agents  []AgentRow `json:"agents"`
	Tools   []ToolRow  `json:"tools"`
	Errors  []ErrorRow `json:"recent_errors"`
}

// Dashboard aggregates the last `days` days (1 → the last 24 hours in hourly buckets). tz is an IANA name
// (empty → the server's zone) so that "a day" matches the user's calendar.
func (s *Store) Dashboard(ctx context.Context, days int, tz string) (*Dashboard, error) {
	if days < 1 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	loc := time.Local
	if l, err := time.LoadLocation(tz); err == nil && tz != "" {
		loc = l
	}
	d := &Dashboard{Days: days, Hourly: days == 1, TZ: loc.String(), Models: []ModelRow{}, Agents: []AgentRow{}, Tools: []ToolRow{}, Errors: []ErrorRow{}}
	layout, step := "2006-01-02", 24*time.Hour
	if d.Hourly {
		layout = "2006-01-02T15:00"
	}
	// the bucket grid: every slot exists, so quiet days show as zero rather than vanishing
	now := time.Now().In(loc)
	var first time.Time
	if d.Hourly {
		first = now.Truncate(time.Hour).Add(-23 * time.Hour)
		step = time.Hour
	} else {
		y, m, dd := now.Date()
		first = time.Date(y, m, dd, 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	}
	idx := map[string]int{}
	for t, i := first, 0; !t.After(now); i++ {
		d.Buckets = append(d.Buckets, Bucket{Label: t.Format(layout)})
		idx[t.Format(layout)] = i
		if d.Hourly {
			t = t.Add(step)
		} else {
			t = t.AddDate(0, 0, 1)
		}
	}
	from := first.UTC()
	// Group in 15-minute UTC slices and place them into the user's buckets here: Postgres cannot use Go's
	// "Local" zone name, and this also handles half-hour zones and daylight-saving changes correctly.
	slot := func(col string) string { return `to_timestamp(floor(extract(epoch FROM ` + col + `) / 900) * 900)` }
	bucket := func(t time.Time) (int, bool) { i, ok := idx[t.In(loc).Format(layout)]; return i, ok }

	// model calls
	rows, err := s.DB.Query(ctx, `SELECT `+slot("ts")+`, count(*), count(*) FILTER (WHERE NOT ok), COALESCE(sum(tokens_in),0), COALESCE(sum(tokens_out),0)
		FROM llm_calls WHERE ts >= $1 GROUP BY 1`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t time.Time
		var c, e, in, out int64
		if err := rows.Scan(&t, &c, &e, &in, &out); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := bucket(t); ok {
			d.Buckets[i].Calls += c
			d.Buckets[i].Errs += e
			d.Buckets[i].In += in
			d.Buckets[i].Out += out
		}
	}
	rows.Close()

	// tasks (top-level and sub-tasks alike: each is a unit of agent work)
	rows, err = s.DB.Query(ctx, `SELECT `+slot("finished_at")+`, count(*) FILTER (WHERE status='done'), count(*) FILTER (WHERE status='failed')
		FROM tasks WHERE finished_at IS NOT NULL AND finished_at >= $1 GROUP BY 1`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t time.Time
		var done, fail int64
		if err := rows.Scan(&t, &done, &fail); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := bucket(t); ok {
			d.Buckets[i].Done += done
			d.Buckets[i].Fail += fail
			d.Totals.TasksDone += done
			d.Totals.TasksFail += fail
		}
	}
	rows.Close()

	// totals
	var since *time.Time
	err = s.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE NOT ok), COALESCE(sum(tokens_in),0), COALESCE(sum(tokens_out),0),
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY ms),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ms),0),
			COALESCE(sum(tokens_out) FILTER (WHERE ok AND tokens_out>0)::float8 / NULLIF(sum(ms) FILTER (WHERE ok AND tokens_out>0),0) * 1000, 0),
			COALESCE(avg(wait_ms),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY wait_ms),0), min(ts)
		FROM llm_calls WHERE ts >= $1`, from).Scan(&d.Totals.Calls, &d.Totals.Errors, &d.Totals.In, &d.Totals.Out,
		&d.Totals.P50MS, &d.Totals.P95MS, &d.Totals.TokPerSec, &d.Totals.WaitAvgMS, &d.Totals.WaitP95MS, &since)
	if err != nil {
		return nil, err
	}
	if since != nil {
		s := since.In(loc).Format(time.RFC3339)
		d.Totals.Since = &s
	}

	// by model
	rows, err = s.DB.Query(ctx, `SELECT model, count(*), count(*) FILTER (WHERE NOT ok), COALESCE(sum(tokens_in),0), COALESCE(sum(tokens_out),0),
			COALESCE(avg(ms),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ms),0),
			COALESCE(sum(tokens_out) FILTER (WHERE ok AND tokens_out>0)::float8 / NULLIF(sum(ms) FILTER (WHERE ok AND tokens_out>0),0) * 1000, 0)
		FROM llm_calls WHERE ts >= $1 GROUP BY model ORDER BY count(*) DESC LIMIT 12`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m ModelRow
		if err := rows.Scan(&m.Model, &m.Calls, &m.Errors, &m.In, &m.Out, &m.AvgMS, &m.P95MS, &m.TokPerSec); err != nil {
			rows.Close()
			return nil, err
		}
		d.Models = append(d.Models, m)
	}
	rows.Close()

	// by agent: model calls and task outcomes side by side
	rows, err = s.DB.Query(ctx, `SELECT COALESCE(NULLIF(a.agent,''),'(background)'), a.calls, a.errs, a.tin, a.tout, COALESCE(t.failed,0), COALESCE(t.done,0)
		FROM (SELECT agent, count(*) calls, count(*) FILTER (WHERE NOT ok) errs, COALESCE(sum(tokens_in),0) tin, COALESCE(sum(tokens_out),0) tout
			FROM llm_calls WHERE ts >= $1 GROUP BY agent) a
		LEFT JOIN (SELECT to_agent, count(*) FILTER (WHERE status='failed') failed, count(*) FILTER (WHERE status='done') done
			FROM tasks WHERE finished_at >= $1 GROUP BY to_agent) t ON t.to_agent = a.agent
		ORDER BY a.tin + a.tout DESC LIMIT 12`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a AgentRow
		if err := rows.Scan(&a.Agent, &a.Calls, &a.Errors, &a.In, &a.Out, &a.Failed, &a.Done); err != nil {
			rows.Close()
			return nil, err
		}
		d.Agents = append(d.Agents, a)
	}
	rows.Close()

	// tools
	rows, err = s.DB.Query(ctx, `SELECT tool, count(*), count(*) FILTER (WHERE NOT ok), COALESCE(avg(ms),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ms),0)
		FROM tool_calls WHERE ts >= $1 GROUP BY tool ORDER BY count(*) DESC LIMIT 12`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t ToolRow
		if err := rows.Scan(&t.Tool, &t.Calls, &t.Errors, &t.AvgMS, &t.P95MS); err != nil {
			rows.Close()
			return nil, err
		}
		d.Tools = append(d.Tools, t)
	}
	rows.Close()

	// recent errors of both kinds
	rows, err = s.DB.Query(ctx, `SELECT ts, kind, what, agent, err FROM (
			SELECT ts, 'llm' kind, model what, agent, err FROM llm_calls WHERE NOT ok AND ts >= $1
			UNION ALL SELECT ts, 'tool', tool, agent, err FROM tool_calls WHERE NOT ok AND ts >= $1) x ORDER BY ts DESC LIMIT 12`, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e ErrorRow
		if err := rows.Scan(&e.TS, &e.Kind, &e.What, &e.Agent, &e.Err); err != nil {
			rows.Close()
			return nil, err
		}
		d.Errors = append(d.Errors, e)
	}
	rows.Close()
	return d, nil
}
