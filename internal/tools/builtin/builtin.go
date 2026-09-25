// Package builtin holds PRISM's general-purpose tools: clock, bookmarks, shell,
// python, files, rss, downloads, artifacts and lexicon.
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/textmatch"
	"prism/internal/tools"
)

// Deps are the services the built-in tools need.
type Deps struct {
	DB       *pgxpool.Pool
	Settings *settings.Store
	LLM      *llm.Router
	DataDir  string
	Emit     func(typ string, data any)
	// Notify delivers a message to the user (download finished etc.).
	Notify func(ctx context.Context, agent, text string)
}

// allowPrivate reports whether the user let web tools reach local/LAN addresses (Settings → Web).
func (d Deps) allowPrivate(ctx context.Context) bool {
	return settings.Load(ctx, d.Settings, settings.KeyWeb, settings.Web{}).AllowPrivate
}

// FSConfig (settings key "fs") controls where agents may write.
type FSConfig struct {
	WriteRoots []string `json:"write_roots"` // in addition to the data dir
	ReadDeny   []string `json:"read_deny"`   // extra denied path prefixes
}

// Register installs every built-in tool.
func Register(reg *tools.Registry, d Deps) (*Downloader, *ProcessManager) {
	if d.Emit == nil {
		d.Emit = func(string, any) {}
	}
	reg.Register(clockTool(d))
	registerBookmarks(reg, d)
	registerShell(reg, d)
	registerFiles(reg, d)
	registerCode(reg, d)
	registerGit(reg, d)
	registerWorkspaces(reg, d)
	registerGH(reg, d)
	registerRepoInfo(reg, d)
	registerImageFetch(reg, d)
	registerRSS(reg, d)
	dl := registerDownloads(reg, d)
	registerArtifacts(reg, d)
	registerLexicon(reg, d)
	pm := newProcessManager(d)
	registerProcess(reg, pm)
	return dl, pm
}

