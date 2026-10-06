package builtin

import (
	"context"
	"testing"

	"prism/internal/testutil"
)

// A download still "running" in the database after a restart has no goroutine any more: it must not look alive.
func TestDownloadsReconcileMarksLostOnesFailed(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	dl := &Downloader{d: Deps{DB: d.Pool}, cancels: map[int64]context.CancelFunc{}}
	for _, st := range []string{"running", "queued", "done"} {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO downloads(url,dest,status) VALUES('u','/tmp/x',$1)`, st); err != nil {
			t.Fatal(err)
		}
	}
	n, err := dl.Reconcile(ctx)
	if err != nil || n != 2 {
		t.Fatalf("reconciled %d, %v", n, err)
	}
	var failed, done int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='failed' AND error<>''), count(*) FILTER (WHERE status='done') FROM downloads`).Scan(&failed, &done)
	if failed != 2 || done != 1 {
		t.Fatalf("failed=%d done=%d", failed, done)
	}
}
