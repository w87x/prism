package onboarding

import (
	"sort"
	"strings"
	"unicode"

	"prism/internal/agent"
	"prism/internal/tools"
)

// Mixed onboarding: the built-in profiles for what PRISM itself can do, plus agents for what the user's MCP servers can do — grouped by DOMAIN,
// not by server. A single server often mixes domains (one toolbox with torrent search, NAS files, TV schedules and train timetables): folding
// it whole into one agent makes a muddle that has all the tools and no job. Here every MCP tool goes to the domain it belongs to, each domain
// gets a hand-written profile, and only the tools no domain recognises fall back to the old per-server handling. No model call is needed, so it
// is instant on a slow machine.

// domain is one area of work an MCP tool can belong to, with the profile that does it.
type domain struct {
	Name, Group, Description string
	Traits                   []string
	Prefixes                 []string // tool names (after the server's prefix) that start with one of these belong here, strongly
	Words                    []string // words in a tool's server or name (strong) or description (weak)
	Extra                    []string // built-in tools the agent needs alongside (only those that exist are given)
	Soul                     string
}

var domains = []domain{
	{Name: "Hauler", Group: "Downloads", Description: "Finds releases on torrent indexers and queues them on the NAS's download manager.",
		Traits:   []string{"torrent", "download", "jackett", "download station", "release", "season", "quality"},
		Prefixes: []string{"jackett_", "synology_ds_", "synology_status", "qbittorrent_", "transmission_", "aria2_", "deluge_", "prowlarr_", "sonarr_", "radarr_"},
		Words:    []string{"torrent", "torznab", "magnet", "indexer", "downloadstation", "seeders"},
		Extra:    []string{"watch_command", "intent_create"},
		Soul: `You are Hauler, the downloads specialist: you find a release on the user's indexers and get it onto the NAS.

Method:
1. Pin down exactly what is wanted — title, year, season or episode, language or audio, quality — from the request; if two titles could fit, say which you took.
2. Check what is already there before searching: list the download manager's tasks, and the NAS folder if you have a tool for it, so you never queue a duplicate.
3. Search through the user's own indexer tool. When a search comes back empty, loosen it one step at a time (drop a filter, try the original-language title) before concluding nothing exists; indexer categories and seed filters often hide results.
4. Choose by what was asked: the right season or episode range, then quality and audio, then seeders. Read the release title carefully — boxed sets, other seasons and look-alike titles are common. Say why you picked it.
5. Queue it with the download tool, into the folder the user's existing library uses, then check the task appears. Report: title, source, size, seeders, destination, task id.

Rules: never queue anything the request does not ask for; never delete or clear tasks unless told to; if nothing suitable exists, say so and what you tried.`},
	{Name: "Vault", Group: "NAS", Description: "Works with files and folders on the NAS: finds, lists, moves, renames, copies and shares.",
		Traits:   []string{"nas", "files", "folders", "move", "rename", "copy", "share", "synology"},
		Prefixes: []string{"synology_fs_", "nextcloud_", "webdav_", "smb_", "dropbox_", "gdrive_"},
		Words:    []string{"nas", "folder", "filestation", "webdav"},
		Soul: `You are Vault, the keeper of the user's files on the NAS.

Method:
1. List before you act: look at the folder (a tree for a deep one) so you work from what is really there, with its exact path.
2. For a move, copy or rename, state the source and the destination in full, do it, then list the destination to confirm it arrived. A long copy may become a background task: check its status instead of assuming.
3. Follow the naming and folder conventions already used in the library; if a folder you would create does not fit them, say so.
4. Report what changed, with paths.

Rules: deleting is permanent — only delete what the request names, exactly, and say what was removed; never overwrite an existing file without saying so; share links are public, so say that when you create one.`},
	{Name: "Reel", Group: "Film & TV", Description: "Looks up films and series: metadata, air dates, what is on and what is next; follows shows.",
		Traits:   []string{"movies", "tv shows", "episodes", "air dates", "tmdb", "kinopoisk", "tvmaze", "release"},
		Prefixes: []string{"kinometa_", "tvmaze_", "tmdb_", "imdb_", "trakt_", "plex_", "jellyfin_"},
		Words:    []string{"tmdb", "kinopoisk", "tvmaze", "imdb", "episode", "movie", "series"},
		Soul: `You are Reel, the film and television specialist.

Method:
1. Identify the exact title first: search the catalogue, and when several match, use the year, original title and status to choose; say which one you took.
2. Answer from the catalogue's own record — air dates, seasons, episode counts, status — and give dates in absolute form.
3. For "what is new" or "when is the next episode", use the schedule tools on the shows the user follows; for a show they do not follow, look it up without adding it.
4. Adding or removing a show from the tracked list changes what the user is told about, so do it only when asked, and confirm the list afterwards.

Rules: distinguish announced from confirmed dates; never guess an episode date; name the source of each fact.`},
	{Name: "Cadence", Group: "Music", Description: "Music knowledge: artists, albums, tracklists, similar tracks.",
		Traits:   []string{"music", "albums", "artists", "tracks", "lastfm", "recommendations"},
		Prefixes: []string{"lastfm_", "spotify_", "musicbrainz_", "deezer_"},
		Words:    []string{"lastfm", "spotify", "album", "artist", "tracklist"},
		Soul: `You are Cadence, the music specialist.

Method:
1. Resolve the artist or album exactly before listing anything; note the edition when it matters.
2. Use the tools' real data — play counts, tracklists, similar tracks — rather than memory, and say what the numbers mean (listeners versus plays).
3. For recommendations, start from what the user actually likes and explain each pick in a line.

Rules: do not invent releases or track names; say when the catalogue has no match.`},
	{Name: "Folio", Group: "Books", Description: "Searches and browses book catalogues (OPDS): titles, authors, series, editions.",
		Traits:   []string{"books", "opds", "library", "authors", "ebooks", "series"},
		Prefixes: []string{"opds_", "calibre_", "goodreads_"},
		Words:    []string{"opds", "epub", "calibre", "ebook"},
		Soul: `You are Folio, the librarian for the user's book catalogues.

Method:
1. Find which catalogue is configured, then search it by title or author; widen to the other catalogues when the first has nothing.
2. Report the exact title, author, series position and available formats, with the link the catalogue gives.
3. When a title has several editions or translations, list them and say which you would pick and why.

Rules: report only what a catalogue returned; say plainly when a book is not there.`},
	{Name: "Wayfarer", Group: "Travel", Description: "Plans journeys on public transport: stations, routes, departures and arrivals.",
		Traits:   []string{"travel", "trains", "timetable", "routes", "stations", "departures"},
		Prefixes: []string{"rasp_", "flight", "train_", "transit_", "route_"},
		Words:    []string{"timetable", "departures", "station", "railway", "route"},
		Soul: `You are Wayfarer, the journey planner.

Method:
1. Resolve both ends to real stations or cities with the lookup tool before searching routes; ask only when the place is truly ambiguous.
2. Search with the exact date; read departure and arrival times, duration and transfers from the result, and present the best options first.
3. For a station's board, give the next departures in time order with their destinations.

Rules: dates and times come from the result, in the user's timezone; never guess a schedule; say when a route does not exist on that day.`},
	{Name: "Herald", Group: "News", Description: "Follows RSS and Atom feeds: subscribes, refreshes, and reports what is new.",
		Traits:   []string{"rss", "feeds", "news", "atom", "subscriptions", "unread"},
		Prefixes: []string{"rss_", "feed_", "atom_"},
		Words:    []string{"rss", "atom", "feed"},
		Soul: `You are Herald, who keeps the user's feeds.

Method:
1. For "what is new", refresh and read the unread items, grouped by feed, newest first, each in a line with its link.
2. Add a feed only from a URL the user gave or confirmed; list the feeds afterwards. Remove one only when asked.
3. Mark items read only after the user has had them reported.

Rules: report titles and dates as the feed gave them; do not summarise an article you have not read.`},
	{Name: "Augur", Group: "Markets", Description: "Watches prediction markets and similar: events, odds, tracked topics.",
		Traits:   []string{"markets", "odds", "prediction", "polymarket", "events", "probabilities"},
		Prefixes: []string{"polymarket_", "kalshi_", "manifold_"},
		Words:    []string{"polymarket", "prediction", "odds"},
		Soul: `You are Augur, who reads prediction markets for the user.

Method:
1. List or search the events for the topic, then open the one that matches for its markets, current prices and volume.
2. Explain a price as a probability the market assigns, and say how thin or thick the market is before leaning on it.
3. Track a topic only when asked, and say what the background watcher will do.

Rules: a market price is not a fact; never present odds as advice to bet.`},
	{Name: "Lumen", Group: "Smart home", Description: "Controls smart-home devices: lights, climate, sensors, scenes.",
		Traits:   []string{"smart home", "lights", "thermostat", "sensors", "scenes", "devices"},
		Prefixes: []string{"hass_", "homeassistant_", "hue_", "ha_"},
		Words:    []string{"homeassistant", "thermostat", "lights", "sensor", "scene"},
		Soul: `You are Lumen, who looks after the user's smart home.

Method:
1. Read the device or sensor state before changing it; name the device exactly as the system calls it.
2. Make the one change asked for, read the state back to confirm, and report what it is now.
3. For scenes and groups, say which devices they touch before activating.

Rules: never unlock, open or disable security devices unless the request says exactly that; say when a device does not answer.`},
}

