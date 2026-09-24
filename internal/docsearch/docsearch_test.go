package docsearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"prism/internal/settings"
	"prism/internal/testutil"
)

func TestIndexAndSearch(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "travel.md"), []byte("# Portugal\n\nWe plan flights to Lisbon in October and a hotel near Alfama.\n\n## Food\n\nTry pasteis de nata."), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "cooking.md"), []byte("# Pasta\n\nBoil water, add salt, cook spaghetti for nine minutes, serve with tomato sauce."), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ignore.bin"), []byte("nothing"), 0o644)
	s := &Service{DB: d.Pool, VectorOn: d.VectorOn, LLM: r}
	ctx := context.Background()
	id, err := s.SaveSource(ctx, Source{Name: "notes", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	files, chunks, err := s.Index(ctx, id, nil)
	if err != nil || files != 2 || chunks < 2 {
		t.Fatalf("index: files=%d chunks=%d err=%v", files, chunks, err)
	}
	hits, err := s.Search(ctx, "flights to Lisbon hotel", "notes", 3)
	if err != nil || len(hits) == 0 || hits[0].File != "travel.md" {
		t.Fatalf("semantic: %+v %v", hits, err)
	}
	// incremental: unchanged files are skipped; a changed file is re-indexed
	if f, _, _ := s.Index(ctx, id, nil); f != 0 {
		t.Fatalf("second index should skip unchanged files, re-indexed %d", f)
	}
	_ = os.WriteFile(filepath.Join(dir, "cooking.md"), []byte("Bake sourdough bread at 230C."), 0o644)
	if f, _, _ := s.Index(ctx, id, nil); f != 1 {
		t.Fatalf("changed file should be re-indexed, got %d", f)
	}
	// lexical fallback when no embedding model exists
	_ = st.Set(ctx, settings.KeyModelRoles, settings.ModelRoles{Chat: "chat", Fast: "chat"})
	if _, err := d.Exec(ctx, `DELETE FROM models WHERE kind='embedding'`); err != nil {
		t.Fatal(err)
	}
	r.Invalidate()
	hits, err = s.Search(ctx, "sourdough", "", 3)
	if err != nil || len(hits) == 0 || hits[0].File != "cooking.md" {
		t.Fatalf("lexical fallback: %+v %v", hits, err)
	}
	// removing a file prunes its chunks
	_ = os.Remove(filepath.Join(dir, "cooking.md"))
	_, _, _ = s.Index(ctx, id, nil)
	if hits, _ = s.Search(ctx, "sourdough", "", 3); len(hits) != 0 {
		t.Fatalf("deleted file still searchable: %+v", hits)
	}
}
