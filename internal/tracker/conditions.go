package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ── alert conditions (memo: deterministic conditions with true / false / unknown) ────────────────────────────
//
// "Tell me when a price drops under 2,500" does not need a model in the loop: it is a comparison on a typed
// column, evaluated every time a row is written. The result is three-valued — a row whose price was not
// observed (or is not a number) is UNKNOWN, never "false": a failed observation must not look like a measured
// no, and must not re-arm an alert. Only a real transition fires: unknown/false → true is "condition met",
// true → false is "cleared".

type Condition struct {
	Name   string `json:"name"`
	Field  string `json:"field"`
	Op     string `json:"op"` // lt lte gt gte eq ne contains exists
	Value  any    `json:"value,omitempty"`
	Notify bool   `json:"notify,omitempty"` // tell the user when it becomes true
}

var condOps = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true, "eq": true, "ne": true, "contains": true, "exists": true}

const (
	resTrue    = "true"
	resFalse   = "false"
	resUnknown = "unknown"
)

func (c Condition) Describe() string {
	if c.Op == "exists" {
		return c.Field + " is known"
	}
	sym := map[string]string{"lt": "<", "lte": "≤", "gt": ">", "gte": "≥", "eq": "=", "ne": "≠", "contains": "contains"}[c.Op]
	return fmt.Sprintf("%s %s %v", c.Field, sym, c.Value)
}

