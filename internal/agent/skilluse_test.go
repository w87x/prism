package agent

import (
	"context"
	"strings"
	"testing"

	"prism/internal/memory"
	"prism/internal/tasks"
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
