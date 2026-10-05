package tracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── snapshots: one refresh as one atomic, idempotent run (memo: complete vs partial snapshots, run receipts) ──
//
// A refresh is a batch of what was observed, not a trickle of upserts:
//   - COMPLETE says "this is everything the source lists". Only then may a row that is absent be marked MISSING —
//     and missing means "not observed in a complete snapshot", never "no longer exists" (the row keeps its data
//     and returns, with an event, if it shows up again).
//   - PARTIAL (a page of results, a search window) only adds and updates; an absent row is left alone.
//   - The run id makes a retry safe: the same run id with the same rows returns the stored receipt; the same run id
//     with different rows is refused. Nothing is half-applied: a bad row fails the whole run.

const maxSnapshotRows = 500

// Observed is one row of a snapshot.
type Observed struct {
	Key       string         `json:"key"`
	Data      map[string]any `json:"data"`
	SourceURL string         `json:"source_url,omitempty"`
}

// Receipt reports what a run did.
type Receipt struct {
	RunID      string    `json:"run_id"`
	Replayed   bool      `json:"replayed,omitempty"` // the run id had been applied before: nothing was done again
	Complete   bool      `json:"complete"`
	Observed   int       `json:"observed"`
	Added      int       `json:"added"`
	Changed    int       `json:"changed"`
	Reappeared int       `json:"reappeared"`
	Unchanged  int       `json:"unchanged"`
	Missing    int       `json:"missing"` // rows marked missing by this run (complete snapshots only)
	At         time.Time `json:"at"`
}

func fingerprint(obs []Observed, complete bool) string {
	cp := append([]Observed(nil), obs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Key < cp[j].Key })
	b, _ := json.Marshal(struct {
		Rows     []Observed `json:"rows"`
		Complete bool       `json:"complete"`
	}{cp, complete})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Commit applies a snapshot. runID must be unique per refresh (a timestamp or a short random id is fine).
func (s *Service) Commit(ctx context.Context, trackerID int64, runID string, obs []Observed, complete bool) (*Receipt, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errors.New("a run id is required (any unique text, e.g. the date and time of this refresh)")
	}
	if len(obs) > maxSnapshotRows {
		return nil, fmt.Errorf("at most %d rows per snapshot: split a bigger source into partial snapshots (complete=false)", maxSnapshotRows)
	}
	t, err := s.byID(ctx, trackerID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range obs {
		obs[i].Key = strings.TrimSpace(obs[i].Key)
		if obs[i].Key == "" {
			return nil, fmt.Errorf("row %d has no key: use the source's own stable id (a URL, a repository id), not a title", i+1)
		}
		if seen[obs[i].Key] {
			return nil, fmt.Errorf("key %q appears twice in this snapshot", obs[i].Key)
		}
		seen[obs[i].Key] = true
		if len(obs[i].Data) == 0 {
			return nil, fmt.Errorf("row %q has no data", obs[i].Key)
		}
	}
	fp := fingerprint(obs, complete)

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// serialise refreshes of one tracker: two concurrent runs must not interleave their diffs
	if _, err := tx.Exec(ctx, `SELECT 1 FROM trackers WHERE id=$1 FOR UPDATE`, t.ID); err != nil {
		return nil, err
	}
	var prevFP string
	var prevReceipt []byte
	switch err := tx.QueryRow(ctx, `SELECT fingerprint, receipt FROM tracker_runs WHERE tracker_id=$1 AND run_id=$2`, t.ID, runID).Scan(&prevFP, &prevReceipt); {
	case err == nil:
		if prevFP != fp {
			return nil, fmt.Errorf("run id %q was already used for a different snapshot: use a new run id for a new refresh", runID)
		}
		var r Receipt
		_ = json.Unmarshal(prevReceipt, &r)
		r.Replayed = true
		return &r, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	rec := &Receipt{RunID: runID, Complete: complete, Observed: len(obs), At: time.Now()}
	var touched []Row
	for _, o := range obs {
		var existed bool
		var oldStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM tracker_rows WHERE tracker_id=$1 AND key=$2`, t.ID, o.Key).Scan(&oldStatus); err == nil {
			existed = true
		}
		row, changes, err := s.upsertTx(ctx, tx, t, o.Key, o.Data, o.SourceURL, runID)
		if err != nil {
			return nil, fmt.Errorf("row %q: %w", o.Key, err)
		}
		switch {
		case !existed:
			rec.Added++
			touched = append(touched, row)
		case oldStatus != "active":
			rec.Reappeared++
			touched = append(touched, row)
		case len(changes) > 0:
			rec.Changed++
			touched = append(touched, row)
		default:
			rec.Unchanged++
		}
	}
	if complete {
		rows, err := tx.Query(ctx, `SELECT id,key FROM tracker_rows WHERE tracker_id=$1 AND status='active'`, t.ID)
		if err != nil {
			return nil, err
		}
		type gone struct {
			id  int64
			key string
		}
		var absent []gone
		for rows.Next() {
			var g gone
			if err := rows.Scan(&g.id, &g.key); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[g.key] {
				absent = append(absent, g)
			}
		}
		rows.Close()
		for _, g := range absent {
			if _, err := tx.Exec(ctx, `UPDATE tracker_rows SET status='missing', missing_since=now() WHERE id=$1`, g.id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO tracker_changes(tracker_id,row_id,row_key,field,old_value,new_value,kind,run_id) VALUES($1,$2,$3,'status','active','missing','missing',$4)`,
				t.ID, g.id, g.key, runID); err != nil {
				return nil, err
			}
			rec.Missing++
		}
	}
	rb, _ := json.Marshal(rec)
	if _, err := tx.Exec(ctx, `INSERT INTO tracker_runs(tracker_id,run_id,fingerprint,complete,receipt) VALUES($1,$2,$3,$4,$5)`, t.ID, runID, fp, complete, rb); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.evaluate(ctx, *t, touched, runID)
	s.changed()
	return rec, nil
}

// Runs lists a tracker's refreshes, newest first.
func (s *Service) Runs(ctx context.Context, trackerID int64, limit int) ([]Receipt, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, `SELECT receipt FROM tracker_runs WHERE tracker_id=$1 ORDER BY id DESC LIMIT $2`, trackerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Receipt{}
	for rows.Next() {
		var b []byte
		var r Receipt
		if rows.Scan(&b) == nil && json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}
