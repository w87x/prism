// Package kb is the knowledge base: pages written by the model from memory banks on request
// ("a controlled query"), organised in folders. Pages can ask a normal agent to research gaps
// first (enrich), regenerate on their own when memory changed enough, and "smart" folders can
// propose the pages that belong inside them.
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/agent"
	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/tools"
)

type Folder struct {
	ID         int64      `json:"id"`
	ParentID   *int64     `json:"parent_id"`
	Name       string     `json:"name"`
	Query      string     `json:"query"`
	Smart      bool       `json:"smart"`
	MaxPages   int        `json:"max_pages"`
	ExpandedAt *time.Time `json:"expanded_at,omitempty"`
}

type Page struct {
	ID          int64      `json:"id"`
	FolderID    *int64     `json:"folder_id"`
	Title       string     `json:"title"`
	Query       string     `json:"query"`
	Body        string     `json:"body,omitempty"`
	Enrich      bool       `json:"enrich"`
	Auto        bool       `json:"auto"`
	Agent       string     `json:"agent"`
	Status      string     `json:"status"` // empty | generating | ready | error
	Error       string     `json:"error"`
	Sources     []int64    `json:"sources"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Config is stored under settings key "kb".
type Config struct {
	Enabled     bool   `json:"enabled"`
	RegenEveryH int    `json:"regen_every_h"` // minimum age before an automatic regeneration
	MinChanges  int    `json:"min_changes"`   // relevant facts added/retired since the last build
	SmartExpand bool   `json:"smart_expand"`  // smart folders may create new pages
	Model       string `json:"model"`         // model or list that writes pages; empty = the fast (auxiliary) role
}

func DefaultConfig() Config {
	return Config{Enabled: true, RegenEveryH: 24, MinChanges: 3, SmartExpand: true}
}

type Service struct {
	DB       *pgxpool.Pool
	Memory   *memory.Service
	LLM      *llm.Router
	Engine   *agent.Engine
	Settings *settings.Store
	Emit     func(typ string, data any)
	Logf     func(level, source, format string, args ...any)

	genMu  sync.Mutex // one generation at a time keeps local models responsive
	busyMu sync.Mutex
	busy   map[int64]bool
}

func (s *Service) emit(t string, d any) {
	if s.Emit != nil {
		s.Emit(t, d)
	}
}

// jobTimeout bounds one page or folder job. Local reasoning models are slow, so it is generous;
// the point is only to not hang forever.
const jobTimeout = 90 * time.Minute

// model is the model reference the writer uses: the configured one, else the fast/auxiliary role
// (which itself falls back to the chat model when no fast model is set).
func (s *Service) model(ctx context.Context) string {
	if m := strings.TrimSpace(s.cfg(ctx).Model); m != "" {
		return m
	}
	return s.LLM.RoleRef(ctx, "fast")
}

// writerName is how the knowledge-base writer appears among the running agents.
const writerName = "Scribe"

// stream is a single-shot completion shown live in the UI as a run of the writer: its tokens
// arrive in the thinking panel and the active-agents list (and, via the run's page id, on the
// page itself), exactly like an agent's.
func (s *Service) stream(ctx context.Context, title string, page int64, system, user string, jsonOut bool) (string, error) {
	if s.Engine == nil {
		return s.LLM.Complete(ctx, s.model(ctx), system, user, jsonOut)
	}
	ref := s.model(ctx)
	run := s.Engine.NewRunID()
	s.emit("run.start", map[string]any{"run": run, "agent": writerName, "depth": 0, "title": title, "kb": page})
	msgs := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	window := s.LLM.Window(ctx, ref)
	estIn, estOut, last := (len(system)+len(user))/4, 0, time.Now()
	resp, err := s.LLM.Chat(ctx, ref, llm.Request{Messages: msgs, JSON: jsonOut}, func(d llm.Delta) {
		estOut += (len(d.Content) + len(d.Reasoning)) / 4
		if time.Since(last) > 700*time.Millisecond { // running estimate until the real usage arrives
			last = time.Now()
			s.emit("run.usage", map[string]any{"run": run, "agent": writerName, "tokens_in": estIn, "tokens_out": estOut, "context": estIn + estOut, "window": window})
		}
		if d.Reasoning != "" {
			s.emit("run.delta", map[string]any{"run": run, "agent": writerName, "kind": "thinking", "text": d.Reasoning})
		}
		if d.Content != "" {
			s.emit("run.delta", map[string]any{"run": run, "agent": writerName, "kind": "content", "text": d.Content})
		}
	})
	status, msg, in, out := "done", "", 0, 0
	if err != nil {
		status, msg = "failed", err.Error()
	} else {
		in, out = resp.Usage.Prompt, resp.Usage.Completion
		s.emit("run.usage", map[string]any{"run": run, "agent": writerName, "tokens_in": in, "tokens_out": out, "context": in + out, "window": window})
	}
	s.emit("run.end", map[string]any{"run": run, "agent": writerName, "depth": 0, "status": status, "error": msg, "tokens_in": in, "tokens_out": out})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (s *Service) logf(level, f string, a ...any) {
	if s.Logf != nil {
		s.Logf(level, "kb", f, a...)
	}
}

func (s *Service) cfg(ctx context.Context) Config {
	return settings.Load(ctx, s.Settings, "kb", DefaultConfig())
}

// ── CRUD ────────────────────────────────────────────────────────────────────

const pageCols = `id,folder_id,title,query,enrich,auto,agent,status,error,sources,generated_at,updated_at`

func scanPage(r pgx.Row) (Page, error) {
	var p Page
	err := r.Scan(&p.ID, &p.FolderID, &p.Title, &p.Query, &p.Enrich, &p.Auto, &p.Agent, &p.Status, &p.Error, &p.Sources, &p.GeneratedAt, &p.UpdatedAt)
	if p.Sources == nil {
		p.Sources = []int64{}
	}
	return p, err
}

// Tree returns every folder and page (without bodies).
func (s *Service) Tree(ctx context.Context) (map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,parent_id,name,query,smart,max_pages,expanded_at FROM kb_folders ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	folders := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.Query, &f.Smart, &f.MaxPages, &f.ExpandedAt); err != nil {
			rows.Close()
			return nil, err
		}
		folders = append(folders, f)
	}
	rows.Close()
	prow, err := s.DB.Query(ctx, `SELECT `+pageCols+` FROM kb_pages ORDER BY lower(title)`)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	pages := []Page{}
	for prow.Next() {
		p, err := scanPage(prow)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return map[string]any{"folders": folders, "pages": pages}, prow.Err()
}

func (s *Service) GetPage(ctx context.Context, id int64) (Page, error) {
	p, err := scanPage(s.DB.QueryRow(ctx, `SELECT `+pageCols+` FROM kb_pages WHERE id=$1`, id))
	if err != nil {
		return p, err
	}
	_ = s.DB.QueryRow(ctx, `SELECT body FROM kb_pages WHERE id=$1`, id).Scan(&p.Body)
	return p, nil
}

func (s *Service) FindPage(ctx context.Context, title string) (Page, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM kb_pages WHERE lower(title)=lower($1) ORDER BY id LIMIT 1`, strings.TrimSpace(title)).Scan(&id)
	if err != nil {
		return Page{}, fmt.Errorf("no knowledge page titled %q", title)
	}
	return s.GetPage(ctx, id)
}

