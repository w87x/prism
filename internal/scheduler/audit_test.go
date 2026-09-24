package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/tasks"
)

// TestAuditMergesTaskIntentAndBriefingSources exercises all four audit kinds together (cron task, intent
// task, watch firing, dream briefing), since Audit stitches them from three unrelated tables and a bug
// merging or sorting them could easily drop or misdate one source without the others showing it.
func TestAuditMergesTaskIntentAndBriefingSources(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)

	now := time.Now()
	var cronTaskID, intentTaskID int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO tasks(from_kind,from_name,to_agent,title,input,status,result,created_at)
		VALUES('cron','Memory consolidation','Mnemosyne','Memory consolidation','run it','done','merged 3 duplicates',$1) RETURNING id`,
		now.Add(-3*time.Hour)).Scan(&cronTaskID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(ctx, `INSERT INTO tasks(from_kind,from_name,to_agent,title,input,status,result,created_at)
		VALUES('intent','intent #1','Atlas','Intent: flight price dropped','check it','done','told the user',$1) RETURNING id`,
		now.Add(-2*time.Hour)).Scan(&intentTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO intents(type,owner,description,predicate,cadence_s,repeat,status,progress,next_due,fired_at)
		VALUES('watch','Scout','Watching for restock','{}','300',false,'fired','back in stock',$1,$1)`, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBriefing(ctx, "Oneiros", "Morning briefing", "Nothing urgent overnight.", 2); err != nil {
		t.Fatal(err)
	}

	entries, err := s.Audit(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(entries), entries)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Time.After(entries[i-1].Time) {
			t.Fatalf("entries not sorted newest-first at index %d: %+v", i, entries)
		}
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"cron", "intent", "watch", "dream"} {
		if !kinds[want] {
			t.Fatalf("missing audit kind %q among %+v", want, entries)
		}
	}
	// newest-first: dream briefing should be entry 0
	if entries[0].Kind != "dream" || entries[0].Title != "Morning briefing" {
		t.Fatalf("entry 0 = %+v, want the dream briefing", entries[0])
	}

	limited, err := s.Audit(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit=2 returned %d entries", len(limited))
	}
}

// TestSystemCronsGetElevatedPriority checks that a built-in maintenance cron (System: true) enqueues its
// task with a higher Priority than an ordinary user-created cron, so it isn't starved behind a backlog of
// ad-hoc chat delegation — tasks.ClaimNext claims by "priority DESC, id", so this only changes turn order
// when the queue actually has a backlog, never preempts something already running.
func TestSystemCronsGetElevatedPriority(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()

	sysID, err := s.SaveCron(ctx, Cron{Name: "Built-in maintenance", Agent: "Mnemosyne", Expr: "0 3 * * *", Prompt: "consolidate", Enabled: true, System: true})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := s.SaveCron(ctx, Cron{Name: "My reminder", Agent: "Atlas", Expr: "0 9 * * *", Prompt: "remind me", Enabled: true, System: false})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RunCronNow(ctx, sysID); err != nil {
		t.Fatal(err)
	}
	if err := s.RunCronNow(ctx, userID); err != nil {
		t.Fatal(err)
	}

	sysTasks, _ := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Mnemosyne"})
	userTasks, _ := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Atlas"})
	if len(sysTasks) != 1 || len(userTasks) != 1 {
		t.Fatalf("expected exactly one task each: sys=%+v user=%+v", sysTasks, userTasks)
	}
	if sysTasks[0].Priority <= userTasks[0].Priority {
		t.Fatalf("a system cron's task (priority %d) should outrank a user cron's (priority %d)", sysTasks[0].Priority, userTasks[0].Priority)
	}
	if userTasks[0].Priority != 0 {
		t.Fatalf("an ordinary user cron should keep the default priority, got %d", userTasks[0].Priority)
	}
}

// Dreaming must take the user's earlier "dismiss" into account: the dream prompt lists what was dismissed,
// and briefing_add refuses a near-duplicate of a dismissed briefing unless new_info says what is new.
func TestDreamingRespectsDismissedBriefings(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	id, err := s.AddBriefing(ctx, "Oneiros", "GPU prices are falling", "Prices of the RTX cards dropped 10 percent this week", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetBriefingStatus(ctx, id, "dismissed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Crons(ctx)
	for _, c := range cs {
		if c.Agent == "Oneiros" {
			if err := s.RunCronNow(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	ts, _ := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Oneiros"})
	if len(ts) != 1 || !strings.Contains(ts[0].Input, "DISMISSED") || !strings.Contains(ts[0].Input, "GPU prices are falling") {
		t.Fatalf("dream prompt must list dismissed briefings: %+v", ts)
	}

	if d := s.SimilarDismissed(ctx, "RTX GPU prices falling", "RTX card prices dropped this week by 10 percent"); d == nil || d.ID != id {
		t.Fatalf("near-duplicate not detected: %+v", d)
	}
	if d := s.SimilarDismissed(ctx, "Dentist appointment tomorrow", "Remember your dentist visit at 10:00"); d != nil {
		t.Fatalf("unrelated briefing wrongly matched: %+v", d)
	}
}

// Answering a briefing's question stores the reply on it and hands it to the agent that wrote it as a task.
func TestReplyingToABriefingRecordsItAndTasksTheAuthor(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	id, err := s.AddBriefing(ctx, "Oneiros", "Weekend plans", "Should I look for flights to Berlin this weekend?", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplyBriefing(ctx, id, "  "); err == nil {
		t.Fatal("an empty reply must be refused")
	}
	task, err := s.ReplyBriefing(ctx, id, "No, I am staying home in May.")
	if err != nil {
		t.Fatal(err)
	}
	if task.ToAgent != "Oneiros" || !strings.Contains(task.Input, "staying home in May") || !strings.Contains(task.Input, "flights to Berlin") {
		t.Fatalf("task = %+v", task)
	}
	b, _ := s.Briefing(ctx, id)
	if b.Reply != "No, I am staying home in May." || b.RepliedAt == nil || b.Status != "delivered" {
		t.Fatalf("briefing = %+v", b)
	}
}
