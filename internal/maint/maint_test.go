package maint

import (
	"context"
	"testing"
	"time"

	"prism/internal/notify"
	"prism/internal/testutil"
)

func TestCleanupHonoursTTLs(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	_, _ = d.Exec(ctx, `INSERT INTO logs(ts,level,source,message) VALUES (now()-interval '20 days','info','t','old'),(now(),'info','t','new')`)
	_, _ = d.Exec(ctx, `INSERT INTO tasks(from_kind,to_agent,input,status,finished_at) VALUES
		('user','A','old','done',now()-interval '40 days'),
		('user','A','recent','done',now()-interval '2 days'),
		('user','A','running','running',NULL)`)
	var oldTask int64
	_ = d.QueryRow(ctx, `SELECT id FROM tasks WHERE input='old'`).Scan(&oldTask)
	_, _ = d.Exec(ctx, `INSERT INTO sessions(agent,kind,task_id) VALUES ('A','task',$1),('Atlas','chat',NULL)`, oldTask)

	r, err := Cleanup(ctx, d.Pool, 14, 30)
	if err != nil || r.Logs != 1 || r.Tasks != 1 || r.Sessions != 1 {
		t.Fatalf("cleanup: %+v %v", r, err)
	}
	var n int
	_ = d.QueryRow(ctx, `SELECT count(*) FROM tasks`).Scan(&n)
	var chat int
	_ = d.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE kind='chat'`).Scan(&chat)
	if n != 2 || chat != 1 {
		t.Fatalf("tasks left %d, chat sessions %d (running/recent tasks and chat sessions must survive)", n, chat)
	}
	// zero means keep forever
	_, _ = d.Exec(ctx, `INSERT INTO logs(ts,level,source,message) VALUES (now()-interval '900 days','info','t','ancient')`)
	if r, _ := Cleanup(ctx, d.Pool, 0, 0); r.Logs != 0 || r.Tasks != 0 {
		t.Fatalf("0 must disable cleanup: %+v", r)
	}
}

func TestNotificationStore(t *testing.T) {
	d := testutil.DB(t)
	s := &notify.Store{DB: d.Pool}
	ctx := context.Background()
	a, _ := s.Add(ctx, notify.Item{Kind: "cron", Level: "info", Title: "Schedule fired", Text: "x"})
	_, _ = s.Add(ctx, notify.Item{Kind: "error", Level: "error", Title: "Scout failed"})
	items, unread, _ := s.List(ctx, 10)
	if len(items) != 2 || unread != 2 || items[0].Kind != "error" {
		t.Fatalf("list: %+v unread=%d", items, unread)
	}
	_ = s.MarkRead(ctx, a.ID)
	if _, unread, _ = s.List(ctx, 10); unread != 1 {
		t.Fatalf("unread %d", unread)
	}
	_ = s.MarkRead(ctx, 0)
	if _, unread, _ = s.List(ctx, 10); unread != 0 {
		t.Fatal("mark all read")
	}
	_, _ = d.Exec(ctx, `UPDATE notifications SET ts = $1`, time.Now().Add(-40*24*time.Hour))
	if n, _ := s.Prune(ctx, 30*24*time.Hour); n != 2 {
		t.Fatalf("prune %d", n)
	}
}

// The task cleanup can be limited to chosen statuses, so e.g. resumable "partial" tasks survive a prune
// that only targets done/failed/cancelled.
func TestCleanupHonoursTaskStatuses(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	_, _ = d.Exec(ctx, `INSERT INTO tasks(from_kind,to_agent,input,status,finished_at) VALUES
		('user','A','d','done',now()-interval '40 days'),
		('user','A','p','partial',now()-interval '40 days'),
		('user','A','f','failed',now()-interval '40 days')`)
	r, err := Cleanup(ctx, d.Pool, 0, 30, "done", "failed")
	if err != nil || r.Tasks != 2 {
		t.Fatalf("cleanup: %+v %v", r, err)
	}
	var left string
	_ = d.QueryRow(ctx, `SELECT status FROM tasks`).Scan(&left)
	if left != "partial" {
		t.Fatalf("left %q, want partial", left)
	}
}

// Reset("data") wipes what PRISM produced but keeps API keys/integrations; Reset("all") wipes those too.
func TestResetKeepsIntegrationsUnlessAll(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	seed := func() {
		_, _ = d.Exec(ctx, `INSERT INTO settings(key,value) VALUES('general','{"user_name":"D"}') ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`)
		_, _ = d.Exec(ctx, `INSERT INTO providers(name,kind,base_url) VALUES('LM','lmstudio','http://x/v1') ON CONFLICT DO NOTHING`)
		_, _ = d.Exec(ctx, `INSERT INTO tasks(from_kind,to_agent,input,root_id) VALUES('user','A','x',1)`)
		_, _ = d.Exec(ctx, `INSERT INTO briefings(agent,title,body) VALUES('O','t','b')`)
		_, _ = d.Exec(ctx, `INSERT INTO memory_banks(kind,name) VALUES('user','user') ON CONFLICT DO NOTHING`)
	}
	count := func(tbl string) int { var n int; _ = d.QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); return n }
	seed()
	if err := Reset(ctx, d.Pool, ScopeData, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if count("tasks") != 0 || count("briefings") != 0 || count("memory_banks") != 0 {
		t.Fatal("produced data must be wiped")
	}
	if count("settings") == 0 || count("providers") == 0 {
		t.Fatal("scope data must keep settings and providers")
	}
	seed()
	if err := Reset(ctx, d.Pool, ScopeAll, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if count("settings") != 0 || count("providers") != 0 || count("tasks") != 0 {
		t.Fatal("scope all must wipe integrations too")
	}
	if err := Reset(ctx, d.Pool, "bogus", ""); err == nil {
		t.Fatal("unknown scope must be rejected")
	}
}