func (s *Service) SavePage(ctx context.Context, p Page) (int64, error) {
	p.Title, p.Query = strings.TrimSpace(p.Title), strings.TrimSpace(p.Query)
	if p.Title == "" || p.Query == "" {
		return 0, errors.New("title and query are required")
	}
	var id int64
	var err error
	if p.ID == 0 {
		err = s.DB.QueryRow(ctx, `INSERT INTO kb_pages(folder_id,title,query,enrich,auto,agent) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
			p.FolderID, p.Title, p.Query, p.Enrich, p.Auto, p.Agent).Scan(&id)
	} else {
		id = p.ID
		_, err = s.DB.Exec(ctx, `UPDATE kb_pages SET folder_id=$2,title=$3,query=$4,enrich=$5,auto=$6,agent=$7,updated_at=now() WHERE id=$1`,
			p.ID, p.FolderID, p.Title, p.Query, p.Enrich, p.Auto, p.Agent)
	}
	if err == nil {
		s.emit("kb.update", map[string]any{"page": id})
	}
	return id, err
}

func (s *Service) DeletePage(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM kb_pages WHERE id=$1`, id)
	s.emit("kb.update", nil)
	return err
}

func (s *Service) SaveFolder(ctx context.Context, f Folder) (int64, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" {
		return 0, errors.New("folder name is required")
	}
	if f.MaxPages <= 0 || f.MaxPages > 30 {
		f.MaxPages = 6
	}
	if f.ParentID != nil && f.ID != 0 && *f.ParentID == f.ID {
		return 0, errors.New("a folder cannot contain itself")
	}
	var id int64
	var err error
	if f.ID == 0 {
		err = s.DB.QueryRow(ctx, `INSERT INTO kb_folders(parent_id,name,query,smart,max_pages) VALUES($1,$2,$3,$4,$5) RETURNING id`, f.ParentID, f.Name, f.Query, f.Smart, f.MaxPages).Scan(&id)
	} else {
		id = f.ID
		_, err = s.DB.Exec(ctx, `UPDATE kb_folders SET parent_id=$2,name=$3,query=$4,smart=$5,max_pages=$6 WHERE id=$1`, f.ID, f.ParentID, f.Name, f.Query, f.Smart, f.MaxPages)
	}
	if err == nil {
		s.emit("kb.update", nil)
	}
	return id, err
}

