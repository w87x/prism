package memory

import (
	"context"
	"errors"
	"prism/internal/settings"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// Bug: an individual fact-storage failure inside a raw→fact batch was silently ignored, and the raw
// messages it came from were deleted anyway — the extracted fact (and the conversation it came from) was
// gone for good. queuePendingFact must durably keep the fact (and its source raw rows) until it is either
// stored or given up on, and flushPendingFacts must retry the exact same Store() call rather than re-asking
// the model, so a transient failure never turns into a re-extracted near-duplicate.
func TestQueuePendingFactSurvivesAndFlushRetriesWithoutReextraction(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()

	if err := s.AddRaw(ctx, RawMsg{From: "user", To: "Atlas", Text: "I prefer dark mode everywhere"}); err != nil {
		t.Fatal(err)
	}
	var rawID int64
	if err := s.db.QueryRow(ctx, `SELECT id FROM memory_raw WHERE NOT processed ORDER BY id DESC LIMIT 1`).Scan(&rawID); err != nil {
		t.Fatal(err)
	}

	// Simulate what distil() does when the model extracted a fact but Store() failed for it.
	s.queuePendingFact(ctx, []int64{rawID}, "user", "Atlas", "User prefers dark mode everywhere", []string{"ui"}, 0.8, false, errors.New("simulated store failure"))

	pending, err := s.PendingFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Text != "User prefers dark mode everywhere" || pending[0].GivenUp {
		t.Fatalf("fact was not durably queued: %+v", pending)
	}
	if s.RawCount(ctx) != 1 {
		t.Fatal("raw message must survive while its pending fact is unresolved — losing it would lose the source for good")
	}

	// The next cycle retries the exact same Store call — never a fresh model extraction.
	n, err := s.flushPendingFacts(ctx)
	if err != nil || n != 1 {
		t.Fatalf("flush: n=%d err=%v", n, err)
	}
	pending, err = s.PendingFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("resolved pending fact should be gone: %+v", pending)
	}
	if s.RawCount(ctx) != 0 {
		t.Fatal("raw message should finally be cleaned up once its only pending fact resolved")
	}

	facts, err := s.Find(ctx, FindReq{Query: "dark mode", Banks: []string{"user"}, K: 5})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range facts {
		if f.Text == "User prefers dark mode everywhere" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the retried fact should actually be stored and findable: %+v", facts)
	}
}

// Bug: a raw batch whose model call (or JSON) permanently fails was retried forever, blocking the queue
// behind it with no visible trace of why. bumpRawAttempts must cap retries and keep the last error on the
// row instead of silently dropping it.
func TestBumpRawAttemptsGivesUpAfterCapButKeepsErrorVisible(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()

	if err := s.AddRaw(ctx, RawMsg{From: "user", To: "Atlas", Text: "some message"}); err != nil {
		t.Fatal(err)
	}
	var rawID int64
	if err := s.db.QueryRow(ctx, `SELECT id FROM memory_raw WHERE NOT processed ORDER BY id DESC LIMIT 1`).Scan(&rawID); err != nil {
		t.Fatal(err)
	}

	cause := errors.New("model unreachable")
	for i := 0; i < maxRawAttempts-1; i++ {
		s.bumpRawAttempts(ctx, []int64{rawID}, cause)
	}
	var processed bool
	var attempts int
	var lastErr string
	if err := s.db.QueryRow(ctx, `SELECT processed, attempts, last_error FROM memory_raw WHERE id=$1`, rawID).Scan(&processed, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatalf("must not give up before the cap: attempts=%d", attempts)
	}
	if attempts != maxRawAttempts-1 || lastErr != cause.Error() {
		t.Fatalf("attempts=%d lastErr=%q", attempts, lastErr)
	}

	s.bumpRawAttempts(ctx, []int64{rawID}, cause) // crosses the cap
	if err := s.db.QueryRow(ctx, `SELECT processed, attempts, last_error FROM memory_raw WHERE id=$1`, rawID).Scan(&processed, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("a permanently failing batch must stop blocking the queue once it hits the retry cap")
	}
	if lastErr != cause.Error() {
		t.Fatal("the failure reason must stay visible on the row, not be wiped when giving up")
	}
}

