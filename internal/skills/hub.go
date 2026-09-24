package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"prism/internal/textmatch"
)

// Hub is a remote skill repository (a GitHub repo containing SKILL.md files).
type Hub struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Repo    string `json:"repo"` // "owner/repo" or a github.com URL
	Path    string `json:"path"` // subdirectory to scan
	Ref     string `json:"ref"`  // branch/tag; empty → default
	Enabled bool   `json:"enabled"`
	Trusted bool   `json:"trusted"` // agents may search and install from this hub on their own
}

type Remote struct {
	Name        string `json:"name"`
	Path        string `json:"path"` // directory containing SKILL.md
	Description string `json:"description"`
}

func (s *Store) Hubs(ctx context.Context) ([]Hub, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,repo,path,ref,enabled,trusted FROM skill_hubs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hub
	for rows.Next() {
		var h Hub
		if err := rows.Scan(&h.ID, &h.Name, &h.Repo, &h.Path, &h.Ref, &h.Enabled, &h.Trusted); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) GetHub(ctx context.Context, id int64) (*Hub, error) {
	var h Hub
	err := s.db.QueryRow(ctx, `SELECT id,name,repo,path,ref,enabled,trusted FROM skill_hubs WHERE id=$1`, id).Scan(&h.ID, &h.Name, &h.Repo, &h.Path, &h.Ref, &h.Enabled, &h.Trusted)
	return &h, err
}

func (s *Store) SaveHub(ctx context.Context, h Hub) (int64, error) {
	h.Repo = normalizeRepo(h.Repo)
	if h.Repo == "" || !strings.Contains(h.Repo, "/") {
		return 0, errors.New(`repo must look like "owner/repo"`)
	}
	if h.Name == "" {
		h.Name = h.Repo
	}
	if h.ID == 0 {
		var id int64
		err := s.db.QueryRow(ctx, `INSERT INTO skill_hubs(name,repo,path,ref,enabled,trusted) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, h.Name, h.Repo, strings.Trim(h.Path, "/"), h.Ref, true, h.Trusted).Scan(&id)
		return id, err
	}
	_, err := s.db.Exec(ctx, `UPDATE skill_hubs SET name=$2,repo=$3,path=$4,ref=$5,enabled=$6,trusted=$7 WHERE id=$1`, h.ID, h.Name, h.Repo, strings.Trim(h.Path, "/"), h.Ref, h.Enabled, h.Trusted)
	return h.ID, err
}

func (s *Store) DeleteHub(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM skill_hubs WHERE id=$1`, id)
	return err
}

func normalizeRepo(r string) string {
	r = strings.TrimSpace(r)
	if u, err := url.Parse(r); err == nil && u.Host != "" {
		r = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		parts := strings.Split(r, "/")
		if len(parts) > 2 {
			r = strings.Join(parts[:2], "/")
		}
	}
	return strings.Trim(r, "/")
}

var httpc = &http.Client{Timeout: 30 * time.Second}

func ghGet(ctx context.Context, u, token string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "prism-skills")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return errors.New("GitHub rate limit reached — add a GitHub token in Settings → Skills")
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("GitHub HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func ghRaw(ctx context.Context, repo, ref, p string) ([]byte, error) {
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, ref, escapePath(p))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "prism-skills")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, p)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func (h Hub) tree(ctx context.Context, token string) (ref string, entries []treeEntry, err error) {
	ref = h.Ref
	if ref == "" {
		var repo struct {
			Default string `json:"default_branch"`
		}
		if err := ghGet(ctx, "https://api.github.com/repos/"+h.Repo, token, &repo); err != nil {
			return "", nil, err
		}
		ref = repo.Default
	}
	var t struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := ghGet(ctx, "https://api.github.com/repos/"+h.Repo+"/git/trees/"+url.PathEscape(ref)+"?recursive=1", token, &t); err != nil {
		return "", nil, err
	}
	return ref, t.Tree, nil
}

