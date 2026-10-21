package onboarding

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/tools"
)

// the tools of a real MCP server on the user's NAS box: torrents, NAS files, TV, music, books, timetables, feeds, markets — one server, many
// domains. Each must land with the agent whose job it is.
var toolboxTools = [][2]string{
	{"jackett_categories", "Return the static reference table of common Torznab category IDs and their friendly aliase"},
	{"jackett_list_indexers", "List all configured indexers in Jackett with their ID and display name."},
	{"jackett_search", "Search all configured torrent indexers (or a specific one) via Jackett, and return a short"},
	{"kinometa_kp_get", "Get full Kinopoisk metadata for a movie or series by its Kinopoisk ID. Returns name, year,"},
	{"kinometa_kp_search", "Search Kinopoisk for movies/series by title query. Returns id, name, alternativeName, year"},
	{"kinometa_tmdb_get", "Get full TMDB metadata for a movie or TV show by TMDB ID. Returns title, overview, vote av"},
	{"kinometa_tmdb_search", "Search TMDB (The Movie Database) for movies or TV series by title. Returns TMDB id, title,"},
	{"lastfm_get_album_tracks", "Get the full tracklist for a specific album."},
	{"lastfm_get_artist_albums", "Get top albums for an artist, ordered by play count."},
	{"lastfm_get_artist_tracks", "Get top tracks for an artist, ordered by play count."},
	{"lastfm_get_similar_tracks", "Find tracks similar to a list of given tracks. Each track must be in 'Artist - Track' form"},
	{"opds_browse", "Browse an OPDS catalog feed by URL or path. When no path is given, returns the catalog roo"},
	{"opds_info", "Return metadata for one configured catalog: title, root URL, discovered search template, a"},
	{"opds_list_catalogs", "List the configured OPDS catalogs (name, url, and best-effort title/reachable status). Use"},
	{"opds_search", "Search an OPDS catalog for books, either by title/general text (query) or by author name ("},
	{"polymarket_polymarket_list", "List currently active Polymarket events, optionally filtered by a case-insensitive regular"},
	{"polymarket_polymarket_list_subscriptions", "List all currently tracked topics with their filter_regexp and creation date."},
	{"polymarket_polymarket_show", "Full detail for one Polymarket event by id: every market within it, current outcome probab"},
	{"polymarket_polymarket_subscribe", "Start tracking a topic: the background poller will periodically re-run filter_regexp again"},
	{"polymarket_polymarket_unsubscribe", "Stop tracking a topic. Past snapshots/alerts for its markets are kept (other subscriptions"},
	{"rasp_find_station", "Find Yandex.Rasp station/city codes by text query. Returns candidate codes and titles for "},
	{"rasp_schedule", "List upcoming departures or arrivals at a station on a given date. station: Yandex station"},
	{"rasp_search", "Search for transport routes between two points on a given date. from/to: Yandex station or"},
	{"rss_add_feed", "Add an RSS or Atom feed to track. Fetches and parses the feed immediately to validate the "},
	{"rss_list_feeds", "List all tracked feeds with their ID, title, URL, description, total/unread item counts, a"},
	{"rss_mark_all_read", "Mark all items read for one feed (URL/ID) or all feeds if feed is omitted."},
	{"rss_refresh", "Fetch one or all feeds and insert new items (ON CONFLICT DO NOTHING). Respects ETag/If-Mod"},
	{"rss_remove_feed", "Remove a tracked feed and all its items. Accepts a feed URL or numeric ID."},
	{"rss_whats_new", "Refresh feeds, then return unread items grouped by feed, newest first (published desc, nul"},
	{"synology_ds_add", "Add a download task to Download Station. Accepts a magnet link, an http(s) URL, or a stage"},
	{"synology_ds_clear_completed", "Remove finished tasks' records from the Download Station list (the 'Clear Completed' butto"},
	{"synology_ds_complete", "Force a task to complete now (e.g. stop an indefinitely-seeding torrent and move it to the"},
	{"synology_ds_delete", "Remove one or more tasks from the Download Station list. Files already downloaded to the N"},
	{"synology_ds_list", "List Download Station tasks. Per task: id, title, status, total size (human-readable), pro"},
	{"synology_ds_pause", "Pause one or more Download Station tasks."},
	{"synology_ds_resume", "Resume one or more paused Download Station tasks."},
	{"synology_fs_copy", "Copy one or more files/folders to a destination folder on the NAS. Copying is asynchronous"},
	{"synology_fs_delete", "Permanently delete one or more files/folders from the NAS (recursively for folders). This "},
	{"synology_fs_list", "List files/folders on the NAS. Returns full /share/path paths; directories end in /, files"},
	{"synology_fs_mkdir", "Create a folder on the NAS, creating any missing intermediate folders along the way (like "},
	{"synology_fs_move", "Move one or more files/folders to a destination folder on the NAS (source is removed). Sam"},
	{"synology_fs_rename", "Rename a file or folder in place (same parent folder, new basename only). To relocate to a"},
	{"synology_fs_share", "Create a public share link for a file or folder on the NAS. Returns the share URL. Paths u"},
	{"synology_fs_task_status", "Check whether a background fs_copy/fs_move task (one that outran its 60s wait) has finishe"},
	{"synology_fs_tree", "List a folder and its descendants as one full /share/path per line, including the root, li"},
	{"synology_fs_upload", "Upload a file's raw content to a folder on the NAS. Typical use: drop a .torrent file into"},
	{"synology_status", "Return DSM Download Station statistics: current download/upload speeds and task counts."},
	{"tvmaze_add_show", "Add a show to the tracked list. Resolves by name (top search result) or TVMaze ID. Persist"},
	{"tvmaze_get_show_id", "Resolve a show name to its single best-matching TVMaze ID, name, status, network, and prem"},
	{"tvmaze_list_all_shows", "Paginate TVMaze's entire show catalog (NOT the tracked list) via GET /shows?page=N — rough"},
	{"tvmaze_list_shows", "List all currently tracked shows with their TVMaze IDs and air status. No shows are tracke"},
	{"tvmaze_next_episode", "Next upcoming episode for each tracked show (Neuromancer, Futurama, King of the Hill, 3 Bo"},
	{"tvmaze_recent_episode", "Most recently aired episode for each tracked show (Neuromancer, Futurama, King of the Hill"},
	{"tvmaze_remove_show", "Remove a show from the tracked list by name (substring, case-insensitive) or TVMaze ID. Pe"},
	{"tvmaze_schedule", "Episodes airing in the next N days for tracked shows (Neuromancer, Futurama, King of the H"},
	{"tvmaze_search", "Search TVMaze for a show by name. Returns up to 5 fuzzy matches with ID, status, network —"},
	{"tvmaze_show_schedule", "Full episode list for ONE show (any show, not just tracked), grouped by season, marking ea"},
}

