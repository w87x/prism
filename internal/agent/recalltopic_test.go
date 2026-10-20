package agent

import (
	"testing"

	"prism/internal/memory"
)

// A task naming one show must not be answered with facts about a different named thing, while facts about its tools, general preferences and
// the same show stay ahead.
func TestRecallPutsFactsAboutOtherNamedThingsBehind(t *testing.T) {
	fs := []memory.Fact{
		{ID: 1, Kind: "fact", Text: "User Danil wants to be alerted when a repack torrent for Call of Duty: Modern Warfare 4 becomes available via Jackett"},
		{ID: 2, Kind: "conclusion", Text: "A cross-bank theme of Pre Structured media ingestion connects folders"},
		{ID: 3, Kind: "fact", Text: "User prefers professional Russian audio dubs for movie torrents"},
		{ID: 4, Kind: "fact", Text: "Danil maintains a Synology DownloadStation instance on his NAS and prefers torrents with high seeder counts"},
		{ID: 5, Kind: "fact", Text: "Rick and Morty seasons are stored under the TV folder in per-season directories"},
	}
	infra := infraWords(&Profile{Tools: []string{"mcp__mcp_toolbox__jackett_search", "mcp__mcp_toolbox__synology_ds_add"}})
	got := memory.RankForRequest("get a torrent of Rick and Morty S01", fs, infra)
	var ids []int64
	for _, f := range got {
		ids = append(ids, f.ID)
	}
	want := []int64{3, 4, 5, 1} // the conclusion is gone; the Call of Duty fact is last
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
	// a task that names nothing specific changes nothing except dropping conclusions
	got = memory.RankForRequest("find me something to watch tonight", fs, infra)
	if len(got) != 4 || got[0].ID != 1 {
		t.Fatalf("an unspecific task keeps the order: %+v", got)
	}
}
