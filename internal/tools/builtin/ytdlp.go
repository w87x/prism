package builtin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Video and audio pages are not files: yt-dlp finds the real streams, merges them and names the result. The
// download manager runs it like it runs aria2c — same download record, same progress, same notification — so
// an agent never needs to babysit a shell yt-dlp with process_status.

var mediaHosts = []string{
	"youtube.com", "youtu.be", "rutube.ru", "vk.com", "vkvideo.ru", "vimeo.com", "dailymotion.com", "twitch.tv",
	"bilibili.com", "ok.ru", "tiktok.com", "instagram.com", "soundcloud.com", "facebook.com", "kinopoisk.ru", "ivi.ru",
}

func isMediaSite(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	for _, m := range mediaHosts {
		if h == m || strings.HasSuffix(h, "."+m) {
			return true
		}
	}
	return false
}

var (
	ytPctRe  = regexp.MustCompile(`\[download\]\s+(\d+(?:\.\d+)?)%`)
	ytItemRe = regexp.MustCompile(`\[download\] Downloading (?:item|video) (\d+) of (\d+)`)
	ytDestRe = regexp.MustCompile(`\[download\] Destination: (.+)$`)
	ytMergeR = regexp.MustCompile(`\[Merger\] Merging formats into "(.+)"`)
)

// ytProgress folds one yt-dlp output line into the overall percentage, tracking which playlist item it is on.
type ytProgress struct {
	item, items int
	pct         float64
	Dest        string
}

// feed consumes a line; it returns the overall percentage (0-100) and whether the line carried progress.
func (y *ytProgress) feed(line string) (float64, bool) {
	if m := ytItemRe.FindStringSubmatch(line); m != nil {
		y.item, _ = strconv.Atoi(m[1])
		y.items, _ = strconv.Atoi(m[2])
		y.pct = 0
		return y.overall(), true
	}
	if m := ytDestRe.FindStringSubmatch(line); m != nil {
		y.Dest = strings.TrimSpace(m[1])
	}
	if m := ytMergeR.FindStringSubmatch(line); m != nil {
		y.Dest = m[1]
	}
	if m := ytPctRe.FindStringSubmatch(line); m != nil {
		y.pct, _ = strconv.ParseFloat(m[1], 64)
		return y.overall(), true
	}
	return 0, false
}

func (y *ytProgress) overall() float64 {
	if y.items > 1 && y.item >= 1 {
		return (float64(y.item-1)*100 + y.pct) / float64(y.items)
	}
	return y.pct
}

func (dl *Downloader) runYtdlp(ctx context.Context, x Download, dir string) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		dl.finish(x, "failed", 0, 0, errors.New("video downloads need yt-dlp (brew install yt-dlp)"), x.Dest)
		return
	}
	// x.Dest: empty → the downloads folder; an existing dir or a name without extension → a folder;
	// otherwise the exact output file.
	out := filepath.Join(dir, "%(title).150B [%(id)s].%(ext)s")
	if x.Dest != "" {
		if st, err := os.Stat(x.Dest); (err == nil && st.IsDir()) || filepath.Ext(x.Dest) == "" {
			_ = os.MkdirAll(x.Dest, 0o755)
			out = filepath.Join(x.Dest, "%(title).150B [%(id)s].%(ext)s")
		} else {
			out = x.Dest
		}
	}
	args := []string{"--newline", "--no-colors", "--no-warnings", "-o", out}
	if x.playlist {
		args = append(args, "--yes-playlist")
	} else {
		args = append(args, "--no-playlist")
	}
	args = append(args, x.URL)
	dl.set(x.ID, "running", 0, 100, "", "")
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		dl.finish(x, "failed", 0, 0, err, x.Dest)
		return
	}
	var yp ytProgress
	var last int64
	tail := ""
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) != "" {
			tail = line
		}
		if p, ok := yp.feed(line); ok && int64(p) != last {
			last = int64(p)
			dl.set(x.ID, "running", last, 100, "", "")
		}
	}
	werr := cmd.Wait()
	dest := firstNonEmpty(firstNonEmpty(yp.Dest, x.Dest), dir)
	switch {
	case ctx.Err() != nil:
		dl.finish(x, "cancelled", last, 100, ctx.Err(), dest)
	case werr != nil:
		dl.finish(x, "failed", last, 100, fmt.Errorf("yt-dlp: %v (%s)", werr, tail), dest)
	default:
		dl.finish(x, "done", 100, 100, nil, dest)
	}
}