func (s *Service) DeleteFolder(ctx context.Context, id int64) error {
	// pages of the folder (and subfolders) are removed with it
	_, err := s.DB.Exec(ctx, `WITH RECURSIVE t AS (SELECT id FROM kb_folders WHERE id=$1 UNION ALL SELECT f.id FROM kb_folders f JOIN t ON f.parent_id=t.id)
		DELETE FROM kb_pages WHERE folder_id IN (SELECT id FROM t)`, id)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM kb_folders WHERE id=$1`, id)
	s.emit("kb.update", nil)
	return err
}

// ── generation ──────────────────────────────────────────────────────────────

func (s *Service) allBanks(ctx context.Context) []string {
	bs, err := s.Memory.Banks(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range bs {
		if b.Status == "active" {
			out = append(out, b.Label())
		}
	}
	return out
}

const composePrompt = `You write a page of a personal knowledge base in Markdown, using ONLY the numbered memory facts provided.
- Start with a one-paragraph summary, then organised sections with ## headings; use tables or lists where they help.
- Cite the supporting facts inline like [#12]. Never state anything the facts do not support.
- If facts conflict, prefer the newest and mention the change. Add a short "Open questions" section listing what is missing.
- Write in the language the facts are in. No preamble, output the page only.
- Never invent details. URLs, numbers, dates and names may appear only if they are written in a fact. If the query asks for something the facts do not contain (for example links), write "not in memory" for it and list it under "Open questions". Never use placeholder addresses such as example.com.`

var urlRe = regexp.MustCompile(`https?://[^\s<>()\[\]"'` + "`" + `]+`)

// dropInventedLinks removes URLs from body that no source fact contains. Small models asked for a
// list of links that memory does not hold will happily make some up; a fabricated link is worse than
// a missing one. It returns the cleaned text and how many links it removed.
func dropInventedLinks(body, facts string) (string, int) {
	n := 0
	out := urlRe.ReplaceAllStringFunc(body, func(u string) string {
		clean := strings.TrimRight(u, ".,;:!?")
		if strings.Contains(facts, clean) || strings.Contains(facts, strings.TrimRight(clean, "/")) {
			return u
		}
		n++
		return "(link not in memory)" + u[len(clean):]
	})
	if n > 0 {
		out += fmt.Sprintf("\n\n> [!WARNING]\n> %d link(s) in this page were not backed by memory and were removed. Memory holds the titles but not the addresses: turn on **enrich** so an agent looks them up, then regenerate.", n)
	}
	return out, n
}

func factLines(fs []memory.Fact) string {
	var sb strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&sb, "#%d (%s, %s) %s\n", f.ID, f.Bank, f.CreatedAt.Format("2006-01-02"), f.Text)
	}
	return sb.String()
}

