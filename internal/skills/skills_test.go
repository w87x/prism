package skills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestFrontmatterAndStore(t *testing.T) {
	body, fm := ParseFrontmatter("\xef\xbb\xbf---\nname: web-scraper\ndescription: >\n  Scrape pages\n  politely.\nlicense: MIT\n---\n\n# Steps\n1. go")
	if fm["name"] != "web-scraper" || fm["description"] != "Scrape pages politely." || !strings.HasPrefix(body, "# Steps") {
		t.Fatalf("frontmatter: %v %q", fm, body)
	}
	if b, fm := ParseFrontmatter("no frontmatter"); len(fm) != 0 || b != "no frontmatter" {
		t.Fatal("plain markdown must pass through")
	}
	d := testutil.DB(t)
	s := NewStore(d.Pool, t.TempDir())
	ctx := context.Background()
	id, err := s.Save(ctx, Skill{Name: "Web Scraper!", Body: "---\nname: web-scraper\ndescription: scrape pages\n---\nbody", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.GetID(ctx, id)
	if k.Name != "web-scraper" || k.Description != "scrape pages" {
		t.Fatalf("saved: %+v", k)
	}
	_, _ = s.Save(ctx, Skill{Name: "hidden", Description: "off", Body: "x", Enabled: false, ID: 0})
	_, _ = d.Exec(ctx, `UPDATE skills SET enabled=false WHERE name='hidden'`)
	sums := s.Summaries(ctx, nil)
	if len(sums) != 1 || sums[0].Name != "web-scraper" { // progressive disclosure: name+description only, enabled only
		t.Fatalf("summaries: %+v", sums)
	}
	if s2 := s.Summaries(ctx, []string{"nope"}); len(s2) != 0 {
		t.Fatal("explicit names must filter")
	}
}

func TestAdaptRejectsPlatformMentionsAfterRepair(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	calls := 0
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		calls++
		if calls == 1 { // first answer still mentions the source platform → one repair pass
			return testutil.Reply{Content: "---\nname: x\ndescription: d\n---\nUse the OpenClaw CLI to run it."}
		}
		return testutil.Reply{Content: "---\nname: x\ndescription: d\n---\nUse a shell command to run it."}
	}
	r, _ := testutil.Setup(t, d, fake)
	out, err := Adapter(r, func() []string { return []string{"shell"} })(context.Background(), "---\nname: x\ndescription: d\n---\nUse openclaw run")
	if err != nil || strings.Contains(strings.ToLower(out), "openclaw") || calls != 2 {
		t.Fatalf("adapt: %q err=%v calls=%d", out, err, calls)
	}
}

func TestScrubPlatformNames(t *testing.T) {
	got := scrubPlatforms("Run OpenClaw, then check ClawHub and the Hermes Agent docs; hermes-like is fine? Open Claw too.")
	for _, bad := range []string{"OpenClaw", "ClawHub", "Hermes Agent", "Open Claw"} {
		if strings.Contains(got, bad) {
			t.Fatalf("still contains %q: %s", bad, got)
		}
	}
}

func runTool(t *testing.T, reg *tools.Registry, name, agent string, args any) (string, error) {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), &tools.Env{Agent: agent}, b)
}

