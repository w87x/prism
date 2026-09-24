// Package search is a single fan-out query across everything PRISM already stores in Postgres — memory
// facts, trackers, knowledge base pages and tasks — for a command-palette-style universal search. It
// reads existing tables directly rather than building its own index: nothing here is a source of truth,
// so there is nothing to keep in sync.
package search

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const perCategory = 8

type Result struct {
	Kind    string `json:"kind"` // memory | tracker | knowledge | task
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Extra   string `json:"extra,omitempty"` // bank label, tracker name, agent name — category-specific context
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// All searches every category and returns them grouped, category-relevance first within each.
func (s *Service) All(ctx context.Context, q string) ([]Result, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []Result{}, nil
	}
	out := []Result{}
	like := "%" + strings.NewReplacer("%", `\%`, "_", `\_`).Replace(q) + "%"

	rows, err := s.db.Query(ctx, `SELECT f.id, left(f.text,160), b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END
		FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.valid_to IS NULL AND f.tsv @@ plainto_tsquery('simple', $1)
		ORDER BY ts_rank(f.tsv, plainto_tsquery('simple', $1)) DESC LIMIT $2`, q, perCategory)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r Result
		r.Kind = "memory"
		if err := rows.Scan(&r.ID, &r.Snippet, &r.Extra); err != nil {
			rows.Close()
			return nil, err
		}
		r.Title = r.Snippet
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trows, err := s.db.Query(ctx, `SELECT id, name, description FROM trackers
		WHERE status='active' AND (name ILIKE $1 OR description ILIKE $1) ORDER BY name LIMIT $2`, like, perCategory)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var r Result
		r.Kind = "tracker"
		if err := trows.Scan(&r.ID, &r.Title, &r.Snippet); err != nil {
			trows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, err
	}

	krows, err := s.db.Query(ctx, `SELECT id, title, left(body,160) FROM kb_pages
		WHERE title ILIKE $1 OR body ILIKE $1 ORDER BY updated_at DESC LIMIT $2`, like, perCategory)
	if err != nil {
		return nil, err
	}
	for krows.Next() {
		var r Result
		r.Kind = "knowledge"
		if err := krows.Scan(&r.ID, &r.Title, &r.Snippet); err != nil {
			krows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	krows.Close()
	if err := krows.Err(); err != nil {
		return nil, err
	}

	tkrows, err := s.db.Query(ctx, `SELECT id, title, to_agent, status FROM tasks
		WHERE title ILIKE $1 ORDER BY created_at DESC LIMIT $2`, like, perCategory)
	if err != nil {
		return nil, err
	}
	for tkrows.Next() {
		var r Result
		r.Kind = "task"
		if err := tkrows.Scan(&r.ID, &r.Title, &r.Extra, &r.Snippet); err != nil {
			tkrows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	tkrows.Close()
	return out, tkrows.Err()
}