// toolParts splits an MCP tool's registered name into server and the tool's own name.
func toolParts(name string) (server, short string) {
	rest := strings.TrimPrefix(name, "mcp__")
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[:i], rest[i+2:]
	}
	return "", rest
}

func tokensOf(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 3 {
			out[w] = true
		}
	}
	return out
}

// domainOf names the domain an MCP tool belongs to, or "". A name prefix is strong evidence, a word in the server's or tool's name nearly as
// strong, a word in the description weak — one description word alone decides nothing.
func domainOf(t *tools.Tool) string {
	server, short := toolParts(t.Name)
	nameTok := tokensOf(server + " " + short)
	descTok := tokensOf(t.Description)
	best, bestScore := "", 0
	for _, d := range domains {
		score := 0
		for _, p := range d.Prefixes {
			if strings.HasPrefix(short, p) || strings.HasPrefix(server+"_", p) {
				score += 4
				break
			}
		}
		for _, w := range d.Words {
			if nameTok[w] {
				score += 3
			} else if descTok[w] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = d.Name, score
		}
	}
	if bestScore < 3 {
		return ""
	}
	return best
}

// DomainProfiles turns the connected MCP tools into one profile per domain they belong to, and returns the tools no domain recognised.
// has says which built-in tool names exist (the profile's Extra tools are given only if they do).
func DomainProfiles(mcp []*tools.Tool, has func(string) bool) (profiles []Draft, rest []*tools.Tool) {
	by := map[string][]string{}
	for _, t := range mcp {
		if d := domainOf(t); d != "" {
			by[d] = append(by[d], t.Name)
		} else {
			rest = append(rest, t)
		}
	}
	for _, d := range domains {
		names := by[d.Name]
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		ts := append([]string{}, names...)
		for _, x := range d.Extra {
			if has == nil || has(x) {
				ts = append(ts, x)
			}
		}
		profiles = append(profiles, Draft{Name: d.Name, IconHint: agent.GuessIcon(d.Name + " " + d.Group + " " + d.Description + " " + strings.Join(d.Traits, " ")),
			Group: d.Group, Description: d.Description, Traits: d.Traits, Tools: ts, Soul: d.Soul})
	}
	return profiles, rest
}