// browseRemote lists the skills available in a hub, fetching each one's description.
func (s *Store) browseRemote(ctx context.Context, h Hub, token string) ([]Remote, error) {
	ref, entries, err := h.tree(ctx, token)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(h.Path, "/")
	var dirs []string
	for _, e := range entries {
		if e.Type == "blob" && path.Base(e.Path) == "SKILL.md" && (prefix == "" || strings.HasPrefix(e.Path, prefix+"/")) {
			dirs = append(dirs, path.Dir(e.Path))
		}
	}
	sort.Strings(dirs)
	if len(dirs) > 300 {
		dirs = dirs[:300]
	}
	out := make([]Remote, len(dirs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	for i, d := range dirs {
		out[i] = Remote{Name: path.Base(d), Path: d}
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if b, err := ghRaw(ctx, h.Repo, ref, path.Join(d, "SKILL.md")); err == nil {
				_, fm := ParseFrontmatter(string(b))
				if fm["name"] != "" {
					out[i].Name = fm["name"]
				}
				out[i].Description = clip(fm["description"], 200)
			}
		}(i, d)
	}
	wg.Wait()
	return out, nil
}

// Import downloads a skill directory (SKILL.md plus bundled files) into the data dir and registers it.
// When adapt is set the body is rewritten for PRISM through the adapter.
func (s *Store) Import(ctx context.Context, h Hub, remotePath, token string, adapt func(context.Context, string) (string, error)) (*Skill, error) {
	ref, entries, err := h.tree(ctx, token)
	if err != nil {
		return nil, err
	}
	remotePath = strings.Trim(remotePath, "/")
	var files []treeEntry
	for _, e := range entries {
		if e.Type == "blob" && strings.HasPrefix(e.Path, remotePath+"/") && e.Size < 1<<20 {
			files = append(files, e)
		}
	}
	if len(files) > 80 {
		files = files[:80]
	}
	skillMd, err := ghRaw(ctx, h.Repo, ref, path.Join(remotePath, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("no SKILL.md in %s: %w", remotePath, err)
	}
	body, fm := ParseFrontmatter(string(skillMd))
	name := SafeName(firstNonEmpty(fm["name"], path.Base(remotePath)))
	dir := filepath.Join(s.dataDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, remotePath+"/")
		dst := filepath.Join(dir, filepath.Clean("/"+rel))
		if !strings.HasPrefix(dst, dir) {
			continue
		}
		b, err := ghRaw(ctx, h.Repo, ref, f.Path)
		if err != nil {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.WriteFile(dst, b, 0o644)
	}
	_ = body
	sk := Skill{Name: name, Description: fm["description"], Body: string(skillMd), Source: "hub:" + h.Name, Enabled: true, Dir: dir}
	if adapt != nil {
		adapted, err := adapt(ctx, string(skillMd))
		if err != nil {
			return nil, fmt.Errorf("adaptation failed: %w", err)
		}
		sk.Original, sk.Body, sk.Adapted = string(skillMd), adapted, true
		if _, fm2 := ParseFrontmatter(adapted); fm2["description"] != "" {
			sk.Description = fm2["description"]
		}
	}
	id, err := s.Save(ctx, sk)
	if err != nil {
		return nil, err
	}
	return s.GetID(ctx, id)
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

type indexEntry struct {
	at    time.Time
	items []Remote
}

var (
	indexMu    sync.Mutex
	indexCache = map[int64]indexEntry{}
)

// Browse returns a hub's skills, cached for 30 minutes (GitHub is rate-limited and descriptions
// need one request per skill).
func (s *Store) Browse(ctx context.Context, h Hub, token string) ([]Remote, error) {
	indexMu.Lock()
	e, ok := indexCache[h.ID]
	indexMu.Unlock()
	if ok && time.Since(e.at) < 30*time.Minute {
		return e.items, nil
	}
	items, err := s.browseRemote(ctx, h, token)
	if err != nil {
		return nil, err
	}
	indexMu.Lock()
	indexCache[h.ID] = indexEntry{time.Now(), items}
	indexMu.Unlock()
	return items, nil
}

// Hit is a skill found by SearchHubs.
type Hit struct {
	HubID   int64  `json:"hub_id"`
	Hub     string `json:"hub"`
	Trusted bool   `json:"trusted"`
	Remote
}

// SearchHubs ranks the skills of every enabled hub (only trusted ones when trustedOnly) against query.
func (s *Store) SearchHubs(ctx context.Context, query, token string, trustedOnly bool, limit int) ([]Hit, error) {
	hubs, err := s.Hubs(ctx)
	if err != nil {
		return nil, err
	}
	var all []Hit
	var docs []string
	var errs []string
	for _, h := range hubs {
		if !h.Enabled || (trustedOnly && !h.Trusted) {
			continue
		}
		items, err := s.Browse(ctx, h, token)
		if err != nil {
			errs = append(errs, h.Name+": "+err.Error())
			continue
		}
		for _, it := range items {
			all = append(all, Hit{HubID: h.ID, Hub: h.Name, Trusted: h.Trusted, Remote: it})
			docs = append(docs, strings.ReplaceAll(it.Name, "-", " ")+" "+it.Description)
		}
	}
	if len(all) == 0 && len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	if strings.TrimSpace(query) == "" {
		if limit > 0 && len(all) > limit {
			all = all[:limit]
		}
		return all, nil
	}
	var out []Hit
	for _, h := range textmatch.Rank(query, docs, limit) {
		out = append(out, all[h.Index])
	}
	return out, nil
}
