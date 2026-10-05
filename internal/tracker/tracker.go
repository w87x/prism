// Package tracker implements structured trackers: user-defined named tables (e.g. "Apartments", "Hardware
// shortlist") that agents populate with evidence-backed rows over time. Every row update is diffed
// field-by-field against what was there before, so a later refresh can report exactly what changed — the
// "what's new since last time?" job neither a memory fact (one self-contained sentence) nor a bookmark (one
// link) is shaped for. Deliberately built on the storage/tooling patterns already established elsewhere in
// this codebase (transactional writes, jsonb columns scanned via RawMessage) rather than new machinery.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Column describes one field. Type is optional (string, number, boolean, time, strings): a declared type is
// enforced on every write and is what makes alert conditions possible; no type = free-form, as before. Required
// columns must be present on a new row. Unit documents a number ("USD", "GB").
type Column struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Description string `json:"description,omitempty"`
}

type Tracker struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Columns     []Column `json:"columns"`
	// Conditions are the tracker's alerts (see conditions.go): deterministic comparisons on typed columns,
	// evaluated on every write.
	Conditions []Condition `json:"conditions"`
	CreatedBy  string      `json:"created_by"`
	Status     string      `json:"status"`
	CreatedAt  time.Time   `json:"created_at"`
	Rows       int         `json:"rows"`
}

type Row struct {
	ID        int64          `json:"id"`
	TrackerID int64          `json:"tracker_id"`
	Key       string         `json:"key"`
	Data      map[string]any `json:"data"`
	SourceURL string         `json:"source_url"`
	// Status: active, missing (absent from a COMPLETE snapshot — not observed, which is not the same as gone) or
	// gone (retired on purpose).
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastSeen     time.Time  `json:"last_seen"`
	MissingSince *time.Time `json:"missing_since,omitempty"`
}

type Change struct {
	ID        int64  `json:"id"`
	TrackerID int64  `json:"tracker_id"`
	RowID     int64  `json:"row_id"`
	RowKey    string `json:"row_key"`
	Field     string `json:"field"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
	// Kind: added, changed, missing, reappeared, retired, condition_met, condition_cleared. RunID names the
	// snapshot that produced it ("" for a single-row write).
	Kind      string    `json:"kind"`
	RunID     string    `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	DB *pgxpool.Pool
	// OnChange notifies the UI after a tracker, row or change is written.
	OnChange func()
	// OnAlert is told when a notifying alert condition becomes true (or a met one clears): the host turns it into
	// a message to the user. May be nil.
	OnAlert func(ctx context.Context, a Alert)
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

const trackerCols = `id,name,description,columns,conditions,created_by,status,created_at`

func scanTracker(r pgx.Row) (Tracker, error) {
	var t Tracker
	var cb, cd []byte
	if err := r.Scan(&t.ID, &t.Name, &t.Description, &cb, &cd, &t.CreatedBy, &t.Status, &t.CreatedAt); err != nil {
		return t, err
	}
	_ = json.Unmarshal(cb, &t.Columns)
	_ = json.Unmarshal(cd, &t.Conditions)
	if t.Conditions == nil {
		t.Conditions = []Condition{}
	}
	return t, nil
}

const rowCols = `id,tracker_id,key,data,source_url,status,created_at,updated_at,last_seen,missing_since`

func scanRow(r pgx.Row) (Row, error) {
	var row Row
	var db []byte
	if err := r.Scan(&row.ID, &row.TrackerID, &row.Key, &db, &row.SourceURL, &row.Status, &row.CreatedAt, &row.UpdatedAt, &row.LastSeen, &row.MissingSince); err != nil {
		return row, err
	}
	_ = json.Unmarshal(db, &row.Data)
	if row.Data == nil {
		row.Data = map[string]any{}
	}
	return row, nil
}

// Create defines a new tracker. A column with a declared type is enforced (and enables alert conditions); a column
// without one is documentation for the agents that populate it, and fields outside the declared columns are
// accepted as free-form, so a later field added by convention doesn't need a migration.
func (s *Service) Create(ctx context.Context, name, description string, cols []Column, by string) (*Tracker, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if len(cols) == 0 {
		return nil, errors.New("at least one column is required")
	}
	seen := map[string]bool{}
	for i := range cols {
		cols[i].Name = strings.TrimSpace(cols[i].Name)
		if cols[i].Name == "" {
			return nil, errors.New("every column needs a name")
		}
		if seen[strings.ToLower(cols[i].Name)] {
			return nil, fmt.Errorf("two columns are named %q", cols[i].Name)
		}
		seen[strings.ToLower(cols[i].Name)] = true
		cols[i].Type = strings.ToLower(strings.TrimSpace(cols[i].Type))
		if !validType(cols[i].Type) {
			return nil, fmt.Errorf("column %q: type must be string, number, boolean, time or strings (or left out)", cols[i].Name)
		}
	}
	cb, err := json.Marshal(cols)
	if err != nil {
		return nil, err
	}
	t, err := scanTracker(s.DB.QueryRow(ctx, `INSERT INTO trackers(name,description,columns,created_by) VALUES($1,$2,$3,$4) RETURNING `+trackerCols,
		name, description, cb, by))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("a tracker named %q already exists", name)
		}
		return nil, err
	}
	s.changed()
	return &t, nil
}

