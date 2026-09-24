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
// any other non-base tool (tool_search / AutoTools).
func RegisterTools(reg *tools.Registry, s *Service) {
	reg.Register(
		&tools.Tool{
			Name: "tracker_create", Category: "trackers", Risk: tools.RiskWrite,
			Description: "Create a structured tracker: a named table other tools then populate with evidence-backed rows over time (e.g. \"Apartments\" with columns Price/Bedrooms/Location/Status). Check tracker_list first — reuse an existing tracker for the same topic rather than creating a near-duplicate.",
			Params: tools.Obj("name,columns",
				tools.Str("name", "short, distinctive name"),
				tools.Str("description", "what this tracks and why"),
				tools.ObjList("columns", "the table's columns", "name", tools.Str("name", "column name"), tools.Str("description", "what belongs in this column"))),
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
				return fmt.Sprintf("Tracker %q created with %d column(s).", t.Name, len(t.Columns)), nil
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
					}
					fmt.Fprintf(&sb, "%s — %s [%s] (%d rows)\n", t.Name, t.Description, strings.Join(cols, ", "), t.Rows)
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
			Name: "tracker_rows", Category: "trackers", Risk: tools.RiskRead,
			Description: "List a tracker's rows, most recently updated first.",
			Params: tools.Obj("tracker",
				tools.Str("tracker", "tracker name"),
				tools.Str("query", "optional text filter"),
				tools.Bool("include_gone", "include retired rows"),
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
					if r.Status != "active" {
						status = " [" + r.Status + "]"
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
					fmt.Fprintf(&sb, "[tracker #%d] %s.%s: %q → %q (%s)\n", c.TrackerID, c.RowKey, c.Field, c.OldValue, c.NewValue, c.CreatedAt.Format("2006-01-02 15:04"))
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
