package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"prism/internal/tools"
)

// RegisterTools installs the structured-tracker toolset: create a table, populate/update its rows with
// evidence, and report what changed. Not restricted to a particular agent — any agent (including one Forge
// hires for the purpose, e.g. "apartment hunter") can build and maintain a tracker; it is discovered like
// any other non-base tool: it has to be listed in an agent's toolset.
func RegisterTools(reg *tools.Registry, s *Service) {
	reg.Register(
		&tools.Tool{
			Name: "tracker_create", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Create a structured tracker: a named table other tools then populate with evidence-backed rows over time (e.g. \"Apartments\" with columns Price/Bedrooms/Location/Status). Check tracker_list first — reuse an existing tracker for the same topic rather than creating a near-duplicate.",
			Params: tools.Obj("name,columns",
				tools.Str("name", "short, distinctive name"),
				tools.Str("description", "what this tracks and why"),
				tools.ObjList("columns", "the table's columns", "name", tools.Str("name", "column name"),
					tools.Enum("type", "optional but recommended: string, number, boolean, time (date or RFC 3339) or strings (a list). A typed column is checked on every write and is what alerts compare", "string", "number", "boolean", "time", "strings"),
					tools.Bool("required", "must be present on every new row"),
					tools.Str("unit", "unit of a number, e.g. USD or GB (documentation)"),
					tools.Str("description", "what belongs in this column"))),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Name        string
					Description string
					Columns     []Column
				}](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Create(ctx, a.Name, a.Description, a.Columns, env.Agent)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Tracker %q created with %d column(s). Rows go in with tracker_snapshot (a whole refresh, idempotent) or tracker_row_upsert (one row); tracker_alert sets alerts on typed columns.", t.Name, len(t.Columns)), nil
			},
		},
		&tools.Tool{
			Name: "tracker_list", Category: "trackers", Risk: tools.RiskRead,
			Description: "List trackers: name, description, columns, row count. Check this before starting research that might already be tracked, or creating a new tracker for a topic that already has one.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				ts, err := s.List(ctx)
				if err != nil {
					return "", err
				}
				if len(ts) == 0 {
					return "No trackers yet.", nil
				}
				var sb strings.Builder
				for _, t := range ts {
					cols := make([]string, len(t.Columns))
					for i, c := range t.Columns {
						cols[i] = c.Name
						if c.Type != "" {
							cols[i] += ":" + c.Type
						}
						if c.Required {
							cols[i] += "*"
						}
					}
					fmt.Fprintf(&sb, "%s — %s [%s] (%d rows)", t.Name, t.Description, strings.Join(cols, ", "), t.Rows)
					if len(t.Conditions) > 0 {
						al := make([]string, len(t.Conditions))
						for i, c := range t.Conditions {
							al[i] = c.Name + ": " + c.Describe()
						}
						fmt.Fprintf(&sb, " alerts: %s", strings.Join(al, "; "))
					}
					sb.WriteString("\n")
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "tracker_row_upsert", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Add or update one row in a tracker, keyed by a stable identifier you choose (e.g. the listing URL). Submitting the same key again updates that row and records exactly what changed. Only send fields you actually re-observed — resubmitting an unchanged value produces no change entry, but sending a field with the same value you already stored is harmless (it's just a no-op for that field).",
			Params: tools.Obj("tracker,key,data",
				tools.Str("tracker", "tracker name"),
				tools.Str("key", "stable id for this row (e.g. the listing URL)"),
				tools.Any("data", "the row's fields as an object matching the tracker's columns"),
				tools.Str("source_url", "where this data came from")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tracker   string
					Key       string
					Data      map[string]any
					SourceURL string `json:"source_url"`
				}](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Get(ctx, a.Tracker)
				if err != nil {
					return "", err
				}
				row, changes, err := s.UpsertRow(ctx, t.ID, a.Key, a.Data, a.SourceURL)
				if err != nil {
					return "", err
				}
				if len(changes) == 0 {
					return fmt.Sprintf("Row %q unchanged.", row.Key), nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "Row %q saved, %d change(s):\n", row.Key, len(changes))
				for _, c := range changes {
					fmt.Fprintf(&sb, "- %s: %q → %q\n", c.Field, c.OldValue, c.NewValue)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "tracker_snapshot", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Record one whole refresh of a tracker: everything you observed, applied atomically as one run. Prefer this to many tracker_row_upsert calls. " +
				"complete=true means 'this is EVERYTHING the source lists' — rows you did not send are then marked MISSING (not deleted; they return, with an event, if seen again). " +
				"complete=false for a page of results or a partial search: nothing is marked missing. The run_id makes a retry safe (same run_id + same rows = nothing is applied twice; same run_id + different rows is refused). " +
				"Keys must be the source's own stable ids (a listing URL, a repository id), never a title. Leave out a field you could not observe — never invent a value. A bad row fails the whole run with every problem listed.",
			Params: tools.Obj("tracker,run_id,rows",
				tools.Str("tracker", "tracker name"),
				tools.Str("run_id", "unique id of this refresh, e.g. the date and time"),
				tools.Bool("complete", "true only if rows is everything the source lists"),
				tools.ObjList("rows", "what you observed", "key",
					tools.Str("key", "the source's stable id for this row"),
					tools.Any("data", "the row's fields as an object matching the tracker's columns"),
					tools.Str("source_url", "where this row came from"))),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tracker  string
					RunID    string     `json:"run_id"`
					Complete bool       `json:"complete"`
					Rows     []Observed `json:"rows"`
				}](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Get(ctx, a.Tracker)
				if err != nil {
					return "", err
				}
				r, err := s.Commit(ctx, t.ID, a.RunID, a.Rows, a.Complete)
				if err != nil {
					return "", err
				}
				if r.Replayed {
					return fmt.Sprintf("Run %q was already applied (%d observed, %d added, %d changed, %d missing); nothing done again.", r.RunID, r.Observed, r.Added, r.Changed, r.Missing), nil
				}
				out := fmt.Sprintf("Run %q applied: %d observed — %d added, %d changed, %d reappeared, %d unchanged", r.RunID, r.Observed, r.Added, r.Changed, r.Reappeared, r.Unchanged)
				if r.Complete {
					out += fmt.Sprintf(", %d now missing", r.Missing)
				} else {
					out += " (partial: absent rows left alone)"
				}
				return out + ". tracker_changes reports what is new.", nil
			},
		},
		&tools.Tool{
			Name: "tracker_alert", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Add or replace an alert on a tracker: a deterministic test on a typed column that is checked every time a row is written, no model involved. " +
				"When it becomes true for a row the user is notified (notify=true); a row whose value was not observed is UNKNOWN, never false. Example: name 'cheap', field 'Price', op 'lte', value 2500. " +
				"Operators: lt, lte, gt, gte (numbers or dates), eq, ne, contains (text/list), exists. Use this instead of a recurring intent for 'tell me when X reaches Y' over tracked rows.",
			Params: tools.Obj("tracker,name,field,op",
				tools.Str("tracker", "tracker name"),
				tools.Str("name", "short name of the alert"),
				tools.Str("field", "the column to test"),
				tools.Enum("op", "comparison", "lt", "lte", "gt", "gte", "eq", "ne", "contains", "exists"),
				tools.Any("value", "what to compare with (omit for exists)"),
				tools.Bool("notify", "tell the user when it becomes true (default true)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tracker, Name, Field, Op string
					Value                    any
					Notify                   *bool
				}](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Get(ctx, a.Tracker)
				if err != nil {
					return "", err
				}
				notify := true
				if a.Notify != nil {
					notify = *a.Notify
				}
				var cs []Condition
				for _, c := range t.Conditions {
					if !strings.EqualFold(c.Name, a.Name) {
						cs = append(cs, c)
					}
				}
				cs = append(cs, Condition{Name: a.Name, Field: a.Field, Op: a.Op, Value: a.Value, Notify: notify})
				if _, err := s.SetConditions(ctx, t.ID, cs); err != nil {
					return "", err
				}
				return fmt.Sprintf("Alert %q set on %q. It has been checked against the existing rows already.", a.Name, t.Name), nil
			},
		},
		&tools.Tool{
			Name: "tracker_alert_remove", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Remove an alert from a tracker.",
			Params:      tools.Obj("tracker,name", tools.Str("tracker", "tracker name"), tools.Str("name", "the alert's name")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Tracker, Name string }](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Get(ctx, a.Tracker)
				if err != nil {
					return "", err
				}
				var cs []Condition
				for _, c := range t.Conditions {
					if !strings.EqualFold(c.Name, a.Name) {
						cs = append(cs, c)
					}
				}
				if len(cs) == len(t.Conditions) {
					return "", fmt.Errorf("tracker %q has no alert named %q", t.Name, a.Name)
				}
				if _, err := s.SetConditions(ctx, t.ID, cs); err != nil {
					return "", err
				}
				return fmt.Sprintf("Alert %q removed.", a.Name), nil
			},
		},
		&tools.Tool{
			Name: "tracker_rows", Category: "trackers", Risk: tools.RiskRead,
			Description: "List a tracker's rows, most recently updated first.",
			Params: tools.Obj("tracker",
				tools.Str("tracker", "tracker name"),
				tools.Str("query", "optional text filter"),
				tools.Bool("include_gone", "include missing and retired rows"),
				tools.Int("limit", "max rows (default 100)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tracker     string
					Query       string
					IncludeGone bool `json:"include_gone"`
					Limit       int
				}](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Get(ctx, a.Tracker)
				if err != nil {
					return "", err
				}
				rows, err := s.Rows(ctx, t.ID, a.Query, a.IncludeGone, a.Limit)
				if err != nil {
					return "", err
				}
				if len(rows) == 0 {
					return "No rows.", nil
				}
				var sb strings.Builder
				for _, r := range rows {
					db, _ := json.Marshal(r.Data)
					status := ""
					switch r.Status {
					case "missing":
						status = " [missing since " + r.MissingSince.Format("2006-01-02") + " — not seen in a complete snapshot]"
					case "gone":
						status = " [retired]"
					}
					fmt.Fprintf(&sb, "%s%s: %s\n", r.Key, status, db)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "tracker_row_retire", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Mark a row as no longer active (e.g. a listing was taken down or sold) — itself a meaningful change, recorded in the tracker's history rather than just deleted.",
			Params: tools.Obj("tracker,key",
				tools.Str("tracker", "tracker name"),
				tools.Str("key", "the row's key"),
				tools.Str("reason", "why (optional)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Tracker, Key, Reason string }](raw)
				if err != nil {
					return "", err
				}
				t, err := s.Get(ctx, a.Tracker)
				if err != nil {
					return "", err
				}
				if _, err := s.RetireRow(ctx, t.ID, a.Key, a.Reason); err != nil {
					return "", err
				}
				return fmt.Sprintf("Row %q retired.", a.Key), nil
			},
		},
		&tools.Tool{
			Name: "tracker_changes", Category: "trackers", Risk: tools.RiskRead,
			Description: "What changed in a tracker (or every tracker) since a given time — the report to give the user after a refresh.",
			Params: tools.Obj("",
				tools.Str("tracker", "tracker name (omit for all trackers)"),
				tools.Str("since", "RFC3339 timestamp, or a duration like \"24h\" / \"7d\" (default 24h)"),
				tools.Int("limit", "max entries (default 50)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tracker string
					Since   string
					Limit   int
				}](raw)
				if err != nil {
					return "", err
				}
				var trackerID int64
				if a.Tracker != "" {
					t, err := s.Get(ctx, a.Tracker)
					if err != nil {
						return "", err
					}
					trackerID = t.ID
				}
				since, err := parseSince(a.Since)
				if err != nil {
					return "", err
				}
				changes, err := s.Changes(ctx, trackerID, since, a.Limit)
				if err != nil {
					return "", err
				}
				if len(changes) == 0 {
					return "No changes.", nil
				}
				var sb strings.Builder
				for _, c := range changes {
					fmt.Fprintf(&sb, "[tracker #%d] %s %s.%s: %q → %q (%s)\n", c.TrackerID, strings.ToUpper(strings.ReplaceAll(c.Kind, "_", " ")), c.RowKey, c.Field, c.OldValue, c.NewValue, c.CreatedAt.Format("2006-01-02 15:04"))
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "tracker_delete", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Permanently delete a tracker and all its rows and history.",
			Params:      tools.Obj("tracker", tools.Str("tracker", "tracker name")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Tracker string }](raw)
				if err != nil {
					return "", err
				}
				if err := s.Delete(ctx, a.Tracker); err != nil {
					return "", err
				}
				return fmt.Sprintf("Tracker %q deleted.", a.Tracker), nil
			},
		},
	)
}

// parseSince accepts an RFC3339 timestamp, a Go duration ("24h", "90m"), or a day count ("7d"); empty
// defaults to the last 24 hours.
func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().Add(-24 * time.Hour), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, errors.New(`invalid "since": use an RFC3339 timestamp, a duration like "24h", or "7d"`)
}