// List summarizes every tracker (including its row count) for a catalog view.
func (s *Service) List(ctx context.Context) ([]Tracker, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+trackerCols+`, (SELECT count(*) FROM tracker_rows r WHERE r.tracker_id=t.id) FROM trackers t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tracker{}
	for rows.Next() {
		var t Tracker
		var cb, cd []byte
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &cb, &cd, &t.CreatedBy, &t.Status, &t.CreatedAt, &t.Rows); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cb, &t.Columns)
		_ = json.Unmarshal(cd, &t.Conditions)
		if t.Conditions == nil {
			t.Conditions = []Condition{}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, name string) (*Tracker, error) {
	t, err := scanTracker(s.DB.QueryRow(ctx, `SELECT `+trackerCols+` FROM trackers WHERE name=$1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("no tracker named %q", name)
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Service) byID(ctx context.Context, id int64) (*Tracker, error) {
	t, err := scanTracker(s.DB.QueryRow(ctx, `SELECT `+trackerCols+` FROM trackers WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("no tracker #%d", id)
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Service) Delete(ctx context.Context, name string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM trackers WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no tracker named %q", name)
	}
	s.changed()
	return nil
}

// Rows lists a tracker's rows, most recently updated first. query, when non-empty, is a case-insensitive
// substring match against the row's data (a tracker is expected to hold at most low hundreds of rows, so a
// simple ILIKE is enough — no need for the BM25 machinery memory/bookmarks use for much larger corpora).
func (s *Service) Rows(ctx context.Context, trackerID int64, query string, includeGone bool, limit int) ([]Row, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	sql := `SELECT ` + rowCols + ` FROM tracker_rows WHERE tracker_id=$1`
	args := []any{trackerID}
	if !includeGone { // missing and retired rows are history, not the live table
		sql += ` AND status='active'`
	}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		sql += fmt.Sprintf(` AND lower(data::text) LIKE $%d`, len(args))
	}
	args = append(args, limit)
	sql += fmt.Sprintf(` ORDER BY updated_at DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) rowByKey(ctx context.Context, trackerID int64, key string) (Row, error) {
	return scanRow(s.DB.QueryRow(ctx, `SELECT `+rowCols+` FROM tracker_rows WHERE tracker_id=$1 AND key=$2`, trackerID, key))
}

// UpsertRow adds a new row, or updates an existing one (matched by key) and returns exactly what changed.
// Submitting a field whose value is unchanged from what's stored produces no change entry — the caller (an
// agent that found the same listing again) is expected to only send fields it actually re-observed, not to
// resubmit everything defensively. Values of typed columns are checked and coerced; a new row must carry every
// required column. Each write is also an event (added / changed / reappeared) in the tracker's history, and
// alert conditions are settled on the result.
func (s *Service) UpsertRow(ctx context.Context, trackerID int64, key string, data map[string]any, sourceURL string) (Row, []Change, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Row{}, nil, errors.New("key is required")
	}
	if len(data) == 0 {
		return Row{}, nil, errors.New("data is required")
	}
	t, err := s.byID(ctx, trackerID)
	if err != nil {
		return Row{}, nil, err
	}
	row, changes, err := s.upsert(ctx, t, key, data, sourceURL, "")
	if err != nil {
		return Row{}, nil, err
	}
	s.evaluate(ctx, *t, []Row{row}, "")
	s.changed()
	return row, changes, nil
}

// upsert is one row's write inside its own transaction (a snapshot calls it per row inside the run's).
func (s *Service) upsert(ctx context.Context, t *Tracker, key string, data map[string]any, sourceURL, runID string) (Row, []Change, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Row{}, nil, err
	}
	defer tx.Rollback(ctx)
	row, changes, err := s.upsertTx(ctx, tx, t, key, data, sourceURL, runID)
	if err != nil {
		return Row{}, nil, err
	}
	return row, changes, tx.Commit(ctx)
}

