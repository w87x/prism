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

// Bug: Reset listed every table this build knows; on a database from an older schema (a table added since) the
// TRUNCATE failed on the first missing name and "start over" did nothing.
func TestResetToleratesTablesTheDatabaseDoesNotHaveYet(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	for _, tbl := range []string{"memory_audit", "tracker_runs"} {
		if _, err := d.Pool.Exec(ctx, `DROP TABLE `+tbl+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES('keepme','{}') ON CONFLICT (key) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO bookmarks(url,title) VALUES('https://x','x')`); err != nil {
		t.Skipf("fixture column mismatch: %v", err)
	}
	if err := Reset(ctx, d.Pool, ScopeData, t.TempDir()); err != nil {
		t.Fatalf("reset on an older schema: %v", err)
	}
	var n int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM bookmarks`).Scan(&n)
	var kept int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key='keepme'`).Scan(&kept)
	if n != 0 || kept != 1 {
		t.Fatalf("produced data must go (%d left) and settings stay (%d)", n, kept)
	}
}

// A reset wipes EVERY table that is not configuration, including ones added after the reset code was written: task_meta survived one, the
// task ids restarted, and new tasks were judged against old criteria.
func TestResetWipesEveryProducedTableIncludingNewOnes(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO antibodies(agent,triggers,rule,source) VALUES('Mira','{x}','a lesson that must go','t')`,
		`INSERT INTO skill_uses(skill,agent,task_id,how) VALUES('s','Mira',1,'injected')`,
		`INSERT INTO tool_approvals(tool,agent,task_id,allowed) VALUES('t','Mira',1,true)`,
		`INSERT INTO runbooks(key,agent,title,steps) VALUES('cron:x','Mira','t','1. step')`,
		`UPDATE core_blocks SET text='about me' WHERE label='about_user'`,
		`INSERT INTO chats(topic,title,auto_title) VALUES('side','Side',false)`,
		`INSERT INTO settings(key,value) VALUES('general','{}') ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO settings(key,value) VALUES('harvest.bookmarks','{"last_id":23138}') ON CONFLICT (key) DO NOTHING`,
	} {
		if _, err := d.Exec(ctx, q); err != nil {
			t.Fatalf("fixture does not match the schema: %v (%s)", err, q)
		}
	}
	if err := Reset(ctx, d.Pool, ScopeData, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int { var n int; _ = d.QueryRow(ctx, q).Scan(&n); return n }
	for _, tbl := range []string{"antibodies", "skill_uses", "tool_approvals", "runbooks", "task_meta"} {
		if n := count(`SELECT count(*) FROM ` + tbl); n != 0 {
			t.Errorf("%s still holds %d rows after a reset", tbl, n)
		}
	}
	if count(`SELECT count(*) FROM core_blocks WHERE label='about_user' AND text=''`) != 1 || count(`SELECT count(*) FROM core_blocks`) != 3 {
		t.Error("core memory keeps its default blocks, emptied")
	}
	if count(`SELECT count(*) FROM chats WHERE topic=''`) != 1 || count(`SELECT count(*) FROM chats`) != 1 {
		t.Error("only the Main chat stays")
	}
	if count(`SELECT count(*) FROM settings WHERE key='harvest.bookmarks'`) != 0 {
		t.Error("a setting that remembers an id must go with the data: the ids start again")
	}
	if count(`SELECT count(*) FROM memory_predicates`) == 0 || count(`SELECT count(*) FROM schema_migrations`) == 0 || count(`SELECT count(*) FROM settings`) == 0 {
		t.Error("seeded rules, the migration record and the settings must stay")
	}
}