// Generate (re)builds a page from memory. It returns once the page is stored.
func (s *Service) Generate(ctx context.Context, id int64) error {
	s.busyMu.Lock()
	if s.busy == nil {
		s.busy = map[int64]bool{}
	}
	if s.busy[id] {
		s.busyMu.Unlock()
		return errors.New("this page is already being generated")
	}
	s.busy[id] = true
	s.busyMu.Unlock()
	defer func() { s.busyMu.Lock(); delete(s.busy, id); s.busyMu.Unlock() }()

	p, err := s.GetPage(ctx, id)
	if err != nil {
		return err
	}
	set := func(status, errMsg string) {
		_, _ = s.DB.Exec(ctx, `UPDATE kb_pages SET status=$2, error=$3, updated_at=now() WHERE id=$1`, id, status, errMsg)
		s.emit("kb.update", map[string]any{"page": id, "status": status})
	}
	set("generating", "")
	s.genMu.Lock()
	defer s.genMu.Unlock()

	fail := func(err error) error {
		set("error", err.Error())
		s.logf("warn", "page %q failed: %v", p.Title, err)
		return err
	}
	facts, err := s.Memory.Find(ctx, memory.FindReq{Query: p.Query, Banks: s.allBanks(ctx), K: 40})
	if err != nil {
		return fail(err)
	}
	if p.Enrich {
		if err := s.enrich(ctx, p, facts); err != nil {
			s.logf("warn", "enrichment of %q: %v (continuing with existing facts)", p.Title, err)
		} else if facts, err = s.Memory.Find(ctx, memory.FindReq{Query: p.Query, Banks: s.allBanks(ctx), K: 40}); err != nil {
			return fail(err)
		}
	}
	var body string
	switch {
	case len(facts) == 0:
		body = "_Memory holds nothing about this yet._\n\nTurn on **enrich** so an agent researches it, or talk to Atlas about it and regenerate later."
	case !s.LLM.HasChat(ctx):
		return fail(errors.New("no chat model configured"))
	default:
		cctx, cancel := context.WithTimeout(ctx, jobTimeout)
		out, err := s.stream(cctx, "KB: "+p.Title, id, composePrompt, "Page title: "+p.Title+"\nQuery: "+p.Query+"\n\nMemory facts:\n"+factLines(facts), false)
		cancel()
		if err != nil {
			return fail(err)
		}
		body = strings.TrimSpace(out)
		if cleaned, n := dropInventedLinks(body, factLines(facts)); n > 0 {
			s.logf("warn", "page %q: removed %d link(s) not found in memory", p.Title, n)
			body = cleaned
		}
	}
	src := make([]int64, len(facts))
	for i, f := range facts {
		src[i] = f.ID
	}
	_, err = s.DB.Exec(ctx, `UPDATE kb_pages SET body=$2, sources=$3, status='ready', error='', generated_at=now(), updated_at=now() WHERE id=$1`, id, body, src)
	if err != nil {
		return fail(err)
	}
	s.emit("kb.update", map[string]any{"page": id, "status": "ready"})
	return nil
}