func toolboxRegistry() *tools.Registry {
	reg := tools.NewRegistry(nil)
	for _, tt := range toolboxTools {
		reg.Register(&tools.Tool{Name: "mcp__mcp_toolbox__" + tt[0], Category: "mcp:mcp_toolbox", Description: tt[1], Deferred: true, Risk: tools.RiskExec, Params: tools.Obj(""),
			Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
	}
	return reg
}

func TestMixedOnboardingGivesEachMCPDomainItsOwnAgent(t *testing.T) {
	ds := TemplatesWithMCP(toolboxRegistry())
	by := map[string]Draft{}
	for _, d := range ds {
		by[d.Name] = d
	}
	want := map[string]string{
		"jackett_search": "Hauler", "jackett_list_indexers": "Hauler", "synology_ds_add": "Hauler", "synology_ds_list": "Hauler", "synology_status": "Hauler",
		"synology_fs_list": "Vault", "synology_fs_move": "Vault", "synology_fs_tree": "Vault", "synology_fs_task_status": "Vault",
		"kinometa_tmdb_search": "Reel", "tvmaze_schedule": "Reel", "tvmaze_add_show": "Reel",
		"lastfm_get_album_tracks": "Cadence", "opds_search": "Folio", "rasp_search": "Wayfarer", "rss_whats_new": "Herald", "polymarket_polymarket_list": "Augur",
	}
	for tool, agent := range want {
		d, ok := by[agent]
		if !ok {
			t.Fatalf("no %s profile among %d drafts", agent, len(ds))
		}
		if !contains(d.Tools, "mcp__mcp_toolbox__"+tool) {
			t.Errorf("%s should hold %s: %v", agent, tool, d.Tools)
		}
	}
	// every tool of the server is held by exactly one profile, and none is dropped
	for _, tt := range toolboxTools {
		n := 0
		for _, d := range ds {
			if contains(d.Tools, "mcp__mcp_toolbox__"+tt[0]) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s is held by %d profiles", tt[0], n)
		}
	}
	// a domain agent holds only its own domain's tools, plus the few built-ins it needs
	for _, x := range by["Vault"].Tools {
		if strings.Contains(x, "jackett") || strings.Contains(x, "tvmaze") {
			t.Errorf("Vault holds %s", x)
		}
	}
	if !contains(by["Hearth"].Tools, "watch_stop") || !contains(by["Hearth"].Tools, "cron_finish") {
		t.Errorf("Hearth needs the watch lifecycle tools: %v", by["Hearth"].Tools)
	}
}
