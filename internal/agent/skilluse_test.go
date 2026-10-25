package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/tasks"
	"prism/internal/testutil"
)

type fakeSkills struct {
	list []SkillMatch
	uses []string
}

func (f *fakeSkills) Summaries(ctx context.Context, names []string) []SkillSummary {
	var out []SkillSummary
	for _, k := range f.list {
		out = append(out, SkillSummary{Name: k.Name, Description: k.Description})
	}
	return out
}

func (f *fakeSkills) Match(ctx context.Context, q string, limit int, allow func(string) bool) []SkillMatch {
	var out []SkillMatch
	for _, k := range f.list {
		if allow != nil && !allow(k.Name) {
			continue
		}
		if strings.Contains(strings.ToLower(q), strings.Split(k.Name, "-")[0]) {
			out = append(out, k)
		}
	}
	return out
}

func (f *fakeSkills) Tidy(ctx context.Context) (string, error) {
	return "skills: nothing to merge", nil
}

func (f *fakeSkills) NoteUse(ctx context.Context, skill, agent string, taskID int64, how string) {
	f.uses = append(f.uses, skill+":"+agent+":"+how)
}

// A task gets the procedure of the skill that fits it, but only skills its agent may see: pinned ones for an agent with a list, unclaimed ones
// for an agent without. A long successful task that no skill covered goes to Daedalus once per agent, and a covered one does not.
func TestSkillsReachTheAgentsTheyBelongTo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	fs := &fakeSkills{list: []SkillMatch{
		{Name: "rail-booking", Description: "book rail tickets", Body: "1. open the timetable"},
		{Name: "blog-watch", Description: "watch a blog for new posts", Body: "1. fetch the feed"},
	}}
	h.e.Skills = fs
	owner, err := h.e.Profiles.Save(ctx, Profile{Name: "Rail", Role: RoleWorker, Enabled: true, Skills: []string{"rail-booking"}}, "t")
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.e.Profiles.Save(ctx, Profile{Name: "Web", Role: RoleWorker, Enabled: true}, "t")
	if err != nil {
		t.Fatal(err)
	}
	note, covered := h.e.skillNote(ctx, owner, "rail ticket to Kazan", 1)
	if !covered || !strings.Contains(note, "open the timetable") {
		t.Fatalf("the owner should get the procedure: %q", note)
	}
	if n, c := h.e.skillNote(ctx, other, "rail ticket to Kazan", 2); c || n != "" {
		t.Fatalf("an unrelated agent must not get a skill pinned to someone else: %q", n)
	}
	if n, c := h.e.skillNote(ctx, other, "blog posts today", 3); !c || !strings.Contains(n, "fetch the feed") {
		t.Fatalf("an agent without a list gets unclaimed skills: %q", n)
	}
	if n, c := h.e.skillNote(ctx, owner, "blog posts today", 4); c || n != "" {
		t.Fatalf("an agent with a list sees only its own skills: %q", n)
	}
	if len(fs.uses) != 2 {
		t.Fatalf("each injection should be recorded: %v", fs.uses)
	}
	if idx := h.e.skillIndex(ctx, other); len(idx) != 1 || idx[0].Name != "blog-watch" {
		t.Fatalf("the prompt index must not carry a colleague's skills: %+v", idx)
	}
}