// enrich asks a regular agent to fill gaps in memory for this page, then waits for it.
func (s *Service) enrich(ctx context.Context, p Page, facts []memory.Fact) error {
	if s.Engine == nil {
		return errors.New("no agent engine")
	}
	who := p.Agent
	if who == "" {
		who = "Atlas"
	}
	input := fmt.Sprintf("Knowledge-base enrichment for the page “%s”.\nQuery: %s\n\nWhat memory already holds:\n%s\nResearch what is missing, outdated or uncertain using your tools (delegate to specialists if useful). Store every durable finding with memory_store (bank project:KB %s, or user/domain where it fits) as self-contained sentences. Reply with a two-line summary of what you added.",
		p.Title, p.Query, orNone(factLines(facts)), p.Title)
	t, err := s.Engine.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "knowledge base", ToAgent: who, Title: "KB: " + p.Title, Input: input})
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	done, err := s.Engine.Tasks.Wait(wctx, t.ID)
	if err != nil {
		return err
	}
	if done.Status != tasks.Done {
		return fmt.Errorf("agent finished %s: %s", done.Status, done.Error)
	}
	return nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(nothing)\n"
	}
	return s
}

// Expand lets a smart folder propose the pages that belong inside it and creates the missing ones.
func (s *Service) Expand(ctx context.Context, folderID int64) ([]Page, error) {
	var f Folder
	if err := s.DB.QueryRow(ctx, `SELECT id,parent_id,name,query,smart,max_pages FROM kb_folders WHERE id=$1`, folderID).Scan(&f.ID, &f.ParentID, &f.Name, &f.Query, &f.Smart, &f.MaxPages); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(f.Query)
	if q == "" {
		return nil, errors.New("this folder has no query to expand")
	}
	if !s.LLM.HasChat(ctx) {
		return nil, errors.New("no chat model configured")
	}
	facts, err := s.Memory.Find(ctx, memory.FindReq{Query: q, Banks: s.allBanks(ctx), K: 30})
	if err != nil {
		return nil, err
	}
	rows, _ := s.DB.Query(ctx, `SELECT title FROM kb_pages WHERE folder_id=$1`, folderID)
	var have []string
	for rows != nil && rows.Next() {
		var t string
		_ = rows.Scan(&t)
		have = append(have, t)
	}
	if rows != nil {
		rows.Close()
	}
	cctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	out, err := s.stream(cctx, "KB folder: "+f.Name, 0, fmt.Sprintf(`You organise a personal knowledge-base folder. The folder "%s" is described as: %s
Propose up to %d distinct pages that belong in it, based on what memory actually knows. Each page has a short "title" and a "query" (a precise request for what the page should cover). Do not repeat existing pages: %s.
Answer JSON only: {"pages":[{"title":"","query":""}]}`, f.Name, q, f.MaxPages, strings.Join(have, "; ")), "Relevant memory facts:\n"+orNone(factLines(facts)), true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Pages []struct{ Title, Query string } `json:"pages"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return nil, fmt.Errorf("unusable proposal: %w", err)
	}
	exists := map[string]bool{}
	for _, h := range have {
		exists[strings.ToLower(h)] = true
	}
	var created []Page
	for _, pg := range parsed.Pages {
		t := strings.TrimSpace(pg.Title)
		if t == "" || strings.TrimSpace(pg.Query) == "" || exists[strings.ToLower(t)] || len(created)+len(have) >= f.MaxPages {
			continue
		}
		exists[strings.ToLower(t)] = true
		id, err := s.SavePage(ctx, Page{FolderID: &folderID, Title: t, Query: pg.Query, Auto: true})
		if err != nil {
			continue
		}
		pp, _ := s.GetPage(ctx, id)
		created = append(created, pp)
	}
	_, _ = s.DB.Exec(ctx, `UPDATE kb_folders SET expanded_at=now() WHERE id=$1`, folderID)
	s.emit("kb.update", nil)
	return created, nil
}

// ── automatic regeneration ──────────────────────────────────────────────────

// changes counts how much relevant memory moved since the page was built: facts that entered the
// top results plus source facts that were retired or deleted.
func (s *Service) changes(ctx context.Context, p Page) int {
	facts, err := s.Memory.Find(ctx, memory.FindReq{Query: p.Query, Banks: s.allBanks(ctx), K: 40})
	if err != nil {
		return 0
	}
	old := map[int64]bool{}
	for _, id := range p.Sources {
		old[id] = true
	}
	n := 0
	for _, f := range facts {
		if !old[f.ID] {
			n++
		}
	}
	var live int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM memory_facts WHERE id=ANY($1) AND valid_to IS NULL`, p.Sources).Scan(&live)
	return n + (len(p.Sources) - live)
}

