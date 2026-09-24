// Package notify stores user notifications (a cron fired, an agent needs you, something failed
// for good) and their per-kind preferences.
package notify

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Item struct {
	ID    int64     `json:"id"`
	TS    time.Time `json:"ts"`
	Kind  string    `json:"kind"`
	Level string    `json:"level"`
	Title string    `json:"title"`
	Text  string    `json:"text"`
	Read  bool      `json:"read"`
	Ref   string    `json:"ref"`
}

type Store struct{ DB *pgxpool.Pool }

func (s *Store) Add(ctx context.Context, it Item) (Item, error) {
	err := s.DB.QueryRow(ctx, `INSERT INTO notifications(kind,level,title,text,ref) VALUES($1,$2,$3,$4,$5) RETURNING id,ts`,
		it.Kind, it.Level, it.Title, it.Text, it.Ref).Scan(&it.ID, &it.TS)
	return it, err
}

func (s *Store) List(ctx context.Context, limit int) ([]Item, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT id,ts,kind,level,title,text,read,ref FROM notifications ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.TS, &it.Kind, &it.Level, &it.Title, &it.Text, &it.Read, &it.Ref); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	var unread int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE NOT read`).Scan(&unread)
	return out, unread, rows.Err()
}

// MarkRead marks one notification (id>0) or all of them (id==0) as read.
func (s *Store) MarkRead(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE notifications SET read=true WHERE ($1=0 OR id=$1)`, id)
	return err
}

// MarkReadRef marks the notifications that point at ref as read and returns how many changed.
func (s *Store) MarkReadRef(ctx context.Context, ref string) (int, error) {
	t, err := s.DB.Exec(ctx, `UPDATE notifications SET read=true WHERE ref=$1 AND NOT read`, ref)
	return int(t.RowsAffected()), err
}

func (s *Store) Clear(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM notifications`)
	return err
}

// Prune deletes notifications older than d and keeps at most 500.
func (s *Store) Prune(ctx context.Context, d time.Duration) (int, error) {
	t1, err := s.DB.Exec(ctx, `DELETE FROM notifications WHERE ts < $1`, time.Now().Add(-d))
	if err != nil {
		return 0, err
	}
	t2, err := s.DB.Exec(ctx, `DELETE FROM notifications WHERE id < (SELECT COALESCE(max(id),0)-500 FROM notifications)`)
	return int(t1.RowsAffected() + t2.RowsAffected()), err
}