func clockTool(d Deps) *tools.Tool {
	return &tools.Tool{
		Name: "clock", Category: "core", Base: true, Risk: tools.RiskRead,
		Description: "Current date and time (optionally in a given IANA timezone). Use it instead of guessing dates.",
		Params:      tools.Obj("", tools.Str("timezone", "IANA zone, e.g. Europe/Berlin (default: user's)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Timezone string }](raw)
			if err != nil {
				return "", err
			}
			loc := time.Local
			if a.Timezone == "" {
				g := settings.Load(ctx, d.Settings, settings.KeyGeneral, settings.General{})
				a.Timezone = g.Timezone
			}
			if a.Timezone != "" {
				l, err := time.LoadLocation(a.Timezone)
				if err != nil {
					return "", fmt.Errorf("unknown timezone %q", a.Timezone)
				}
				loc = l
			}
			n := time.Now().In(loc)
			return fmt.Sprintf("%s (%s, week %d, unix %d)", n.Format("Monday 2006-01-02 15:04:05 MST -0700"), loc, isoWeek(n), n.Unix()), nil
		},
	}
}

func isoWeek(t time.Time) int { _, w := t.ISOWeek(); return w }

// ── bookmarks ───────────────────────────────────────────────────────────────

type Bookmark struct {
	ID          int64    `json:"id"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	// Rank drives both bookmark_find's ordering and eventual pruning: an agent-added bookmark starts at 1,
	// is nudged up (×1.01) each time it is actually returned by a search, and decays (×0.99/day) otherwise
	// — see RecordBookmarkUse / DecayBookmarks. A user-added one starts above the agent baseline entirely
	// (see TopmostBookmarkRank): it was saved on purpose, not surfaced by a query match.
	Rank      float64    `json:"rank"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func ListBookmarks(ctx context.Context, db *pgxpool.Pool) ([]Bookmark, error) {
	rows, err := db.Query(ctx, `SELECT id,url,title,description,keywords,rank,last_used,created_at FROM bookmarks ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.URL, &b.Title, &b.Description, &b.Keywords, &b.Rank, &b.LastUsed, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// TopmostBookmarkRank is one above the highest rank any bookmark currently has, so a fresh user-added
// bookmark always sorts ahead of whatever the agents have accumulated.
func TopmostBookmarkRank(ctx context.Context, db *pgxpool.Pool) (float64, error) {
	var top float64
	err := db.QueryRow(ctx, `SELECT COALESCE(MAX(rank),0)+1 FROM bookmarks`).Scan(&top)
	return top, err
}

// SaveBookmark inserts or updates a bookmark. b.Rank is honored only on a fresh insert (the caller decides
// the starting rank — bookmark_add uses 1, a user save uses TopmostBookmarkRank); editing an existing
// bookmark, or re-adding a URL that already exists, never touches its accumulated rank.
func SaveBookmark(ctx context.Context, db *pgxpool.Pool, b Bookmark) (int64, error) {
	if b.Keywords == nil {
		b.Keywords = []string{}
	}
	if b.Rank == 0 {
		b.Rank = 1 // safety default for a caller that forgot to set one
	}
	var id int64
	var err error
	if b.ID == 0 {
		err = db.QueryRow(ctx, `INSERT INTO bookmarks(url,title,description,keywords,rank) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (url) DO UPDATE SET title=EXCLUDED.title, description=EXCLUDED.description, keywords=EXCLUDED.keywords RETURNING id`,
			b.URL, b.Title, b.Description, b.Keywords, b.Rank).Scan(&id)
	} else {
		id = b.ID
		_, err = db.Exec(ctx, `UPDATE bookmarks SET url=$2,title=$3,description=$4,keywords=$5 WHERE id=$1`, b.ID, b.URL, b.Title, b.Description, b.Keywords)
	}
	return id, err
}

// FindBookmarks ranks saved bookmarks against query — all text matches, weighted by rank (usefulness earned
// over time, the same fusion idea as memory's effRank) and re-sorted — and records usage on whatever it
// returns. Shared by the bookmark_find tool and web_search's own "check bookmarks first" pass (see
// internal/web), so a saved link can be surfaced before spending a real search call.
func FindBookmarks(ctx context.Context, db *pgxpool.Pool, query string, limit int) ([]Bookmark, error) {
	all, err := ListBookmarks(ctx, db)
	if err != nil {
		return nil, err
	}
	docs := make([]string, len(all))
	for i, b := range all {
		docs[i] = b.Title + " " + b.Description + " " + strings.Join(b.Keywords, " ") + " " + b.URL
	}
	if limit <= 0 {
		limit = 5
	}
	hits := textmatch.Rank(query, docs, 0)
	if len(hits) == 0 {
		return nil, nil
	}
	for i := range hits {
		w := 0.8 + 0.2*math.Min(all[hits[i].Index].Rank, 5)
		hits[i].Score *= w
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Bookmark, len(hits))
	ids := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = all[h.Index]
		ids[i] = out[i].ID
	}
	_ = RecordBookmarkUse(ctx, db, ids) // best-effort: a ranking nudge failing must not fail the actual lookup
	return out, nil
}

// RecordBookmarkUse bumps the bookmarks actually returned by a search — the signal that they are proving
// useful, not just sitting there. Also marks them used today, so today's decay pass skips them.
func RecordBookmarkUse(ctx context.Context, db *pgxpool.Pool, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := db.Exec(ctx, `UPDATE bookmarks SET rank=rank*1.01, last_used=now() WHERE id=ANY($1)`, ids)
	return err
}

// bookmarkDecayFloor: a rank below this is treated as effectively zero and the bookmark is deleted outright
// rather than left to decay asymptotically forever.
const bookmarkDecayFloor = 0.01

// DecayBookmarks applies one calendar day's decay to bookmarks not returned by any search today (or created
// today — a bookmark gets at least one full day before it can start decaying), then deletes any that have
// decayed past bookmarkDecayFloor. Safe to call more than once a day (each row decays at most once per day,
// tracked in last_decay) — a caller does not need its own once-a-day scheduling.
func DecayBookmarks(ctx context.Context, db *pgxpool.Pool) (decayed, deleted int, err error) {
	tag, err := db.Exec(ctx, `UPDATE bookmarks SET rank=rank*0.99, last_decay=CURRENT_DATE
		WHERE (last_decay IS NULL OR last_decay < CURRENT_DATE)
		AND GREATEST(last_used, created_at)::date < CURRENT_DATE`)
	if err != nil {
		return 0, 0, err
	}
	decayed = int(tag.RowsAffected())
	tag2, err := db.Exec(ctx, `DELETE FROM bookmarks WHERE rank < $1`, bookmarkDecayFloor)
	if err != nil {
		return decayed, 0, err
	}
	return decayed, int(tag2.RowsAffected()), nil
}

func registerBookmarks(reg *tools.Registry, d Deps) {
	reg.Register(
		&tools.Tool{
			Name: "bookmark_find", Category: "bookmarks", Risk: tools.RiskRead,
			Description: "Fast-dial: find saved bookmarks (URL + description) by keywords, without a web search.",
			Params:      tools.Obj("query", tools.Str("query", "keywords"), tools.Int("limit", "max (default 5)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query string
					Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				hits, err := FindBookmarks(ctx, d.DB, a.Query, a.Limit)
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return "No bookmarks match.", nil
				}
				var sb strings.Builder
				for _, b := range hits {
					fmt.Fprintf(&sb, "#%d %s — %s\n  %s [%s]\n", b.ID, b.Title, b.URL, b.Description, strings.Join(b.Keywords, ", "))
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "bookmark_add", Category: "bookmarks", Risk: tools.RiskWrite, Auto: true,
			Description: "Save a URL to the user's bookmarks with a description and keywords for fast lookup.",
			Params: tools.Obj("url,title", tools.Str("url", "the URL"), tools.Str("title", "title"), tools.Str("description", "what it is / when to use it"),
				tools.StrList("keywords", "search keywords")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				b, err := tools.Decode[Bookmark](raw)
				if err != nil {
					return "", err
				}
				if !strings.HasPrefix(b.URL, "http://") && !strings.HasPrefix(b.URL, "https://") {
					return "", fmt.Errorf("url must start with http:// or https://")
				}
				b.Rank = 1 // every agent-added bookmark starts at the same baseline, whatever else was decoded
				id, err := SaveBookmark(ctx, d.DB, b)
				return fmt.Sprintf("Saved bookmark #%d.", id), err
			},
		},
		&tools.Tool{
			Name: "bookmark_delete", Category: "bookmarks", Risk: tools.RiskWrite,
			Description: "Delete a bookmark by id.",
			Params:      tools.Obj("id", tools.Int("id", "bookmark id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				_, err = d.DB.Exec(ctx, `DELETE FROM bookmarks WHERE id=$1`, a.ID)
				return "deleted", err
			},
		},
	)
}

// ── artifacts ───────────────────────────────────────────────────────────────

type Artifact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Mime      string    `json:"mime"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is set on ephemeral artifacts (hand-offs between agents); they are deleted when it passes
	// unless the user keeps them. Tainted ones were written while untrusted content was in scope.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Tainted   bool       `json:"tainted,omitempty"`
}

// ArtifactOpts are the optional properties of a new artifact.
type ArtifactOpts struct {
	TTL       time.Duration // > 0: ephemeral
	Tainted   bool
	SessionID int64
}

const (
	MaxArtifactTTL     = 7 * 24 * time.Hour
	DefaultArtifactTTL = 6 * time.Hour
)

const artifactCols = `id,name,mime,path,size,created_by,created_at,expires_at,tainted`

func scanArtifact(r pgx.Row) (Artifact, error) {
	var a Artifact
	err := r.Scan(&a.ID, &a.Name, &a.Mime, &a.Path, &a.Size, &a.CreatedBy, &a.CreatedAt, &a.ExpiresAt, &a.Tainted)
	return a, err
}

// GetArtifact returns a live (not expired) artifact.
func GetArtifact(ctx context.Context, db *pgxpool.Pool, id int64) (Artifact, error) {
	a, err := scanArtifact(db.QueryRow(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE id=$1 AND (expires_at IS NULL OR expires_at>now())`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, fmt.Errorf("artifact #%d does not exist (or has expired)", id)
	}
	return a, err
}

// KeepArtifact makes an ephemeral artifact permanent.
func KeepArtifact(ctx context.Context, db *pgxpool.Pool, id int64) error {
	t, err := db.Exec(ctx, `UPDATE artifacts SET expires_at=NULL WHERE id=$1 AND (expires_at IS NULL OR expires_at>now())`, id)
	if err == nil && t.RowsAffected() == 0 {
		return fmt.Errorf("artifact #%d does not exist (or has expired)", id)
	}
	return err
}

// PurgeArtifacts deletes expired artifacts and their files; it returns how many went.
func PurgeArtifacts(ctx context.Context, db *pgxpool.Pool) (int, error) {
	rows, err := db.Query(ctx, `DELETE FROM artifacts WHERE expires_at IS NOT NULL AND expires_at<=now() RETURNING path`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			_ = os.Remove(p)
			n++
		}
	}
	return n, rows.Err()
}

// ArtifactsOfSession lists the live artifacts an agent session wrote (for "look at artifact #N" hand-offs).
func ArtifactsOfSession(ctx context.Context, db *pgxpool.Pool, session int64) []Artifact {
	rows, err := db.Query(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE session_id=$1 AND (expires_at IS NULL OR expires_at>now()) ORDER BY id LIMIT 20`, session)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		if a, err := scanArtifact(rows); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func ListArtifacts(ctx context.Context, db *pgxpool.Pool) ([]Artifact, error) {
	rows, err := db.Query(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE expires_at IS NULL OR expires_at>now() ORDER BY id DESC LIMIT 300`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveArtifact writes content under <data>/artifacts and registers it.
func SaveArtifact(ctx context.Context, d Deps, name, mime string, content []byte, by string) (*Artifact, error) {
	return SaveArtifactOpts(ctx, d, name, mime, content, by, ArtifactOpts{})
}

// SaveArtifactOpts is SaveArtifact with a lifetime and provenance. Ephemeral artifacts live in their own folder.
func SaveArtifactOpts(ctx context.Context, d Deps, name, mime string, content []byte, by string, o ArtifactOpts) (*Artifact, error) {
	sub := time.Now().Format("2006-01")
	if o.TTL > 0 {
		_, _ = PurgeArtifacts(ctx, d.DB) // expired hand-offs go whenever a new one is made, not only on the hourly sweep
		sub = "tmp"
		if o.TTL > MaxArtifactTTL {
			o.TTL = MaxArtifactTTL
		}
	}
	dir := filepath.Join(d.DataDir, "artifacts", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := safeName(name)
	p := filepath.Join(dir, fmt.Sprintf("%s-%s", time.Now().Format("0102-150405"), base))
	if err := os.WriteFile(p, content, 0o644); err != nil {
		return nil, err
	}
	if mime == "" {
		mime = "text/plain"
	}
	a := &Artifact{Name: name, Mime: mime, Path: p, Size: int64(len(content)), CreatedBy: by, Tainted: o.Tainted}
	var exp, sess any
	if o.TTL > 0 {
		t := time.Now().Add(o.TTL)
		a.ExpiresAt, exp = &t, t
	}
	if o.SessionID != 0 {
		sess = o.SessionID
	}
	err := d.DB.QueryRow(ctx, `INSERT INTO artifacts(name,mime,path,size,created_by,expires_at,tainted,session_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,created_at`,
		a.Name, a.Mime, a.Path, a.Size, a.CreatedBy, exp, o.Tainted, sess).Scan(&a.ID, &a.CreatedAt)
	if err == nil && d.Emit != nil {
		d.Emit("artifact.new", a)
	}
	return a, err
}

func safeName(n string) string {
	n = filepath.Base(strings.TrimSpace(n))
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, n)
	if n == "" || n == "." {
		n = "artifact.txt"
	}
	return n
}

func registerArtifacts(reg *tools.Registry, d Deps) {
	reg.Register(
		&tools.Tool{
			Name: "artifact_save", Category: "files", Base: true, Risk: tools.RiskWrite, Auto: true,
			Description: "Save a deliverable (report, CSV, markdown, code, JSON) as an artifact and get its id. Two uses: a lasting deliverable for the user (default), " +
				"or a temporary hand-off to another agent — set ttl_minutes (e.g. 120) and the artifact deletes itself afterwards; then tell your requester 'see artifact #N' and they read it with artifact_read. " +
				"Prefer an artifact over pasting a long result into your answer.",
			Params: tools.Obj("name,content", tools.Str("name", "file name with extension"), tools.Str("content", "file content (text)"),
				tools.Str("mime", "MIME type, default inferred text/plain"),
				tools.Int("ttl_minutes", "make it temporary: deleted after this many minutes (max 10080). Omit for a lasting artifact")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Name       string `json:"name"`
					Content    string `json:"content"`
					Mime       string `json:"mime"`
					TTLMinutes int    `json:"ttl_minutes"`
				}](raw)
				if err != nil {
					return "", err
				}
				o := ArtifactOpts{Tainted: env.Tainted, SessionID: env.SessionID}
				if a.TTLMinutes > 0 {
					o.TTL = time.Duration(a.TTLMinutes) * time.Minute
				}
				art, err := SaveArtifactOpts(ctx, d, a.Name, a.Mime, []byte(a.Content), env.Agent, o)
				if err != nil {
					return "", err
				}
				if art.ExpiresAt != nil {
					return fmt.Sprintf("Temporary artifact #%d saved (%d bytes), expires %s. Tell the reader: artifact_read id=%d.", art.ID, art.Size, art.ExpiresAt.Format("15:04 on 2 Jan"), art.ID), nil
				}
				return fmt.Sprintf("Artifact #%d saved at %s (%d bytes).", art.ID, art.Path, art.Size), nil
			},
		},
		&tools.Tool{
			Name: "artifact_read", Category: "files", Base: true, Risk: tools.RiskRead,
			Description: "Read an artifact by id (for example one a colleague pointed you to: 'see artifact #100'). Text is returned in pages (offset/limit in characters); " +
				"for PDF or Office files the path is returned so you can use doc_read. Expired artifacts are gone.",
			Params: tools.Obj("id", tools.Int("id", "artifact id"), tools.Int("offset", "first character (default 0)"), tools.Int("limit", "max characters (default 20000)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID            int64 `json:"id"`
					Offset, Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				art, err := GetArtifact(ctx, d.DB, a.ID)
				if err != nil {
					return "", err
				}
				if art.Tainted && env.Taint != nil { // written while untrusted content was in scope: whoever reads it inherits that
					env.Taint()
				}
				b, err := os.ReadFile(art.Path)
				if err != nil {
					return "", fmt.Errorf("artifact #%d: file is missing", art.ID)
				}
				head := fmt.Sprintf("Artifact #%d %s (%s, %d bytes, by %s)", art.ID, art.Name, art.Mime, art.Size, art.CreatedBy)
				if art.ExpiresAt != nil {
					head += ", expires " + art.ExpiresAt.Format("15:04 on 2 Jan")
				}
				if !utf8.Valid(b) {
					return head + "\nBinary content; file at " + art.Path + " (doc_read opens PDF and Office files).", nil
				}
				r := []rune(string(b))
				if a.Limit <= 0 || a.Limit > 60000 {
					a.Limit = 20000
				}
				if a.Offset < 0 || a.Offset >= len(r) {
					return head + fmt.Sprintf("\n(%d characters)", len(r)), nil
				}
				end := min(a.Offset+a.Limit, len(r))
				out := head + "\n---\n" + string(r[a.Offset:end])
				if end < len(r) {
					out += fmt.Sprintf("\n…[%d more characters; continue with offset=%d]", len(r)-end, end)
				}
				return out, nil
			},
		},
		&tools.Tool{
			Name: "artifact_list", Category: "files", Risk: tools.RiskRead,
			Description: "List recent artifacts.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				as, err := ListArtifacts(ctx, d.DB)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for i, a := range as {
					if i >= 30 {
						break
					}
					life := ""
					if a.ExpiresAt != nil {
						life = ", temporary, expires " + a.ExpiresAt.Format("15:04 on 2 Jan")
					}
					fmt.Fprintf(&sb, "#%d %s (%s, %d B, by %s%s) %s\n", a.ID, a.Name, a.Mime, a.Size, a.CreatedBy, life, a.Path)
				}
				if sb.Len() == 0 {
					return "No artifacts yet.", nil
				}
				return sb.String(), nil
			},
		},
	)
}
