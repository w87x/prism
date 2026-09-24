// Package docsearch is PRISM's local semantic search: it chunks and embeds text
// files from configured folders (notes, docs) and answers similarity queries,
// using pgvector when available and falling back to in-process cosine or
// full-text search when no embedding model is configured.
package docsearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/db"
	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/textmatch"
	"prism/internal/tools"
)

type Source struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Path      string     `json:"path"`
	Globs     []string   `json:"globs"`
	IndexedAt *time.Time `json:"indexed_at,omitempty"`
	Files     int        `json:"files"`
	Chunks    int        `json:"chunks"`
	// Watch re-indexes the source in the background when its files change.
	Watch bool `json:"watch"`
	// Untrusted marks material that came from outside (downloads, mail attachments, dropped files): what an
	// agent reads from it may carry instructions, so a turn that searched it is tainted.
	Untrusted bool `json:"untrusted"`
}

type Hit struct {
	Source    string  `json:"source"`
	File      string  `json:"file"`
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
	Untrusted bool    `json:"untrusted,omitempty"`
}

type Service struct {
	DB       *pgxpool.Pool
	VectorOn bool
	LLM      *llm.Router
	// Extract reads non-text formats (PDF, docx, epub, pictures…); nil → plain text only.
	Extract *Extractor
	// InboxDir is where dropped and ingested files are kept (the "Inbox" source).
	InboxDir string
	// CanRead vets a path before doc_ingest copies it (the agents' file policy); nil → refuse everything.
	CanRead func(ctx context.Context, path string) error
	// Emit reports background progress to the UI; may be nil.
	Emit func(typ string, data any)
}

func (s *Service) Sources(ctx context.Context) ([]Source, error) {
	rows, err := s.DB.Query(ctx, `SELECT s.id,s.name,s.path,s.globs,s.indexed_at,
		(SELECT count(DISTINCT file) FROM doc_chunks c WHERE c.source_id=s.id),(SELECT count(*) FROM doc_chunks c WHERE c.source_id=s.id),s.watch,s.untrusted FROM doc_sources s ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var x Source
		if err := rows.Scan(&x.ID, &x.Name, &x.Path, &x.Globs, &x.IndexedAt, &x.Files, &x.Chunks, &x.Watch, &x.Untrusted); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) SaveSource(ctx context.Context, x Source) (int64, error) {
	if strings.TrimSpace(x.Name) == "" || strings.TrimSpace(x.Path) == "" {
		return 0, errors.New("name and path are required")
	}
	if len(x.Globs) == 0 {
		x.Globs = []string{"*.md", "*.txt"}
	}
	var id int64
	var err error
	if x.ID == 0 {
		err = s.DB.QueryRow(ctx, `INSERT INTO doc_sources(name,path,globs,watch,untrusted) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (name) DO UPDATE SET path=EXCLUDED.path, globs=EXCLUDED.globs, watch=EXCLUDED.watch, untrusted=EXCLUDED.untrusted RETURNING id`,
			x.Name, x.Path, x.Globs, x.Watch, x.Untrusted).Scan(&id)
	} else {
		id = x.ID
		_, err = s.DB.Exec(ctx, `UPDATE doc_sources SET name=$2,path=$3,globs=$4,watch=$5,untrusted=$6 WHERE id=$1`, x.ID, x.Name, x.Path, x.Globs, x.Watch, x.Untrusted)
	}
	return id, err
}

func (s *Service) DeleteSource(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM doc_sources WHERE id=$1`, id)
	return err
}

var paraSplit = regexp.MustCompile(`\n\s*\n`)

// chunk splits text into ~maxLen pieces at paragraph boundaries, prefixing the nearest heading.
func chunk(text string, maxLen int) []string {
	var out []string
	var cur strings.Builder
	heading := ""
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
		if heading != "" {
			cur.WriteString(heading + "\n")
		}
	}
	for _, p := range paraSplit.Split(text, -1) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "#") {
			if i := strings.IndexByte(p, '\n'); i > 0 {
				heading = strings.TrimSpace(p[:i])
			} else {
				heading = p
			}
		}
		for len(p) > maxLen { // very long paragraph: hard split
			cut := strings.LastIndexAny(p[:maxLen], ".!?\n ")
			if cut < maxLen/2 {
				cut = maxLen
			}
			if cur.Len()+cut > maxLen {
				flush()
			}
			cur.WriteString(p[:cut] + "\n")
			p = strings.TrimSpace(p[cut:])
			flush()
		}
		if cur.Len()+len(p) > maxLen && cur.Len() > len(heading)+1 {
			flush()
		}
		cur.WriteString(p + "\n\n")
	}
	flush()
	return out
}

