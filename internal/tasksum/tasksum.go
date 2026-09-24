// Package tasksum stores and searches task summaries: what a finished task's goal was, what was decided
// and tried (including approaches that failed or were abandoned), how it came out, and what was left
// undone. This answers "what did we try last week, and why did we reject it?" — a question a single atomic
// memory fact (internal/memory) cannot reliably hold, since it is one self-contained sentence, not a
// narrative. Each summary links back to its task (task_id), and from there to the task's full transcript
// via the task_transcript tool.
package tasksum

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/textmatch"
)

type Summary struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	Agent      string    `json:"agent"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Goal       string    `json:"goal"`
	Decisions  string    `json:"decisions"`
	Attempts   string    `json:"attempts"`
	Outcome    string    `json:"outcome"`
	Unfinished string    `json:"unfinished"`
	CreatedAt  time.Time `json:"created_at"`
}

type Store struct{ DB *pgxpool.Pool }

const cols = `id,task_id,agent,title,status,goal,decisions,attempts,outcome,unfinished,created_at`

func scan(r pgx.Row) (Summary, error) {
	var s Summary
	err := r.Scan(&s.ID, &s.TaskID, &s.Agent, &s.Title, &s.Status, &s.Goal, &s.Decisions, &s.Attempts, &s.Outcome, &s.Unfinished, &s.CreatedAt)
	return s, err
}

// Save stores a task's summary, replacing any earlier one for the same task (one row per task).
func (s *Store) Save(ctx context.Context, sum Summary) (Summary, error) {
	return scan(s.DB.QueryRow(ctx, `INSERT INTO task_summaries(task_id,agent,title,status,goal,decisions,attempts,outcome,unfinished)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (task_id) DO UPDATE SET agent=$2,title=$3,status=$4,goal=$5,decisions=$6,attempts=$7,outcome=$8,unfinished=$9
		RETURNING `+cols, sum.TaskID, sum.Agent, sum.Title, sum.Status, sum.Goal, sum.Decisions, sum.Attempts, sum.Outcome, sum.Unfinished))
}

// Get returns the summary for a task, or pgx.ErrNoRows if it has none (yet, or was skipped as trivial).
func (s *Store) Get(ctx context.Context, taskID int64) (*Summary, error) {
	sm, err := scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM task_summaries WHERE task_id=$1`, taskID))
	if err != nil {
		return nil, err
	}
	return &sm, nil
}

func (s *Store) List(ctx context.Context, limit int) ([]Summary, error) {
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM task_summaries ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		sm, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// Find ranks summaries against query with BM25 (see internal/textmatch — the same scorer bookmarks use), no
// popularity weighting: a summary's usefulness does not fade for having been looked up rarely, and a task
// tried once is not "more correct" for being cited often. Ties keep List's most-recent-first order.
func (s *Store) Find(ctx context.Context, query string, limit int) ([]Summary, error) {
	if limit <= 0 {
		limit = 5
	}
	all, err := s.List(ctx, 2000)
	if err != nil {
		return nil, err
	}
	docs := make([]string, len(all))
	for i, sm := range all {
		docs[i] = strings.Join([]string{sm.Title, sm.Goal, sm.Decisions, sm.Attempts, sm.Outcome, sm.Unfinished}, "\n")
	}
	hits := textmatch.Rank(query, docs, limit)
	out := make([]Summary, len(hits))
	for i, h := range hits {
		out[i] = all[h.Index]
	}
	return out, nil
}