// Start runs the regeneration loop until ctx ends.
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		first := time.After(2 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return
			case <-first:
				s.sweep(ctx)
			case <-t.C:
				s.sweep(ctx)
			}
		}
	}()
}

func (s *Service) sweep(ctx context.Context) {
	cfg := s.cfg(ctx)
	if !cfg.Enabled || !s.LLM.HasChat(ctx) {
		return
	}
	age := time.Duration(max(cfg.RegenEveryH, 1)) * time.Hour
	if cfg.SmartExpand {
		rows, err := s.DB.Query(ctx, `SELECT id FROM kb_folders WHERE smart AND query<>'' AND (expanded_at IS NULL OR expanded_at < $1)`, time.Now().Add(-age))
		if err == nil {
			var ids []int64
			for rows.Next() {
				var id int64
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				if _, err := s.Expand(ctx, id); err != nil {
					s.logf("warn", "smart folder %d: %v", id, err)
				}
			}
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT `+pageCols+` FROM kb_pages WHERE auto AND status<>'generating' ORDER BY generated_at NULLS FIRST`)
	if err != nil {
		return
	}
	var due []Page
	for rows.Next() {
		if p, err := scanPage(rows); err == nil {
			due = append(due, p)
		}
	}
	rows.Close()
	built := 0
	for _, p := range due {
		if built >= 3 { // spread the work over several sweeps
			return
		}
		switch {
		case p.GeneratedAt == nil:
		case time.Since(*p.GeneratedAt) < age:
			continue
		case s.changes(ctx, p) < max(cfg.MinChanges, 1):
			continue
		}
		if err := s.Generate(ctx, p.ID); err == nil {
			built++
		}
	}
}

// ── agent tools ─────────────────────────────────────────────────────────────

// RegisterTools lets agents read the knowledge base and request pages.
func (s *Service) RegisterTools(reg *tools.Registry) {
	reg.Register(
		&tools.Tool{
			Name: "kb_list", Category: "knowledge", Risk: tools.RiskRead,
			Description: "List the knowledge-base pages (titles and their queries).",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				t, err := s.Tree(ctx)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, p := range t["pages"].([]Page) {
					fmt.Fprintf(&sb, "- %s [%s] — %s\n", p.Title, p.Status, p.Query)
				}
				if sb.Len() == 0 {
					return "The knowledge base is empty.", nil
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "kb_read", Category: "knowledge", Risk: tools.RiskRead,
			Description: "Read a knowledge-base page by title: a synthesized, cited summary of what memory knows about a topic. Cheaper than many memory_find calls.",
			Params:      tools.Obj("title", tools.Str("title", "page title (see kb_list)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Title string }](raw)
				if err != nil {
					return "", err
				}
				p, err := s.FindPage(ctx, a.Title)
				if err != nil {
					return "", err
				}
				if p.Body == "" {
					return "Page exists but has not been generated yet.", nil
				}
				return p.Body, nil
			},
		},
		&tools.Tool{
			Name: "kb_request", Category: "knowledge", Risk: tools.RiskWrite, Auto: true,
			Description: "Create a knowledge-base page from a query over memory and generate it in the background. The page is written ONLY from what memory already holds. If it needs anything memory may lack (news, links, prices, anything current or external), set enrich=true so an agent researches and stores it first.",
			Params:      tools.Obj("title,query", tools.Str("title", "page title"), tools.Str("query", "what the page should cover"), tools.Bool("enrich", "research gaps first")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Title, Query string
					Enrich       bool
				}](raw)
				if err != nil {
					return "", err
				}
				id, err := s.SavePage(ctx, Page{Title: a.Title, Query: a.Query, Enrich: a.Enrich, Auto: true})
				if err != nil {
					return "", err
				}
				go func() { _ = s.Generate(context.Background(), id) }()
				return fmt.Sprintf("Page “%s” created and being generated.", a.Title), nil
			},
		},
	)
}
