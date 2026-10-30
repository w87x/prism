package scheduler

import (
	"context"
	"testing"
)

// A schedule made before day-field mixing was refused is reported once, not rewritten.
func TestWarnDayFieldsOnceAndLeavesTheCronAlone(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	var told []string
	s.Notify = func(kind, level, title, text string) { told = append(told, text) }
	if _, err := s.DB.Exec(ctx, `INSERT INTO crons(name,agent,expr,prompt,enabled) VALUES('Premiere','Lyra','0 12 20,21,22 * 1','x',true),('Daily','Lyra','0 9 * * *','y',true)`); err != nil {
		t.Fatal(err)
	}
	s.warnDayFields(ctx)
	s.warnDayFields(ctx)
	if len(told) != 1 {
		t.Fatalf("want one warning for the mixed schedule only, got %d: %v", len(told), told)
	}
	var expr string
	_ = s.DB.QueryRow(ctx, `SELECT expr FROM crons WHERE name='Premiere'`).Scan(&expr)
	if expr != "0 12 20,21,22 * 1" {
		t.Fatalf("the expression must stay as it was: %q", expr)
	}
}