func TestLongUncoveredTaskIsOfferedToDaedalusOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.e.Skills = &fakeSkills{}
	p := &Profile{Name: "Web", Role: RoleWorker}
	tk := tasks.Task{ID: 42, ToAgent: "Web", Title: "scrape the thing", Input: "scrape the thing"}
	long := &RunResult{Text: "done", Iterations: 9}
	h.e.maybeLearnSkill(ctx, tk, p, long, true)
	h.e.maybeLearnSkill(ctx, tk, p, &RunResult{Text: "done", Iterations: 2}, false)
	count := func() (n int) {
		_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE to_agent='Daedalus'`).Scan(&n)
		return
	}
	if count() != 0 {
		t.Fatal("a covered or a short task must not be offered")
	}
	h.e.maybeLearnSkill(ctx, tk, p, long, false)
	h.e.maybeLearnSkill(ctx, tk, p, long, false)
	if count() != 1 {
		t.Fatalf("offered %d times, want once per agent", count())
	}
}

// One lesson lives in one place: a lesson worded differently folds into the one already there, and an old "Pitfall:" memory fact moves into
// immune memory (with the words of its task as triggers) and is retired.
func TestLessonsAreKeptOnceAndPitfallFactsFold(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.e.AddAntibody(ctx, "Mira", []string{"avito"}, "When scraping Avito, stop retrying the same URL variants across fetches.", "a")
	_ = h.e.AddAntibody(ctx, "Mira", []string{"listing"}, "Stop retrying the same URL variants across fetches when scraping Avito.", "b")
	abs, _ := h.e.Antibodies(ctx)
	if len(abs) != 1 || len(abs[0].Triggers) != 2 {
		t.Fatalf("a reworded lesson must fold into the first, adding its triggers: %+v", abs)
	}
	tk, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: "Mira", Title: "Extract exchange rates", Input: "Extract the exchange rates table from the central bank website"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Memory.Store(ctx, memory.StoreReq{Bank: "profile", Agent: "Mira", Text: "Pitfall: When extracting tables, stop paging once the rows repeat.", Tags: []string{"pitfall"}, TaskID: tk.ID}); err != nil {
		t.Fatal(err)
	}
	if n := h.e.FoldPitfalls(ctx); n != 1 {
		t.Fatalf("folded %d", n)
	}
	abs, _ = h.e.Antibodies(ctx)
	if len(abs) != 2 {
		t.Fatalf("the pitfall should now be a lesson: %+v", abs)
	}
	if n := h.e.FoldPitfalls(ctx); n != 0 {
		t.Fatalf("folded again: %d", n)
	}
}

// The acceptance judge must see what the agent really did. A task that starts its own session used to reach the check with no session id, so
// the trail read "(none)" and every report was called unsupported.
func TestAcceptanceJudgeSeesTheToolTrailOfAFreshTask(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var judgeInput string
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		_, last, _ := msgAt(req, -1)
		switch {
		case strings.Contains(sys, "team lead checking"):
			judgeInput = last
			return testutil.Reply{Content: `{"met":true,"gap":""}`}
		case call == 0:
			return testutil.Reply{Tools: []llm.ToolCall{{ID: "c1", Name: "clock", Arguments: `{}`}}}
		}
		return testutil.Reply{Content: "It is late."}
	}
	tk, _ := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "agent", FromName: "Atlas", ToAgent: "Atlas", Title: "time", Input: "what time is it", Depth: 1}, true)
	h.e.Tasks.SetCriteria(ctx, tk.ID, "the time is reported")
	h.e.RunTask(ctx, tk, TaskOpts{})
	if !strings.Contains(judgeInput, "clock") {
		t.Fatalf("the judge was not shown the tool calls: %q", judgeInput)
	}
}

// A tool the user keeps allowing, with no refusal, is offered for arming; one refused once, a read-only one, a dangerous-sounding one and a
// dismissed one are not.
func TestEarnedAutonomyOffersToolsTheUserKeepsAllowing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var pick, refused string
	for _, tl := range h.e.Tools.All() {
		if tl.Risk != 0 && !tl.Auto && !neverAuto.MatchString(tl.Name) && !h.e.Tools.State(tl.Name).Armed && h.e.Tools.State(tl.Name).Enabled {
			if pick == "" {
				pick = tl.Name
			} else if refused == "" {
				refused = tl.Name
				break
			}
		}
	}
	if pick == "" || refused == "" {
		t.Skip("no suitable tools registered in this harness")
	}
	for i := 0; i < 9; i++ {
		h.e.noteApproval(ctx, pick, "Mira", int64(i%4+1), true)
		h.e.noteApproval(ctx, refused, "Mira", int64(i%4+1), true)
	}
	h.e.noteApproval(ctx, refused, "Mira", 9, false)
	got := h.e.ArmSuggestions(ctx)
	if len(got) != 1 || got[0].Tool != pick || got[0].Allows != 9 {
		t.Fatalf("suggestions: %+v (want only %s)", got, pick)
	}
	if err := h.e.DismissArmSuggestion(ctx, pick); err != nil {
		t.Fatal(err)
	}
	if got := h.e.ArmSuggestions(ctx); len(got) != 0 {
		t.Fatalf("a dismissed suggestion must not come back: %+v", got)
	}
}

// Work done again and again in short runs — no single run worth a skill — is noticed as a group: three or more finished tasks for the same
// kind of job on at least two days, uncovered by any skill, go to Daedalus once.
func TestRecurringWorkIsHandedToDaedalusOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.e.Skills = &fakeSkills{}
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Niko", Role: RoleWorker, Enabled: true}, "t"); err != nil {
		t.Fatal(err)
	}
	add := func(goal string, daysAgo int) {
		tk, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "agent", FromName: "Atlas", ToAgent: "Niko", Title: "t", Input: goal, Depth: 1}, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.e.DB.Exec(ctx, `INSERT INTO task_summaries(task_id,agent,title,status,goal,created_at) VALUES($1,'Niko','t','done',$2, now()-make_interval(days=>$3))`, tk.ID, goal, daysAgo); err != nil {
			t.Fatal(err)
		}
	}
	add("Search Jackett for the torrent of the series Futurama season 11 and add the best release to Synology Download Station", 3)
	add("Search Jackett for the torrent of the series Haunted Hotel season 2 and add the best release to Synology Download Station", 2)
	add("Search Jackett for the torrent of the series Widow's Bay season 1 and add the best release to Synology Download Station", 0)
	add("Tell me the current weather forecast for Moscow tomorrow morning", 1)
	if n := h.e.learnRecurringSkills(ctx); n != 1 {
		t.Fatalf("one recurring job should be offered, got %d", n)
	}
	var title, input string
	_ = h.e.DB.QueryRow(ctx, `SELECT title, input FROM tasks WHERE to_agent='Daedalus'`).Scan(&title, &input)
	if !strings.Contains(title, "recurring") || !strings.Contains(input, "Jackett") || strings.Contains(input, "weather") || !strings.Contains(input, "Niko") {
		t.Fatalf("the request to Daedalus: %q / %q", title, input)
	}
	// not again: gated for a day, and the tasks were handed over already
	_ = h.e.Settings.Delete(ctx, recurringOfferKey)
	var st recurringState
	_, _ = h.e.Settings.Get(ctx, recurringOfferKey, &st)
	st.At = time.Time{}
	st.Offered = nil
	if n := h.e.learnRecurringSkills(ctx); n != 1 { // state was wiped on purpose: offers again
		t.Fatalf("with the record wiped it is offered again: %d", n)
	}
	if n := h.e.learnRecurringSkills(ctx); n != 0 {
		t.Fatalf("a second run the same day does nothing: %d", n)
	}
}

// Daedalus writes skills from what already happened: it must not be able to send colleagues off to redo the work.
func TestDaedalusCannotAskColleagues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p := &Profile{Name: "Daedalus", Role: RoleMaint, System: true, Tools: []string{"task_transcript", "skill_write"}}
	active := h.e.initialTools(ctx, p, RunSpec{})
	if active["ask_colleague"] || active["delegate"] || !active["task_transcript"] {
		t.Fatalf("Daedalus's tools: %v", active)
	}
}

// What was learned about an agent goes when it is deleted, so a new agent with the same name does not start with the old one's lessons.
func TestDeletingAnAgentDropsWhatWasLearnedAboutIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.e.Profiles.Save(ctx, Profile{Name: "Wren", Role: RoleWorker, Enabled: true}, "t")
	if err != nil {
		t.Fatal(err)
	}
	_ = h.e.AddAntibody(ctx, "Wren", []string{"avito"}, "When scraping Avito, stop retrying the same URL variants.", "t")
	_, _ = h.e.DB.Exec(ctx, `INSERT INTO trust(agent,tool,wins,losses) VALUES('Wren','web_fetch',3,0)`)
	if err := h.e.Profiles.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.e.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM antibodies WHERE agent='Wren') + (SELECT count(*) FROM trust WHERE agent='Wren')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d leftovers of a deleted agent", n)
	}
}

// Skills are the specialists' procedures: the staff and the entry agent get none put in front of their tasks.
func TestSkillsAreNotPutInFrontOfStaffOrTheEntryAgent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.e.Skills = &fakeSkills{list: []SkillMatch{{Name: "avito-search", Description: "avito listings", Body: "steps"}}}
	worker := &Profile{Name: "Scout", Role: RoleWorker}
	if n, c := h.e.skillNote(ctx, worker, "find avito listings", 1); !c || n == "" {
		t.Fatalf("a specialist gets it: %q", n)
	}
	for _, r := range []string{RoleMaint, RoleEntry} {
		if n, c := h.e.skillNote(ctx, &Profile{Name: "X", Role: r}, "find avito listings", 2); c || n != "" {
			t.Fatalf("role %s must not get a skill: %q", r, n)
		}
	}
}
