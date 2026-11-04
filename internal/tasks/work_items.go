package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// WorkItem is a draft plan item. A linked task supplies its status; an unlinked item is not assumed complete.
type WorkItem struct {
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	DoneWhen   string   `json:"done_when"`
	DependsOn  []string `json:"depends_on"`
	DelegateID *int64   `json:"delegate_id,omitempty"`
	Status     string   `json:"status"`
}

func (s *Store) SaveWorkItems(ctx context.Context, taskID int64, items []WorkItem) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range items {
		depends := item.DependsOn
		if depends == nil {
			depends = []string{}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO work_items(task_id,item_no,title,done_when,depends_on) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, taskID, item.Number, item.Title, item.DoneWhen, depends); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) WorkItems(ctx context.Context, taskID int64) ([]WorkItem, error) {
	rows, err := s.db.Query(ctx, `SELECT w.item_no,w.title,w.done_when,w.depends_on,w.delegate_id,COALESCE(t.status,'unassigned') FROM work_items w LEFT JOIN tasks t ON t.id=w.delegate_id WHERE w.task_id=$1 ORDER BY w.item_no`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkItem
	for rows.Next() {
		var item WorkItem
		if err := rows.Scan(&item.Number, &item.Title, &item.DoneWhen, &item.DependsOn, &item.DelegateID, &item.Status); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// LinkWorkItems links only the caller's own plan to a direct child. An abandoned waiting task may be replaced;
// a running or successfully completed task cannot be silently displaced.
func (s *Store) LinkWorkItems(ctx context.Context, rootID, childID int64, items []int) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var replaced []int64
	for _, item := range items {
		var previous *int64
		if err := tx.QueryRow(ctx, `SELECT delegate_id FROM work_items WHERE task_id=$1 AND item_no=$2 FOR UPDATE`, rootID, item).Scan(&previous); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("plan item %d does not exist", item)
			}
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE work_items SET delegate_id=$3 WHERE task_id=$1 AND item_no=$2 AND (delegate_id IS NULL OR EXISTS (SELECT 1 FROM tasks old WHERE old.id=delegate_id AND old.status IN ('partial','failed','cancelled','waiting_input'))) AND EXISTS (SELECT 1 FROM tasks WHERE id=$3 AND parent_id=$1)`, rootID, item, childID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("plan item %d does not exist or is already assigned", item)
		}
		if previous != nil {
			replaced = append(replaced, *previous)
		}
	}
	var cancelled []int64
	for _, id := range replaced {
		tag, err := tx.Exec(ctx, `UPDATE tasks SET status='cancelled',error='Reassigned to another task',question='',finished_at=now() WHERE id=$1 AND status='waiting_input' AND NOT EXISTS (SELECT 1 FROM work_items WHERE delegate_id=$1)`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			cancelled = append(cancelled, id)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range cancelled {
		if t, err := s.Get(ctx, id); err == nil {
			s.notify(t)
		}
	}
	return nil
}
