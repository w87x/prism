package builtin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/settings"
	"prism/internal/testutil"
)

func bookmarkRank(t *testing.T, deps Deps, id int64) float64 {
	t.Helper()
	var r float64
	if err := deps.DB.QueryRow(context.Background(), `SELECT rank FROM bookmarks WHERE id=$1`, id).Scan(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Every agent-added bookmark starts at the same baseline, regardless of anything the model tried to sneak
// into the tool call (bookmark_add's schema does not expose rank at all).
func TestBookmarkAddStartsAtRankOne(t *testing.T) {
	reg, deps, _, _ := setup(t)
	out, err := run(t, reg, "bookmark_add", map[string]any{"url": "https://example.com/a", "title": "A", "rank": 999})
	if err != nil {
		t.Fatal(err)
	}
	id := bookmarkIDFromMessage(t, out)
	if got := bookmarkRank(t, deps, id); got != 1 {
		t.Fatalf("expected rank 1 regardless of the (undocumented) rank field, got %v", got)
	}
}

// A bookmark actually returned by a search is nudged up; one that was not is left untouched — the whole
// point of the ranking being usage-driven, not just presence.
func TestBookmarkFindBumpsOnlyReturnedBookmarks(t *testing.T) {
	reg, deps, _, _ := setup(t)
	ctx := context.Background()
	useful, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://pcpartpicker.com", Title: "PCPartPicker", Description: "compare PC hardware prices", Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://recipes.example", Title: "Recipes", Description: "cooking ideas", Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, reg, "bookmark_find", map[string]any{"query": "hardware prices"}); err != nil {
		t.Fatal(err)
	}
	if got := bookmarkRank(t, deps, useful); got <= 1 || got > 1.02 {
		t.Fatalf("returned bookmark should be nudged to ~1.01, got %v", got)
	}
	if got := bookmarkRank(t, deps, unrelated); got != 1 {
		t.Fatalf("unrelated bookmark must be untouched, got %v", got)
	}
	var lastUsed *string
	if err := deps.DB.QueryRow(ctx, `SELECT last_used::text FROM bookmarks WHERE id=$1`, useful).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if lastUsed == nil {
		t.Fatal("last_used should be set on the returned bookmark")
	}
}

// A bookmark with a strong track record (high rank) can win out over a barely-more-relevant one that has
// never proven useful — relevance still matters (an utterly unrelated bookmark never surfaces), but usage
// breaks close ties.
func TestBookmarkRankWeightsSearchOrder(t *testing.T) {
	reg, deps, _, _ := setup(t)
	ctx := context.Background()
	// both plausibly match "price tracker"; B has slightly more matching text, A has a strong usage history
	lowRank, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://a.example", Title: "Price Tracker", Description: "price tracker tool", Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	highRank, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://b.example", Title: "Price Tracker Pro", Description: "price tracker and price history tool", Rank: 5})
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, reg, "bookmark_find", map[string]any{"query": "price tracker", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.Contains(lines[0], "b.example") {
		t.Fatalf("the far higher-ranked bookmark should lead, got:\n%s", out)
	}
	_, _ = lowRank, highRank
}

func TestTopmostBookmarkRankIsAboveExisting(t *testing.T) {
	_, deps, _, _ := setup(t)
	ctx := context.Background()
	if _, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://x.example", Title: "X", Rank: 7}); err != nil {
		t.Fatal(err)
	}
	top, err := TopmostBookmarkRank(ctx, deps.DB)
	if err != nil {
		t.Fatal(err)
	}
	if top <= 7 {
		t.Fatalf("expected a rank above the current max (7), got %v", top)
	}
}

// The decay pass must skip a bookmark used or created today (it needs a full day before it can start
// losing rank), and must skip a row it has already decayed today even if asked again (the hourly cleanup
// tick calls this far more than once a day).
func TestDecayBookmarksSkipsTodayAndDoesNotDoubleDecay(t *testing.T) {
	_, deps, _, _ := setup(t)
	ctx := context.Background()
	freshToday, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://fresh.example", Title: "Fresh", Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://stale.example", Title: "Stale", Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	// backdate the stale one as if it were created (and never used since) three days ago
	if _, err := deps.DB.Exec(ctx, `UPDATE bookmarks SET created_at=now()-interval '3 days' WHERE id=$1`, stale); err != nil {
		t.Fatal(err)
	}

	decayed, deleted, err := DecayBookmarks(ctx, deps.DB)
	if err != nil {
		t.Fatal(err)
	}
	if decayed != 1 || deleted != 0 {
		t.Fatalf("expected exactly the stale bookmark decayed, got decayed=%d deleted=%d", decayed, deleted)
	}
	if got := bookmarkRank(t, deps, freshToday); got != 1 {
		t.Fatalf("a bookmark created today must not decay yet, got %v", got)
	}
	if got := bookmarkRank(t, deps, stale); got >= 1 || got < 0.98 {
		t.Fatalf("stale bookmark should have decayed by ~1%%, got %v", got)
	}

	// calling it again the same "day" must not decay the stale bookmark a second time
	decayed2, _, err := DecayBookmarks(ctx, deps.DB)
	if err != nil {
		t.Fatal(err)
	}
	if decayed2 != 0 {
		t.Fatalf("expected no further decay on a second call the same day, got %d", decayed2)
	}
	if got := bookmarkRank(t, deps, stale); got < 0.98 {
		t.Fatalf("rank should not have moved on the second call, got %v", got)
	}
}

// A bookmark that has decayed past the negligible-rank floor is deleted outright rather than left to decay
// asymptotically forever.
func TestDecayBookmarksDeletesBelowFloor(t *testing.T) {
	_, deps, _, _ := setup(t)
	ctx := context.Background()
	id, err := SaveBookmark(ctx, deps.DB, Bookmark{URL: "https://almostgone.example", Title: "Almost gone", Rank: 0.005})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.DB.Exec(ctx, `UPDATE bookmarks SET created_at=now()-interval '3 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	_, deleted, err := DecayBookmarks(ctx, deps.DB)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected the near-zero bookmark deleted, got %d", deleted)
	}
	if _, err := deps.DB.Exec(ctx, `SELECT 1 FROM bookmarks WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := deps.DB.QueryRow(ctx, `SELECT count(*) FROM bookmarks WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("bookmark below the decay floor should have been deleted")
	}
}

// EnrichBookmark fetches the page and asks the (fake) model for a description and keywords.
func TestEnrichBookmarkFetchesPageAndAsksModel(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "```json\n" + `{"description":"A tool for comparing prices","keywords":["price","comparison","shopping"]}` + "\n```"}
	}
	r, st := testutil.Setup(t, d, fake)
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>PriceComparo</title></head><body><h1>Compare prices instantly</h1></body></html>`))
	}))
	defer srv.Close()
	deps := Deps{Settings: st, LLM: r}
	sug, err := EnrichBookmark(context.Background(), deps, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if sug.Title != "PriceComparo" {
		t.Fatalf("title: %q", sug.Title)
	}
	if sug.Description != "A tool for comparing prices" || len(sug.Keywords) != 3 {
		t.Fatalf("suggestion: %+v", sug)
	}
}

