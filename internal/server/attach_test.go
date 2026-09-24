package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/agent"
	"prism/internal/app"
	"prism/internal/config"
	"prism/internal/docsearch"
)

// attachServer is a Server with just enough wiring for the attachment helpers: a data dir and a read policy
// that refuses anything under deny.
func attachServer(t *testing.T, deny string) (*Server, string) {
	t.Helper()
	data := t.TempDir()
	s := &Server{App: &app.App{Cfg: &config.Config{DataDir: data}}}
	s.App.Ext.Docs = &docsearch.Service{CanRead: func(_ context.Context, p string) error {
		if deny != "" && (p == deny || strings.HasPrefix(p, deny+string(filepath.Separator))) {
			return errors.New("path is protected")
		}
		return nil
	}}
	return s, data
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":             "report.pdf",
		"../../etc/passwd":       "passwd",
		"..\\..\\win\\sys.ini":   "sys.ini",
		"":                       "file",
		"   ":                    "file",
		"..":                     "file",
		".hidden":                "hidden",
		"a/b/c d (1).txt":        "c d (1).txt",
		"bad:*?\"<>|name.txt":    "bad_name.txt",
		strings.Repeat("x", 300): strings.Repeat("x", 120),
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveUploads(t *testing.T) {
	s, data := attachServer(t, "")
	notes, err := s.saveUploads([]agent.Upload{{Name: "../evil.txt", Data: []byte("a")}, {Name: "evil.txt", Data: []byte("bb")}, {Name: "doc.pdf", Data: []byte("%PDF")}})
	if err != nil || len(notes) != 3 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	up := filepath.Join(data, "work", "uploads")
	for _, n := range notes {
		p := strings.SplitN(strings.TrimPrefix(n, "- "), " — ", 2)[0]
		if !strings.HasPrefix(p, up+string(filepath.Separator)) {
			t.Fatalf("upload escaped the uploads dir: %s", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("not saved: %v", err)
		}
	}
	if !strings.Contains(notes[1], "evil-2.txt") {
		t.Fatalf("same-name uploads must not overwrite each other: %v", notes)
	}
	if _, err := s.saveUploads([]agent.Upload{{Name: "e", Data: nil}}); err == nil {
		t.Fatal("empty file accepted")
	}
	big := make([]byte, maxAttachFile+1)
	if _, err := s.saveUploads([]agent.Upload{{Name: "big", Data: big}}); err == nil {
		t.Fatal("oversized file accepted")
	}
	many := make([]agent.Upload, maxAttachFiles+1)
	for i := range many {
		many[i] = agent.Upload{Name: "f", Data: []byte("x")}
	}
	if _, err := s.saveUploads(many); err == nil {
		t.Fatal("too many files accepted")
	}
}

func TestPathNotesAndBrowse(t *testing.T) {
	root := t.TempDir()
	deny := filepath.Join(root, "secret")
	proj := filepath.Join(root, "proj")
	for _, d := range []string{deny, filepath.Join(proj, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, c := range map[string]string{
		filepath.Join(proj, "README.md"): "hello", filepath.Join(proj, ".env"): "K=1",
		filepath.Join(deny, "key"): "k",
	} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := attachServer(t, deny)
	ctx := context.Background()

	notes, err := s.pathNotes(ctx, []string{proj, filepath.Join(proj, "README.md")})
	if err != nil || len(notes) != 2 {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
	if !strings.Contains(notes[0], "folder, 2 items") || strings.Contains(notes[0], ".env") || !strings.Contains(notes[0], "src/") {
		t.Fatalf("folder note (dotfiles are not listed): %s", notes[0])
	}
	if !strings.Contains(notes[1], "file, 5 bytes") {
		t.Fatalf("file note: %s", notes[1])
	}
	for _, bad := range []string{deny, filepath.Join(deny, "key"), "relative/path", filepath.Join(root, "missing")} {
		if _, err := s.pathNotes(ctx, []string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if !strings.HasPrefix(attachNote(notes), "Attached") || attachNote(nil) != "" {
		t.Fatal("attachNote")
	}

	l, err := s.browse(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "proj" || l.Parent != filepath.Dir(root) {
		t.Fatalf("browse hides denied entries: %v parent=%s", names, l.Parent)
	}
	l, _ = s.browse(ctx, proj, false)
	if len(l.Entries) != 2 || !l.Entries[0].Dir || l.Entries[1].Name != "README.md" {
		t.Fatalf("directories first, dotfiles hidden: %+v", l.Entries)
	}
	if l, _ = s.browse(ctx, proj, true); len(l.Entries) != 3 {
		t.Fatalf("hidden=true should show .env: %+v", l.Entries)
	}
	if _, err := s.browse(ctx, deny, false); err == nil {
		t.Fatal("browsed a protected folder")
	}
	// pointing at a file lists its folder
	if l, err = s.browse(ctx, filepath.Join(proj, "README.md"), false); err != nil || l.Path != proj {
		t.Fatalf("file path: %+v err=%v", l, err)
	}
}
