package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Files that come with a message (uploaded in the web UI, sent over Telegram) are written into the agents'
// workspace, and the message gets a note naming them. These helpers are shared by both front doors.

var unsafeFileName = regexp.MustCompile(`[^\w.\- ()+@,]+`)

// CleanFileName reduces a client-supplied name to a safe base name: no directories, no odd characters, bounded length.
func CleanFileName(n string) string {
	n = filepath.Base(strings.ReplaceAll(strings.TrimSpace(n), "\\", "/"))
	n = strings.Trim(unsafeFileName.ReplaceAllString(n, "_"), ". ")
	if n == "" {
		n = "file"
	}
	if len(n) > 120 {
		ext := filepath.Ext(n)
		if len(ext) > 12 {
			ext = ""
		}
		n = n[:120-len(ext)] + ext
	}
	return n
}

// HumanSize renders a byte count for notes and chips.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// WriteUploads saves files into dir (created if needed, private to the user) under safe names that never
// overwrite each other, and returns one note line per file.
func WriteUploads(dir string, files []Upload) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var notes []string
	used := map[string]bool{}
	for _, f := range files {
		name := CleanFileName(f.Name)
		for base, i := name, 2; used[name]; i++ {
			ext := filepath.Ext(base)
			name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(base, ext), i, ext)
		}
		used[name] = true
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, f.Data, 0o600); err != nil {
			return nil, err
		}
		notes = append(notes, fmt.Sprintf("- %s — %s", p, HumanSize(int64(len(f.Data)))))
	}
	return notes, nil
}

// AttachNote is the note appended to a message that carries files or folders.
func AttachNote(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "Attached (already on disk; open with file_list / file_read, or doc_read for PDF and Office files):\n" + strings.Join(notes, "\n")
}