// validateConditions normalises and checks a tracker's alerts against its columns.
func validateConditions(cols []Column, cs []Condition) ([]Condition, error) {
	byName := map[string]Column{}
	for _, c := range cols {
		byName[strings.ToLower(c.Name)] = c
	}
	seen := map[string]bool{}
	out := make([]Condition, 0, len(cs))
	for _, c := range cs {
		c.Name = strings.TrimSpace(c.Name)
		c.Op = strings.ToLower(strings.TrimSpace(c.Op))
		if c.Name == "" {
			return nil, errors.New("an alert needs a name")
		}
		if seen[strings.ToLower(c.Name)] {
			return nil, fmt.Errorf("two alerts are named %q", c.Name)
		}
		seen[strings.ToLower(c.Name)] = true
		if !condOps[c.Op] {
			return nil, fmt.Errorf("alert %q: operator must be one of lt, lte, gt, gte, eq, ne, contains, exists", c.Name)
		}
		col, ok := byName[strings.ToLower(strings.TrimSpace(c.Field))]
		if !ok {
			return nil, fmt.Errorf("alert %q: there is no column %q", c.Name, c.Field)
		}
		c.Field = col.Name
		if c.Op == "exists" {
			c.Value = nil
		} else {
			if c.Value == nil {
				return nil, fmt.Errorf("alert %q needs a value to compare with", c.Name)
			}
			switch col.Type {
			case TypeNumber, TypeTime, TypeBoolean:
				v, err := coerce(col, c.Value)
				if err != nil {
					return nil, fmt.Errorf("alert %q: %w", c.Name, err)
				}
				c.Value = v
			}
			if (c.Op == "lt" || c.Op == "lte" || c.Op == "gt" || c.Op == "gte") && col.Type != TypeNumber && col.Type != TypeTime {
				return nil, fmt.Errorf("alert %q: %s compares numbers or dates, but column %q is %s — declare the column's type first", c.Name, c.Op, col.Name, orFree(col.Type))
			}
			if c.Op == "contains" && col.Type != TypeString && col.Type != TypeStrings && col.Type != "" {
				return nil, fmt.Errorf("alert %q: contains works on text or lists, not %s", c.Name, col.Type)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func orFree(t string) string {
	if t == "" {
		return "free-form"
	}
	return t
}

// evalCondition is the three-valued test of one row.
func evalCondition(c Condition, col Column, r Row) string {
	if r.Status != "active" {
		return resUnknown // a missing or retired row says nothing about its fields
	}
	v, present := r.Data[c.Field]
	if c.Op == "exists" {
		if present && v != nil && v != "" {
			return resTrue
		}
		return resFalse
	}
	if !present || v == nil || v == "" {
		return resUnknown
	}
	cv, err := coerce(col, v)
	if err != nil {
		return resUnknown
	}
	want, err := coerce(col, c.Value)
	if err != nil {
		return resUnknown
	}
	switch c.Op {
	case "contains":
		needle := strings.ToLower(strings.TrimSpace(fmt.Sprint(c.Value)))
		switch x := cv.(type) {
		case string:
			return boolRes(strings.Contains(strings.ToLower(x), needle))
		case []string:
			for _, e := range x {
				if strings.EqualFold(e, needle) {
					return resTrue
				}
			}
			return resFalse
		}
		return resUnknown
	case "eq", "ne":
		eq := strings.EqualFold(fmt.Sprint(cv), fmt.Sprint(want))
		if f1, ok1 := cv.(float64); ok1 {
			if f2, ok2 := want.(float64); ok2 {
				eq = f1 == f2
			}
		}
		return boolRes(eq == (c.Op == "eq"))
	}
	// ordering: numbers, or dates/times
	var cmp int
	switch x := cv.(type) {
	case float64:
		y, ok := want.(float64)
		if !ok {
			return resUnknown
		}
		cmp = sign(x - y)
	case string:
		a, aok := parseWhen(x)
		b, bok := parseWhen(fmt.Sprint(want))
		if !aok || !bok {
			return resUnknown
		}
		cmp = a.Compare(b)
	default:
		return resUnknown
	}
	switch c.Op {
	case "lt":
		return boolRes(cmp < 0)
	case "lte":
		return boolRes(cmp <= 0)
	case "gt":
		return boolRes(cmp > 0)
	default:
		return boolRes(cmp >= 0)
	}
}

func sign(f float64) int {
	switch {
	case f < 0:
		return -1
	case f > 0:
		return 1
	}
	return 0
}

func boolRes(b bool) string {
	if b {
		return resTrue
	}
	return resFalse
}

func parseWhen(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// Alert is what a condition transition reports to the host.
type Alert struct {
	Tracker   Tracker
	Row       Row
	Condition Condition
	Met       bool // true: became true; false: was true and is now cleared
}

// Conditions returns a tracker's alerts.
func (s *Service) Conditions(ctx context.Context, trackerID int64) ([]Condition, error) {
	var raw []byte
	if err := s.DB.QueryRow(ctx, `SELECT conditions FROM trackers WHERE id=$1`, trackerID).Scan(&raw); err != nil {
		return nil, err
	}
	var cs []Condition
	_ = json.Unmarshal(raw, &cs)
	if cs == nil {
		cs = []Condition{}
	}
	return cs, nil
}

// SetConditions replaces a tracker's alerts (validated against its columns) and evaluates them on the rows it
// already has, so a freshly added alert reports what is true now instead of waiting for the next write.
func (s *Service) SetConditions(ctx context.Context, trackerID int64, cs []Condition) ([]Condition, error) {
	t, err := s.byID(ctx, trackerID)
	if err != nil {
		return nil, err
	}
	cs, err = validateConditions(t.Columns, cs)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(cs)
	if _, err := s.DB.Exec(ctx, `UPDATE trackers SET conditions=$2 WHERE id=$1`, trackerID, b); err != nil {
		return nil, err
	}
	t.Conditions = cs
	// drop state of alerts that no longer exist, then settle the rest on current rows
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	_, _ = s.DB.Exec(ctx, `DELETE FROM tracker_condition_state WHERE row_id IN (SELECT id FROM tracker_rows WHERE tracker_id=$1) AND NOT (name=ANY($2))`, trackerID, names)
	if rows, err := s.Rows(ctx, trackerID, "", false, 1000); err == nil {
		s.evaluate(ctx, *t, rows, "")
	}
	s.changed()
	return cs, nil
}

// evaluate settles every alert for the given rows. A transition is recorded as a row event and, for a
// notifying alert that became true, reported to the host (OnAlert). Unknown never changes anything.
func (s *Service) evaluate(ctx context.Context, t Tracker, rows []Row, runID string) {
	if len(t.Conditions) == 0 {
		return
	}
	cols := map[string]Column{}
	for _, c := range t.Columns {
		cols[c.Name] = c
	}
	for _, r := range rows {
		for _, c := range t.Conditions {
			res := evalCondition(c, cols[c.Field], r)
			if res == resUnknown {
				continue
			}
			var prev string
			err := s.DB.QueryRow(ctx, `SELECT result FROM tracker_condition_state WHERE row_id=$1 AND name=$2`, r.ID, c.Name).Scan(&prev)
			if err == nil && prev == res {
				continue
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO tracker_condition_state(row_id,name,result) VALUES($1,$2,$3)
				ON CONFLICT (row_id,name) DO UPDATE SET result=EXCLUDED.result, changed_at=now()`, r.ID, c.Name, res); err != nil {
				continue
			}
			switch {
			case res == resTrue:
				_, _ = s.DB.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value,kind,run_id) VALUES($1,$2,$3,$4,$5,$6,'condition_met',$7)`,
					t.ID, r.ID, r.Key, c.Name, orDash(prev), fmt.Sprintf("%s (now %v)", c.Describe(), r.Data[c.Field]), runID)
				if c.Notify && s.OnAlert != nil {
					s.OnAlert(ctx, Alert{Tracker: t, Row: r, Condition: c, Met: true})
				}
			case prev == resTrue:
				_, _ = s.DB.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value,kind,run_id) VALUES($1,$2,$3,$4,'true',$5,'condition_cleared',$6)`,
					t.ID, r.ID, r.Key, c.Name, fmt.Sprintf("no longer: %s (now %v)", c.Describe(), r.Data[c.Field]), runID)
			}
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