func matchGlob(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, name); ok {
			return true
		}
	}
	return false
}

// Index (re)indexes a source incrementally: unchanged files are skipped, deleted files pruned.
func (s *Service) Index(ctx context.Context, id int64, progress func(done, total int)) (files, chunks int, err error) {
	var src Source
	if err := s.DB.QueryRow(ctx, `SELECT id,name,path,globs FROM doc_sources WHERE id=$1`, id).Scan(&src.ID, &src.Name, &src.Path, &src.Globs); err != nil {
		return 0, 0, err
	}
	root := src.Path
	if strings.HasPrefix(root, "~/") {
		h, _ := os.UserHomeDir()
		root = filepath.Join(h, root[2:])
	}
	if _, err := os.Stat(root); err != nil {
		return 0, 0, fmt.Errorf("source path not accessible: %w", err)
	}
	known := map[string]time.Time{}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT file, mtime FROM doc_chunks WHERE source_id=$1`, id)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var f string
		var m time.Time
		_ = rows.Scan(&f, &m)
		known[f] = m
	}
	rows.Close()
	var todo []string
	present := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if !matchGlob(src.Globs, d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxSize(strings.ToLower(filepath.Ext(d.Name()))) || info.Size() == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		present[rel] = true
		if m, ok := known[rel]; !ok || !m.Equal(info.ModTime().Truncate(time.Microsecond)) {
			todo = append(todo, rel)
		}
		return nil
	})
	for f := range known {
		if !present[f] {
			_, _ = s.DB.Exec(ctx, `DELETE FROM doc_chunks WHERE source_id=$1 AND file=$2`, id, f)
		}
	}
	embed := s.LLM.HasEmbedding(ctx)
	hashes := map[string]string{}
	if hr, err := s.DB.Query(ctx, `SELECT DISTINCT file, hash FROM doc_chunks WHERE source_id=$1`, id); err == nil {
		for hr.Next() {
			var f, h string
			_ = hr.Scan(&f, &h)
			hashes[f] = h
		}
		hr.Close()
	}
	for i, rel := range todo {
		if progress != nil {
			progress(i, len(todo))
		}
		p := filepath.Join(root, rel)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		text, err := s.Extract.Text(ctx, p)
		if err != nil || text == "" {
			if s.Emit != nil && err != nil {
				s.Emit("docs.skipped", map[string]any{"source": src.Name, "file": rel, "error": err.Error()})
			}
			continue
		}
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:])
		if hashes[rel] == hash { // saved again, but the words are the same: only remember the new mtime
			_, _ = s.DB.Exec(ctx, `UPDATE doc_chunks SET mtime=$3 WHERE source_id=$1 AND file=$2`, id, rel, info.ModTime().Truncate(time.Microsecond))
			continue
		}
		cs := chunk(text, 900)
		var vecs [][]float32
		if embed && len(cs) > 0 {
			for j := 0; j < len(cs); j += 24 {
				v, err := s.LLM.Embed(ctx, "", cs[j:min(j+24, len(cs))])
				if err != nil {
					embed = false // stop trying this run; lexical search still works
					vecs = nil
					break
				}
				vecs = append(vecs, v...)
			}
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return files, chunks, err
		}
		_, _ = tx.Exec(ctx, `DELETE FROM doc_chunks WHERE source_id=$1 AND file=$2`, id, rel)
		mt := info.ModTime().Truncate(time.Microsecond)
		for j, c := range cs {
			var emb []byte
			var lit any
			if vecs != nil && j < len(vecs) {
				nv := memory.Normalize(vecs[j])
				emb = memory.EncodeVec(nv)
				if s.VectorOn {
					lit = db.VectorLiteral(nv)
				}
			}
			if s.VectorOn {
				_, err = tx.Exec(ctx, `INSERT INTO doc_chunks(source_id,file,mtime,ord,text,embedding,vec,hash) VALUES($1,$2,$3,$4,$5,$6,$7::vector,$8)`, id, rel, mt, j, c, emb, lit, hash)
			} else {
				_, err = tx.Exec(ctx, `INSERT INTO doc_chunks(source_id,file,mtime,ord,text,embedding,hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, rel, mt, j, c, emb, hash)
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				return files, chunks, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return files, chunks, err
		}
		files++
		chunks += len(cs)
	}
	_, _ = s.DB.Exec(ctx, `UPDATE doc_sources SET indexed_at=now() WHERE id=$1`, id)
	if progress != nil {
		progress(len(todo), len(todo))
	}
	return files, chunks, nil
}

var tokRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// Search returns the best chunks for the query (optionally within one source); hits from sources that hold
// outside material are marked Untrusted.
func (s *Service) Search(ctx context.Context, query, source string, limit int) ([]Hit, error) {
	hits, err := s.search(ctx, query, source, limit)
	if err != nil || len(hits) == 0 {
		return hits, err
	}
	rows, qerr := s.DB.Query(ctx, `SELECT name FROM doc_sources WHERE untrusted`)
	if qerr != nil {
		return hits, nil
	}
	defer rows.Close()
	untrusted := map[string]bool{}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		untrusted[n] = true
	}
	for i := range hits {
		hits[i].Untrusted = untrusted[hits[i].Source]
	}
	return hits, nil
}

func (s *Service) search(ctx context.Context, query, source string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 30 {
		limit = 6
	}
	var qv []float32
	if s.LLM.HasEmbedding(ctx) {
		if v, err := s.LLM.Embed(ctx, "", []string{query}); err == nil && len(v) > 0 {
			qv = memory.Normalize(v[0])
		}
	}
	var hits []Hit
	if qv != nil && s.VectorOn {
		rows, err := s.DB.Query(ctx, `SELECT s.name, c.file, c.text, 1 - (c.vec <=> $1::vector) AS sim
			FROM doc_chunks c JOIN doc_sources s ON s.id=c.source_id
			WHERE ($2='' OR s.name=$2) AND c.vec IS NOT NULL AND vector_dims(c.vec)=$3 ORDER BY c.vec <=> $1::vector LIMIT $4`, db.VectorLiteral(qv), source, len(qv), limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h Hit
			if err := rows.Scan(&h.Source, &h.File, &h.Text, &h.Score); err != nil {
				rows.Close()
				return nil, err
			}
			hits = append(hits, h)
		}
		rows.Close()
		return hits, nil
	}
	if qv != nil { // in-process cosine
		rows, err := s.DB.Query(ctx, `SELECT s.name, c.file, c.text, c.embedding FROM doc_chunks c JOIN doc_sources s ON s.id=c.source_id
			WHERE ($1='' OR s.name=$1) AND c.embedding IS NOT NULL`, source)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h Hit
			var emb []byte
			if err := rows.Scan(&h.Source, &h.File, &h.Text, &emb); err != nil {
				rows.Close()
				return nil, err
			}
			if sim := memory.Dot(qv, memory.DecodeVec(emb)); sim > 0 {
				h.Score = sim
				hits = append(hits, h)
			}
		}
		rows.Close()
		sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
		if len(hits) > limit {
			hits = hits[:limit]
		}
		return hits, nil
	}
	// lexical fallback (no embedding model): Postgres full-text with OR semantics
	var terms []string
	for _, t := range tokRe.Split(strings.ToLower(query), -1) {
		if len(t) > 1 {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT s.name, c.file, c.text, ts_rank(c.tsv, to_tsquery('simple',$1)) AS r
		FROM doc_chunks c JOIN doc_sources s ON s.id=c.source_id
		WHERE ($2='' OR s.name=$2) AND c.tsv @@ to_tsquery('simple',$1) ORDER BY r DESC LIMIT $3`, strings.Join(terms, " | "), source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Source, &h.File, &h.Text, &h.Score); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	_ = textmatch.Tokens
	return hits, rows.Err()
}

// RegisterTools installs semantic_search and semantic_index.
func RegisterTools(reg *tools.Registry, s *Service) {
	reg.Register(
		&tools.Tool{
			Name: "semantic_search", Category: "search", Risk: tools.RiskRead,
			Description: "Semantic (meaning-based) search over the user's indexed local documents/notes. Returns the best matching passages with file names. Sources are configured in Settings; use semantic_index to refresh one.",
			Params:      tools.Obj("query", tools.Str("query", "natural-language query"), tools.Str("source", "restrict to a source name"), tools.Int("limit", "max passages (default 6)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query, Source string
					Limit         int
				}](raw)
				if err != nil {
					return "", err
				}
				hits, err := s.Search(ctx, a.Query, a.Source, a.Limit)
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return "No matching passages (is the source indexed?).", nil
				}
				var sb strings.Builder
				for i, h := range hits {
					t := h.Text
					if r := []rune(t); len(r) > 700 {
						t = string(r[:700]) + "…"
					}
					fmt.Fprintf(&sb, "%d. [%s] %s (score %.2f)\n%s\n\n", i+1, h.Source, h.File, h.Score, t)
					if h.Untrusted && env.Taint != nil {
						env.Taint() // dropped or downloaded material may carry instructions: gate what follows
					}
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "doc_read", Category: "search", Risk: tools.RiskRead,
			Description: "Read the text of a document on the user's disk that file_read cannot open: PDF (scans are read with OCR), Word .docx, EPUB, ODT, RTF, HTML, or a picture with text. Returns a window of the text; continue with offset. Only paths the agents may read are accepted. Plain text files: use file_read.",
			Params: tools.Obj("path", tools.Str("path", "file to read (absolute or ~/…)"), tools.Int("offset", "first character (default 0)"),
				tools.Int("limit", "max characters (default 12000)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path          string
					Offset, Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				p := strings.TrimSpace(a.Path)
				if strings.HasPrefix(p, "~/") {
					h, _ := os.UserHomeDir()
					p = filepath.Join(h, p[2:])
				}
				if !filepath.IsAbs(p) {
					return "", errors.New("give an absolute path (or one starting with ~/)")
				}
				if s.CanRead == nil || s.Extract == nil {
					return "", errors.New("document reading is not configured")
				}
				if err := s.CanRead(ctx, p); err != nil {
					return "", err
				}
				info, err := os.Stat(p)
				if err != nil || info.IsDir() {
					return "", fmt.Errorf("%s is not a readable file", p)
				}
				if !Supported(p) {
					return "", fmt.Errorf("%s: not a document type doc_read understands (use file_read for text files)", filepath.Base(p))
				}
				text, err := s.Extract.Text(ctx, p)
				if err != nil {
					return "", err
				}
				// files the user attached to the chat are theirs; anything else may be downloaded material carrying instructions
				if uploads := filepath.Join(filepath.Dir(s.InboxDir), "work", "uploads"); !strings.HasPrefix(p, uploads+string(filepath.Separator)) && env.Taint != nil {
					env.Taint()
				}
				r := []rune(text)
				if a.Limit <= 0 || a.Limit > 40000 {
					a.Limit = 12000
				}
				if a.Offset < 0 || a.Offset >= len(r) {
					return fmt.Sprintf("(document has %d characters)", len(r)), nil
				}
				end := min(a.Offset+a.Limit, len(r))
				out := string(r[a.Offset:end])
				if end < len(r) {
					out += fmt.Sprintf("\n…[%d more characters; continue with offset=%d]", len(r)-end, end)
				}
				return out, nil
			},
		},
		&tools.Tool{
			Name: "doc_ingest", Category: "search", Risk: tools.RiskWrite,
			Description: "Add a document from the user's disk to the searchable Inbox: PDF (scans are read with OCR), Word .docx, EPUB, ODT, RTF, HTML, Markdown/text, CSV, or a picture with text. The file is copied into PRISM's inbox and indexed; afterwards semantic_search finds it. Only paths the agents may read are accepted.",
			Params:      tools.Obj("path", tools.Str("path", "file to add (absolute or ~/…)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Path string }](raw)
				if err != nil {
					return "", err
				}
				p := strings.TrimSpace(a.Path)
				if strings.HasPrefix(p, "~/") {
					h, _ := os.UserHomeDir()
					p = filepath.Join(h, p[2:])
				}
				if !filepath.IsAbs(p) {
					return "", errors.New("give an absolute path (or one starting with ~/)")
				}
				if s.CanRead == nil {
					return "", errors.New("file access is not configured")
				}
				if err := s.CanRead(ctx, p); err != nil {
					return "", err
				}
				info, err := os.Stat(p)
				if err != nil || info.IsDir() {
					return "", fmt.Errorf("%s is not a readable file", p)
				}
				if info.Size() > maxBinaryFile {
					return "", fmt.Errorf("%s is too large", p)
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", err
				}
				name, chunks, err := s.Ingest(ctx, filepath.Base(p), b)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Added %q to the Inbox (%d searchable passages). Search it with semantic_search (source %q).", name, chunks, InboxName), nil
			},
		},
		&tools.Tool{
			Name: "semantic_index", Category: "search", Risk: tools.RiskWrite, Auto: true,
			Description: "(Re)index a configured document source so semantic_search sees new/changed files.",
			Params:      tools.Obj("source", tools.Str("source", "source name")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Source string }](raw)
				if err != nil {
					return "", err
				}
				srcs, err := s.Sources(ctx)
				if err != nil {
					return "", err
				}
				for _, x := range srcs {
					if strings.EqualFold(x.Name, a.Source) {
						f, c, err := s.Index(ctx, x.ID, nil)
						return fmt.Sprintf("Indexed %d changed files (%d chunks).", f, c), err
					}
				}
				return "", fmt.Errorf("unknown source %q", a.Source)
			},
		},
	)
}

// ── inbox, ingestion and watching ───────────────────────────────────────────

const InboxName = "Inbox"

// EnsureInbox makes sure the Inbox source exists: a watched, untrusted folder that takes every supported format.
func (s *Service) EnsureInbox(ctx context.Context) (Source, error) {
	if s.InboxDir == "" {
		return Source{}, errors.New("no inbox folder configured")
	}
	if err := os.MkdirAll(s.InboxDir, 0o755); err != nil {
		return Source{}, err
	}
	srcs, err := s.Sources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, x := range srcs {
		if x.Name == InboxName {
			return x, nil
		}
	}
	x := Source{Name: InboxName, Path: s.InboxDir, Globs: DocGlobs, Watch: true, Untrusted: true}
	id, err := s.SaveSource(ctx, x)
	x.ID = id
	return x, err
}

var unsafeFile = regexp.MustCompile(`[^\p{L}\p{N} _.()\[\]-]+`)

// safeFileName keeps a dropped file inside the inbox: no directories, no odd characters.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Trim(unsafeFile.ReplaceAllString(name, "_"), ". ")
	if name == "" {
		name = "document"
	}
	if r := []rune(name); len(r) > 120 {
		name = string(r[:100]) + filepath.Ext(name)
	}
	return name
}

func uniqueIn(dir, name string) string {
	p := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
}

// Ingest stores a document in the inbox and indexes it right away; it returns the stored name and what was found.
func (s *Service) Ingest(ctx context.Context, name string, data []byte) (stored string, chunks int, err error) {
	name = safeFileName(name)
	if !Supported(name) {
		return "", 0, fmt.Errorf("%s: this kind of file is not supported (PDF, Word .docx, EPUB, ODT, RTF, HTML, Markdown/text, CSV and pictures are)", name)
	}
	if int64(len(data)) > maxSize(strings.ToLower(filepath.Ext(name))) {
		return "", 0, fmt.Errorf("%s is too large (%d MB)", name, len(data)>>20)
	}
	src, err := s.EnsureInbox(ctx)
	if err != nil {
		return "", 0, err
	}
	p := uniqueIn(s.InboxDir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", 0, err
	}
	_, c, err := s.Index(ctx, src.ID, nil)
	if err != nil {
		return filepath.Base(p), 0, err
	}
	if c == 0 { // nothing readable came out (a scanned file without OCR, an empty document…)
		_ = os.Remove(p)
		return "", 0, fmt.Errorf("%s: no text could be read from it", name)
	}
	return filepath.Base(p), c, nil
}

// WatchLoop re-indexes watched sources on a timer (indexing is incremental, so an idle pass is only a directory walk).
func (s *Service) WatchLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		srcs, err := s.Sources(ctx)
		if err != nil {
			continue
		}
		for _, x := range srcs {
			if !x.Watch {
				continue
			}
			f, c, err := s.Index(ctx, x.ID, nil)
			if s.Emit != nil && (f > 0 || err != nil) {
				s.Emit("docs.indexed", map[string]any{"id": x.ID, "source": x.Name, "files": f, "chunks": c, "error": errString(err)})
			}
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
