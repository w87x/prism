package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"prism/internal/agent"
)

// Attachments to a chat message: files uploaded from the browser (saved in the agents' workspace) and
// paths on this Mac picked in the folder browser (nothing is copied; the agents read them in place under
// their usual file policy). The message text gets a short note that names them, so both the history and
// the agents see what was attached.
const (
	maxAttachFiles   = 6
	maxAttachFile    = 20 << 20
	maxAttachTotal   = 24 << 20
	maxAttachPaths   = 8
	attachSampleSize = 12
)

func cleanName(n string) string { return agent.CleanFileName(n) }

func humanSize(n int64) string { return agent.HumanSize(n) }

// obsidianSafeTitle makes a title safe to use as a note's file name (no path separators or control
// characters; Vault.resolve confines the rest).
func obsidianSafeTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, strings.TrimSpace(title))
	if title == "" {
		title = "note"
	}
	if len(title) > 120 {
		title = title[:120]
	}
	return title
}

// saveUploads stores uploaded files under <data>/work/uploads/<stamp>/ and returns their note lines.
func (s *Server) saveUploads(files []agent.Upload) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > maxAttachFiles {
		return nil, fmt.Errorf("at most %d files per message", maxAttachFiles)
	}
	total := 0
	for _, f := range files {
		if len(f.Data) == 0 {
			return nil, fmt.Errorf("%s is empty", cleanName(f.Name))
		}
		if len(f.Data) > maxAttachFile {
			return nil, fmt.Errorf("%s is larger than %d MB; use “attach from this Mac” for big files", cleanName(f.Name), maxAttachFile>>20)
		}
		total += len(f.Data)
	}
	if total > maxAttachTotal {
		return nil, fmt.Errorf("attachments add up to more than %d MB", maxAttachTotal>>20)
	}
	return agent.WriteUploads(filepath.Join(s.App.Cfg.DataDir, "work", "uploads", time.Now().Format("20060102-150405.000")), files)
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	return p
}

func (s *Server) canRead(ctx context.Context, p string) error {
	if s.App.Ext.Docs == nil || s.App.Ext.Docs.CanRead == nil {
		return errors.New("file access is not configured")
	}
	return s.App.Ext.Docs.CanRead(ctx, p)
}

// pathNotes vets paths picked on this Mac (same policy as the agents' file tools) and describes them.
func (s *Server) pathNotes(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) > maxAttachPaths {
		return nil, fmt.Errorf("at most %d paths per message", maxAttachPaths)
	}
	var notes []string
	for _, raw := range paths {
		p := expandTilde(strings.TrimSpace(raw))
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%q is not an absolute path", raw)
		}
		p = filepath.Clean(p)
		if err := s.canRead(ctx, p); err != nil {
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		if !info.IsDir() {
			notes = append(notes, fmt.Sprintf("- %s — file, %s", p, humanSize(info.Size())))
			continue
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		var names []string
		n := 0
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			n++
			if len(names) < attachSampleSize {
				nm := e.Name()
				if e.IsDir() {
					nm += "/"
				}
				names = append(names, nm)
			}
		}
		sample := strings.Join(names, ", ")
		if n > len(names) {
			sample += ", …"
		}
		notes = append(notes, fmt.Sprintf("- %s/ — folder, %d items (%s)", strings.TrimRight(p, "/"), n, sample))
	}
	return notes, nil
}

// mapFolders starts a folder map for every attached folder (a summary per file, written in the background) and
// returns the note lines that tell the agents about it.
func (s *Server) mapFolders(ctx context.Context, paths []string) []string {
	if s.App.Ext.Folders == nil {
		return nil
	}
	var notes []string
	for _, raw := range paths {
		p := filepath.Clean(expandTilde(strings.TrimSpace(raw)))
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		m, err := s.App.Ext.Folders.Build(ctx, p)
		if err != nil {
			continue // no fast model, or the folder cannot be mapped: the agents still have file_list / file_search
		}
		state := "being built"
		if m.Status == "done" {
			state = "ready"
		}
		notes = append(notes, fmt.Sprintf("- map of %s/ is %s: folder_map finds files by what they contain (path + query), for you and for anyone you delegate to", strings.TrimRight(p, "/"), state))
	}
	return notes
}

// attachNote composes the note appended to a message.
func attachNote(notes []string) string { return agent.AttachNote(notes) }

type dirEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

type dirListing struct {
	Path    string     `json:"path"`
	Parent  string     `json:"parent"`
	Home    string     `json:"home"`
	Entries []dirEntry `json:"entries"`
	More    int        `json:"more"` // entries not shown
}

// browse lists a directory for the picker; places the agents may not read are refused, denied children hidden.
func (s *Server) browse(ctx context.Context, path string, hidden bool) (*dirListing, error) {
	home, _ := os.UserHomeDir()
	p := strings.TrimSpace(path)
	if p == "" {
		p = home
	}
	p = filepath.Clean(expandTilde(p))
	if !filepath.IsAbs(p) {
		return nil, errors.New("give an absolute path")
	}
	if err := s.canRead(ctx, p); err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		p = filepath.Dir(p)
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := &dirListing{Path: p, Home: home}
	if p != "/" {
		out.Parent = filepath.Dir(p)
	}
	for _, e := range ents {
		if !hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(p, e.Name())
		if s.canRead(ctx, full) != nil {
			continue
		}
		de := dirEntry{Name: e.Name()}
		if fi, err := os.Stat(full); err == nil { // follows symlinks: a linked folder is a folder
			de.Dir, de.Size = fi.IsDir(), fi.Size()
		} else {
			continue
		}
		out.Entries = append(out.Entries, de)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	if len(out.Entries) > 600 {
		out.More = len(out.Entries) - 600
		out.Entries = out.Entries[:600]
	}
	return out, nil
}
