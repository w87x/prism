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

type Column struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Tracker struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Columns     []Column  `json:"columns"`
	CreatedBy   string    `json:"created_by"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	Rows        int       `json:"rows"`
}

type Row struct {
	ID        int64          `json:"id"`
	TrackerID int64          `json:"tracker_id"`
	Key       string         `json:"key"`
	Data      map[string]any `json:"data"`
	SourceURL string         `json:"source_url"`
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type Change struct {
	ID        int64     `json:"id"`
	TrackerID int64     `json:"tracker_id"`
	RowID     int64     `json:"row_id"`
	RowKey    string    `json:"row_key"`
	Field     string    `json:"field"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	DB *pgxpool.Pool
	// OnChange notifies the UI after a tracker, row or change is written.
	OnChange func()
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

const trackerCols = `id,name,description,columns,created_by,status,created_at`

func scanTracker(r pgx.Row) (Tracker, error) {
	var t Tracker
	var cb []byte
	if err := r.Scan(&t.ID, &t.Name, &t.Description, &cb, &t.CreatedBy, &t.Status, &t.CreatedAt); err != nil {
		return t, err
	}
	_ = json.Unmarshal(cb, &t.Columns)
	return t, nil
}

const rowCols = `id,tracker_id,key,data,source_url,status,created_at,updated_at`

func scanRow(r pgx.Row) (Row, error) {
	var row Row
	var db []byte
	if err := r.Scan(&row.ID, &row.TrackerID, &row.Key, &db, &row.SourceURL, &row.Status, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return row, err
	}
	_ = json.Unmarshal(db, &row.Data)
	if row.Data == nil {
		row.Data = map[string]any{}
	}
	return row, nil
}

// Create defines a new tracker. Columns are documentation for the agents that populate it (a hint, not an
// enforced schema — tracker_row_upsert accepts any fields), so a later column added by convention doesn't
// need a migration.
func (s *Service) Create(ctx context.Context, name, description string, cols []Column, by string) (*Tracker, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if len(cols) == 0 {
		return nil, errors.New("at least one column is required")
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
		var cb []byte
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &cb, &t.CreatedBy, &t.Status, &t.CreatedAt, &t.Rows); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cb, &t.Columns)
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
	if !includeGone {
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
// resubmit everything defensively.
func (s *Service) UpsertRow(ctx context.Context, trackerID int64, key string, data map[string]any, sourceURL string) (Row, []Change, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Row{}, nil, errors.New("key is required")
	}
	if len(data) == 0 {
		return Row{}, nil, errors.New("data is required")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Row{}, nil, err
	}
	defer tx.Rollback(ctx)

	existing, err := scanRow(tx.QueryRow(ctx, `SELECT `+rowCols+` FROM tracker_rows WHERE tracker_id=$1 AND key=$2 FOR UPDATE`, trackerID, key))
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
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
				changes = append(changes, Change{Field: k, OldValue: strVal(existing.Data[k]), NewValue: strVal(v)})
			}
			merged[k] = v
		}
		if existing.Status == "gone" {
			changes = append(changes, Change{Field: "status", OldValue: "gone", NewValue: "active"})
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
			trackerID, key, mb, sourceURL))
	case len(changes) == 0 && sourceURL == existing.SourceURL:
		// nothing actually changed: leave the row as-is (no pointless updated_at churn) and report no changes
		return existing, nil, tx.Commit(ctx)
	default:
		row, err = scanRow(tx.QueryRow(ctx, `UPDATE tracker_rows SET data=$3, source_url=$4, status='active', updated_at=now() WHERE tracker_id=$1 AND key=$2 RETURNING `+rowCols,
			trackerID, key, mb, sourceURL))
	}
	if err != nil {
		return Row{}, nil, err
	}
	for i := range changes {
		changes[i].TrackerID, changes[i].RowID, changes[i].RowKey = trackerID, row.ID, key
		if _, err := tx.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value) VALUES($1,$2,$3,$4,$5,$6)`,
			trackerID, row.ID, key, changes[i].Field, changes[i].OldValue, changes[i].NewValue); err != nil {
			return Row{}, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Row{}, nil, err
	}
	s.changed()
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
	upd, err := scanRow(s.DB.QueryRow(ctx, `UPDATE tracker_rows SET status='gone', updated_at=now() WHERE id=$1 RETURNING `+rowCols, row.ID))
	if err != nil {
		return Row{}, err
	}
	nv := "gone"
	if reason = strings.TrimSpace(reason); reason != "" {
		nv = "gone: " + reason
	}
	_, _ = s.DB.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value) VALUES($1,$2,$3,'status','active',$4)`,
		trackerID, row.ID, key, nv)
	s.changed()
	return upd, nil
}

// Changes reports what changed since t, newest first — the "what's new" a refresh reports back. trackerID
// 0 reports across every tracker.
func (s *Service) Changes(ctx context.Context, trackerID int64, since time.Time, limit int) ([]Change, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT id,tracker_id,row_id,row_key,field,old_value,new_value,created_at FROM tracker_changes
		WHERE ($1=0 OR tracker_id=$1) AND created_at>=$2 ORDER BY id DESC LIMIT $3`, trackerID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ID, &c.TrackerID, &c.RowID, &c.RowKey, &c.Field, &c.OldValue, &c.NewValue, &c.CreatedAt); err != nil {
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