func (s *Service) upsertTx(ctx context.Context, tx pgx.Tx, t *Tracker, key string, data map[string]any, sourceURL, runID string) (Row, []Change, error) {
	existing, err := scanRow(tx.QueryRow(ctx, `SELECT `+rowCols+` FROM tracker_rows WHERE tracker_id=$1 AND key=$2 FOR UPDATE`, t.ID, key))
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
		return Row{}, nil, err
	}
	data, err = normalizeData(t.Columns, data, isNew)
	if err != nil {
		return Row{}, nil, err
	}

	var changes []Change
	merged := map[string]any{}
	if !isNew {
		for k, v := range existing.Data {
			merged[k] = v
		}
		for k, v := range data {
			if ov, had := existing.Data[k]; !had || !jsonEqual(ov, v) {
				changes = append(changes, Change{Field: k, OldValue: strVal(existing.Data[k]), NewValue: strVal(v), Kind: "changed"})
			}
			merged[k] = v
		}
		if existing.Status != "active" {
			changes = append(changes, Change{Field: "status", OldValue: existing.Status, NewValue: "active", Kind: "reappeared"})
		}
	} else {
		merged = data
	}

	mb, err := json.Marshal(merged)
	if err != nil {
		return Row{}, nil, err
	}
	var row Row
	switch {
	case isNew:
		row, err = scanRow(tx.QueryRow(ctx, `INSERT INTO tracker_rows(tracker_id,key,data,source_url) VALUES($1,$2,$3,$4) RETURNING `+rowCols,
			t.ID, key, mb, sourceURL))
	case len(changes) == 0 && sourceURL == existing.SourceURL:
		// nothing actually changed: no updated_at churn, no events — just remember it was seen again
		row, err = scanRow(tx.QueryRow(ctx, `UPDATE tracker_rows SET last_seen=now() WHERE id=$1 RETURNING `+rowCols, existing.ID))
		return row, nil, err
	default:
		row, err = scanRow(tx.QueryRow(ctx, `UPDATE tracker_rows SET data=$3, source_url=$4, status='active', missing_since=NULL, updated_at=now(), last_seen=now() WHERE tracker_id=$1 AND key=$2 RETURNING `+rowCols,
			t.ID, key, mb, sourceURL))
	}
	if err != nil {
		return Row{}, nil, err
	}
	if isNew { // a new row is an event too: "what's new" must include it, and it has nothing to diff against
		if _, err := tx.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value,kind,run_id) VALUES($1,$2,$3,'status','','new','added',$4)`,
			t.ID, row.ID, key, runID); err != nil {
			return Row{}, nil, err
		}
	}
	for i := range changes {
		changes[i].TrackerID, changes[i].RowID, changes[i].RowKey = t.ID, row.ID, key
		if _, err := tx.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value,kind,run_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			t.ID, row.ID, key, changes[i].Field, changes[i].OldValue, changes[i].NewValue, changes[i].Kind, runID); err != nil {
			return Row{}, nil, err
		}
	}
	return row, changes, nil
}

// RetireRow marks a row no longer active (e.g. a listing was taken down) — itself a meaningful change,
// recorded in the tracker's history like any other. A row already retired is left as-is (no duplicate entry).
func (s *Service) RetireRow(ctx context.Context, trackerID int64, key, reason string) (Row, error) {
	row, err := s.rowByKey(ctx, trackerID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Row{}, fmt.Errorf("no row %q in this tracker", key)
	}
	if err != nil {
		return Row{}, err
	}
	if row.Status == "gone" {
		return row, nil
	}
	upd, err := scanRow(s.DB.QueryRow(ctx, `UPDATE tracker_rows SET status='gone', missing_since=NULL, updated_at=now() WHERE id=$1 RETURNING `+rowCols, row.ID))
	if err != nil {
		return Row{}, err
	}
	nv := "gone"
	if reason = strings.TrimSpace(reason); reason != "" {
		nv = "gone: " + reason
	}
	_, _ = s.DB.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value,kind) VALUES($1,$2,$3,'status',$5,$4,'retired')`,
		trackerID, row.ID, key, nv, row.Status)
	s.changed()
	return upd, nil
}

// Changes reports what changed since t, newest first — the "what's new" a refresh reports back. trackerID
// 0 reports across every tracker.
func (s *Service) Changes(ctx context.Context, trackerID int64, since time.Time, limit int) ([]Change, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT id,tracker_id,row_id,row_key,field,old_value,new_value,kind,run_id,created_at FROM tracker_changes
		WHERE ($1=0 OR tracker_id=$1) AND created_at>=$2 ORDER BY id DESC LIMIT $3`, trackerID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ID, &c.TrackerID, &c.RowID, &c.RowKey, &c.Field, &c.OldValue, &c.NewValue, &c.Kind, &c.RunID, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteRow removes one row and its change history outright (manual cleanup — e.g. a row added by mistake;
// use RetireRow instead when a tracked thing genuinely disappeared and should stay in the history).
func (s *Service) DeleteRow(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM tracker_rows WHERE id=$1`, id)
	if err == nil {
		s.changed()
	}
	return err
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func strVal(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
