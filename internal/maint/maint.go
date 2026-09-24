// Package maint applies the retention (TTL) settings: old logs, finished tasks and the
// agent sessions that belonged to them.
package maint

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Result struct {
	Logs     int `json:"logs"`
	Tasks    int `json:"tasks"`
	Sessions int `json:"sessions"`
}

// Cleanup removes logs older than logsDays and finished tasks older than tasksDays (0 keeps forever),
// then the task sessions that no longer have a task.
// statuses limits which finished tasks are removed (e.g. done, failed, cancelled, partial); none = every finished task.
func Cleanup(ctx context.Context, db *pgxpool.Pool, logsDays, tasksDays int, statuses ...string) (Result, error) {
	var r Result
	if logsDays > 0 {
		t, err := db.Exec(ctx, `DELETE FROM logs WHERE ts < $1`, time.Now().AddDate(0, 0, -logsDays))
		if err != nil {
			return r, err
		}
		r.Logs = int(t.RowsAffected())
	}
	if tasksDays > 0 {
		t, err := db.Exec(ctx, `DELETE FROM tasks WHERE finished_at IS NOT NULL AND finished_at < $1 AND (COALESCE(cardinality($2::text[]),0)=0 OR status=ANY($2))`, time.Now().AddDate(0, 0, -tasksDays), statuses)
		if err != nil {
			return r, err
		}
		r.Tasks = int(t.RowsAffected())
		t, err = db.Exec(ctx, `DELETE FROM sessions WHERE kind='task' AND (task_id IS NULL OR task_id NOT IN (SELECT id FROM tasks))`)
		if err != nil {
			return r, err
		}
		r.Sessions = int(t.RowsAffected())
	}
	return r, nil
}