// skill_write is Daedalus's only way to actually persist a routine: it must accept a complete SKILL.md,
// reject one with no name in its frontmatter, and writing the same name again must update the existing
// skill (Daedalus extending/correcting one it already wrote) rather than silently forking a duplicate.
func TestSkillWriteSavesAndOverwritesByName(t *testing.T) {
	d := testutil.DB(t)
	s := NewStore(d.Pool, t.TempDir())
	reg := tools.NewRegistry(d.Pool)
	RegisterTools(reg, s)
	ctx := context.Background()

	if _, err := runTool(t, reg, "skill_write", "Daedalus", map[string]any{"body": "---\ndescription: no name given\n---\nbody"}); err == nil {
		t.Fatal("a SKILL.md with no name in its frontmatter must be rejected")
	}

	out, err := runTool(t, reg, "skill_write", "Daedalus", map[string]any{"body": "---\nname: price-check\ndescription: compare prices across two sites\n---\n1. fetch both\n2. compare"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "price-check") {
		t.Fatalf("expected the saved name in the confirmation: %q", out)
	}
	k, err := s.Get(ctx, "price-check")
	if err != nil {
		t.Fatal(err)
	}
	if k.Description != "compare prices across two sites" || !strings.Contains(k.Body, "1. fetch both") {
		t.Fatalf("saved skill: %+v", k)
	}
	firstID := k.ID

	// writing the same name again must update the existing skill, not create a second one
	if _, err := runTool(t, reg, "skill_write", "Daedalus", map[string]any{"body": "---\nname: price-check\ndescription: compare prices, now with currency conversion\n---\n1. fetch both\n2. convert\n3. compare"}); err != nil {
		t.Fatal(err)
	}
	k2, err := s.Get(ctx, "price-check")
	if err != nil {
		t.Fatal(err)
	}
	if k2.ID != firstID {
		t.Fatalf("expected the same skill row updated, got a new id %d (was %d)", k2.ID, firstID)
	}
	if !strings.Contains(k2.Description, "currency conversion") {
		t.Fatalf("expected the updated description, got %q", k2.Description)
	}
	all, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range all {
		if x.Name == "price-check" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one price-check skill, found %d", n)
	}
}

func TestTidyMergesSkillsThatTeachTheSameThing(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	s := NewStore(d.Pool, t.TempDir())
	s.LLM = r
	var rehomed string
	s.Rehome = func(ctx context.Context, from []string, to string) { rehomed = strings.Join(from, ",") + ">" + to }
	ctx := context.Background()
	for _, k := range [][2]string{{"avoid-ajax-loop", "do not loop on ajax guesses"}, {"fetch-pattern-switch", "switch url pattern when fetches repeat"}, {"rail-booking", "book rail tickets"}} {
		if _, err := s.Save(ctx, Skill{Name: k[0], Description: k[1], Body: "---\nname: " + k[0] + "\ndescription: " + k[1] + "\n---\nsteps", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	fake.Handler = func(map[string]any, int) testutil.Reply {
		return testutil.Reply{Content: `{"groups":[{"members":["avoid-ajax-loop","fetch-pattern-switch","rail-booking-typo"],"body":"---\nname: stop-looping-on-fetches\ndescription: when fetches repeat, change approach\n---\n1. switch pattern"}]}`}
	}
	if msg, err := s.Tidy(ctx); err != nil || !strings.Contains(msg, "1 groups merged") || rehomed != "avoid-ajax-loop,fetch-pattern-switch>stop-looping-on-fetches" {
		t.Fatalf("msg=%q err=%v rehomed=%q (an invented member is ignored, the rest merge)", msg, err, rehomed)
	}
	on, _ := s.Summaries(ctx, nil), 0
	names := ""
	for _, x := range on {
		names += x.Name + " "
	}
	if !strings.Contains(names, "stop-looping-on-fetches") || !strings.Contains(names, "rail-booking") || strings.Contains(names, "avoid-ajax-loop") {
		t.Fatalf("enabled skills after the merge: %s", names)
	}
}

// A skill is put in front of a task only when the task is about what the skill is about: sharing a couple of everyday words with its
// description is not enough.
func TestMatchNeedsTheTaskToBeAboutTheSkill(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	s := NewStore(d.Pool, t.TempDir())
	s.LLM = r
	ctx := context.Background()
	if _, err := s.Save(ctx, Skill{Name: "avito-listing-loop", Description: "when fetching avito listing pages stop looping over url variants", Body: "---\nname: avito-listing-loop\ndescription: x\n---\n1. one url", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if ms := s.Match(ctx, "fetching avito listing pages looping url variants", 3, nil); len(ms) != 1 {
		t.Fatalf("a task about the same thing should match: %+v", ms)
	}
	unrelated := "Find and download the animated film with Russian audio on RuTracker, check seeders, then add the torrent to the NAS and confirm the listing of the folder pages is complete, then report which pages loop"
	if ms := s.Match(ctx, unrelated, 3, nil); len(ms) != 0 {
		t.Fatalf("a task that merely shares words (listing, pages, loop) must not match: %+v", ms)
	}
}

// A new skill that reads almost like an existing one is refused with a pointer to it (extend that one), unless the writer says how it differs.
func TestSkillWriteRefusesNearTwin(t *testing.T) {
	d := testutil.DB(t)
	s := NewStore(d.Pool, t.TempDir())
	reg := tools.NewRegistry(d.Pool)
	RegisterTools(reg, s)
	write := func(name, desc string, extra map[string]any) (string, error) {
		args := map[string]any{"body": "---\nname: " + name + "\ndescription: " + desc + "\n---\n1. do it"}
		for k, v := range extra {
			args[k] = v
		}
		return runTool(t, reg, "skill_write", "Daedalus", args)
	}
	if _, err := write("synology-queue-torrent", "queue a torrent on the Synology Download Station", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := write("synology-torrent-queue", "add a torrent to the Synology Download Station queue", nil); err == nil || !strings.Contains(err.Error(), "synology-queue-torrent") {
		t.Fatalf("a twin must be refused with the original's name: %v", err)
	}
	if _, err := write("synology-torrent-queue", "add a torrent to the Synology Download Station queue", map[string]any{"distinct": "pauses and resumes, not adds"}); err != nil {
		t.Fatalf("with a reason it goes through: %v", err)
	}
	if _, err := write("avito-listing-search", "find listings on Avito and compare prices", nil); err != nil {
		t.Fatalf("an unrelated skill is fine: %v", err)
	}
}