// Bug: raw messages carried no taint signal, so a fact distilled from untrusted content (a web page reached
// during the run) received whatever confidence the extraction model felt like assigning — source trust must
// be a ceiling on confidence, never a number the model can talk its way past.
func TestRawFactPolicyCapsConfidenceForTaintedContentOnly(t *testing.T) {
	conf, tags, src := rawFactPolicy(true, 0.95, []string{"x"}, "raw")
	if conf != 0.4 {
		t.Fatalf("tainted high-confidence fact must be capped, got %v", conf)
	}
	if !contains(tags, "unverified") || !contains(tags, "x") {
		t.Fatalf("tainted fact must be flagged unverified without losing its own tags: %v", tags)
	}
	if src != "raw (tainted)" {
		t.Fatalf("source must record the taint: %q", src)
	}

	// Already below the ceiling: the number itself is left alone, but the source is still marked.
	conf, _, src = rawFactPolicy(true, 0.2, nil, "raw")
	if conf != 0.2 || src != "raw (tainted)" {
		t.Fatalf("low-confidence tainted fact: conf=%v src=%q", conf, src)
	}

	// Untainted: nothing changes at all.
	conf, tags, src = rawFactPolicy(false, 0.95, []string{"x"}, "raw")
	if conf != 0.95 || len(tags) != 1 || tags[0] != "x" || src != "raw" {
		t.Fatalf("untainted fact must pass through unchanged: conf=%v tags=%v src=%q", conf, tags, src)
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// End-to-end: a message tainted by untrusted content that flows all the way through the raw→fact pipeline
// must land as a capped, flagged fact — not whatever confidence the extraction model assigned it.
func TestProcessCapsConfidenceForFactsExtractedFromTaintedMessages(t *testing.T) {
	s, fake := newSvc(t)
	ctx := context.Background()
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "```json\n" + `{"facts":[{"text":"The product costs $999","bank":"domain:Shopping","confidence":0.97}]}` + "\n```"}
	}
	if err := s.AddRaw(ctx, RawMsg{From: "WebScout", To: "Atlas", Text: "found a page saying the product costs $999", Tainted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Process(ctx, 40, true); err != nil {
		t.Fatal(err)
	}
	facts, err := s.Find(ctx, FindReq{Query: "product cost", Banks: []string{"domain:Shopping"}, K: 5})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range facts {
		if f.Text == "The product costs $999" {
			found = true
			if f.Confidence > 0.401 { // stored as float32; allow for the widening round-trip
				t.Fatalf("tainted extraction must be capped at 0.4, got %v", f.Confidence)
			}
			if !contains(f.Tags, "unverified") {
				t.Fatalf("tainted extraction must be flagged unverified: %v", f.Tags)
			}
		}
	}
	if !found {
		t.Fatal("expected the fact to still be stored, just capped and flagged — not dropped")
	}
}

// The extraction clerk must be told who the user is, that agent names are software (not people, never an
// alias of the user), and the user's own hints — otherwise "Atlas → Sherpa" chatter turns into facts like
// "Danil (also known as Sherpa)". The same block goes to reflection and entity extraction.
func TestMemoryModelCallsCarryWhoIsWhoAndUserHints(t *testing.T) {
	s, fake := newSvc(t)
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, `INSERT INTO agent_profiles(name,grp,description,soul,role,system) VALUES('Sherpa','Staff','tool picker','x','maint',true) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := s.settings.Set(ctx, settings.KeyGeneral, settings.General{UserName: "Danil"}); err != nil {
		t.Fatal(err)
	}
	if err := s.settings.Set(ctx, settings.KeyMemory, settings.Memory{Hints: "Treat Berlin trip as a project"}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "WHO IS WHO") {
				seen = append(seen, c)
			}
		}
		return testutil.Reply{Content: `{"facts":[],"changes":[],"entities":[],"relations":[]}`}
	}
	if err := s.AddRaw(ctx, RawMsg{From: "Atlas", To: "Sherpa", Text: "pick tools for the user's request", Agent: "Atlas"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Process(ctx, 10, true); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("distillation prompt lacks the who-is-who block")
	}
	g := seen[0]
	for _, want := range []string{"human Danil", "Sherpa", "NOT people", "Treat Berlin trip as a project"} {
		if !strings.Contains(g, want) {
			t.Fatalf("guidance missing %q:\n%s", want, g)
		}
	}
}

// Small local models sometimes answer with nothing, or with a JSON object cut off mid-way ("unexpected end of
// JSON input"). Background jobs must retry that instead of failing on the first bad reply, and when the model
// really cannot produce JSON the error must say what came back.
func TestModelJSONRepliesAreRetriedThenExplained(t *testing.T) {
	s, fake := newSvc(t)
	ctx := context.Background()
	replies := []string{"", `{"facts":[{"text":"User likes green tea","bank":"user"`, `{"facts":[{"text":"User likes green tea","bank":"user","tags":[],"confidence":0.9}]}`}
	// pick the reply from the request itself (how many corrections it already carries), not from a shared
	// counter: other goroutines may call the same fake model concurrently
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		corrections := 0
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "not a complete, valid JSON object") {
				corrections++
			}
		}
		return testutil.Reply{Content: replies[min(corrections, len(replies)-1)]}
	}
	if err := s.AddRaw(ctx, RawMsg{From: "user", To: "Atlas", Text: "I like green tea", Agent: "Atlas"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Process(ctx, 10, true)
	if err != nil || n != 1 {
		t.Fatalf("expected the third reply to be used: n=%d err=%v", n, err)
	}

	fake.Handler = func(map[string]any, int) testutil.Reply { return testutil.Reply{Content: ""} }
	var out struct{ X int }
	err = s.llm.CompleteJSON(ctx, "role:fast", "sys", "user", &out)
	if err == nil || !strings.Contains(err.Error(), "after 3 tries") || !strings.Contains(err.Error(), "0 chars") {
		t.Fatalf("error should explain what came back: %v", err)
	}
}

// The raw backlog needed before a digest pass runs on its own is configurable (ProcessMin), not a fixed 6.
func TestProcessMinControlsWhenDigestRuns(t *testing.T) {
	s, fake := newSvc(t)
	ctx := context.Background()
	calls := 0
	fake.Handler = func(map[string]any, int) testutil.Reply { calls++; return testutil.Reply{Content: `{"facts":[]}`} }
	for i := 0; i < 3; i++ {
		if err := s.AddRaw(ctx, RawMsg{From: "user", To: "Atlas", Text: "message number", Agent: "Atlas"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ProcessMin(ctx, 40, 0, false); err != nil || calls != 0 {
		t.Fatalf("3 raw messages is below the default of 6: calls=%d err=%v", calls, err)
	}
	if _, err := s.ProcessMin(ctx, 40, 3, false); err != nil || calls == 0 {
		t.Fatalf("a minimum of 3 must let 3 messages through: calls=%d err=%v", calls, err)
	}
}