// A fetch failure is a real error (nothing to suggest); a page that fetches fine but a model call that then
// fails must not take the whole suggestion down with it — the title alone is still useful.
func TestEnrichBookmarkFetchFailureIsAnErrorModelFailureIsNot(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	deps := Deps{Settings: st, LLM: r}

	if _, err := EnrichBookmark(context.Background(), deps, "http://127.0.0.1:1"); err == nil {
		t.Fatal("expected an error when the page cannot be fetched at all")
	}

	fake.Handler = func(req map[string]any, call int) testutil.Reply { return testutil.Reply{Status: 500} }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte(`<html><head><title>T</title></head><body>x</body></html>`))
	}))
	defer srv.Close()
	sug, err := EnrichBookmark(context.Background(), deps, srv.URL)
	if err != nil {
		t.Fatalf("a model failure after a successful fetch must not fail the whole call: %v", err)
	}
	if sug.Title != "T" || sug.Description != "" {
		t.Fatalf("expected the title alone with no description: %+v", sug)
	}
}

func bookmarkIDFromMessage(t *testing.T, msg string) int64 {
	t.Helper()
	var id int64
	if _, err := fmt.Sscanf(msg, "Saved bookmark #%d.", &id); err != nil {
		t.Fatalf("could not parse bookmark id from %q: %v", msg, err)
	}
	return id
}
