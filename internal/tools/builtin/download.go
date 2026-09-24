package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"prism/internal/netguard"
	"prism/internal/tools"
)

type Download struct {
	ID         int64      `json:"id"`
	URL        string     `json:"url"`
	Dest       string     `json:"dest"`
	Status     string     `json:"status"` // queued | running | done | failed | cancelled
	Bytes      int64      `json:"bytes"`
	Total      int64      `json:"total"`
	Error      string     `json:"error"`
	Owner      string     `json:"owner"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	media      bool       // fetch with yt-dlp (video/audio pages) instead of a plain file transfer
	playlist   bool       // with media: take a whole playlist/season, not just one video
}

// Downloader is PRISM's first-class download manager: HTTP(S) natively with
// resume and progress, magnet/torrent/FTP through aria2c when it is installed.
type Downloader struct {
	d       Deps
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

func (dl *Downloader) List(ctx context.Context, limit int) ([]Download, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := dl.d.DB.Query(ctx, `SELECT id,url,dest,status,bytes,total,error,owner,created_at,finished_at FROM downloads ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Download
	for rows.Next() {
		var x Download
		if err := rows.Scan(&x.ID, &x.URL, &x.Dest, &x.Status, &x.Bytes, &x.Total, &x.Error, &x.Owner, &x.CreatedAt, &x.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (dl *Downloader) Get(ctx context.Context, id int64) (Download, error) {
	var x Download
	err := dl.d.DB.QueryRow(ctx, `SELECT id,url,dest,status,bytes,total,error,owner,created_at,finished_at FROM downloads WHERE id=$1`, id).
		Scan(&x.ID, &x.URL, &x.Dest, &x.Status, &x.Bytes, &x.Total, &x.Error, &x.Owner, &x.CreatedAt, &x.FinishedAt)
	return x, err
}

func (dl *Downloader) Cancel(ctx context.Context, id int64) error {
	dl.mu.Lock()
	c := dl.cancels[id]
	dl.mu.Unlock()
	if c != nil {
		c()
		return nil
	}
	_, err := dl.d.DB.Exec(ctx, `UPDATE downloads SET status='cancelled', finished_at=now() WHERE id=$1 AND status IN ('queued','running')`, id)
	return err
}

// Start registers and launches a download; it returns immediately.
func (dl *Downloader) Start(ctx context.Context, rawURL, dest, owner string) (Download, error) {
	return dl.StartOpts(ctx, rawURL, dest, owner, false, false)
}

// StartOpts is Start with the media switches: media forces yt-dlp (it is also chosen automatically for the
// well-known video sites), playlist lets it take a whole playlist or season.
func (dl *Downloader) StartOpts(ctx context.Context, rawURL, dest, owner string, media, playlist bool) (Download, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		return Download{}, fmt.Errorf("invalid URL %q", rawURL)
	}
	if (u.Scheme == "http" || u.Scheme == "https") && !dl.d.allowPrivate(ctx) {
		if err := netguard.CheckURL(ctx, rawURL); err != nil {
			return Download{}, err
		}
	}
	dir := filepath.Join(dl.d.DataDir, "downloads")
	if dest != "" {
		if filepath.IsAbs(expandHome(dest)) {
			dest = expandHome(dest)
			if err := dl.d.canWrite(ctx, dest); err != nil {
				return Download{}, err
			}
		} else {
			dest = filepath.Join(dir, safeName(dest))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Download{}, err
	}
	x := Download{URL: rawURL, Dest: dest, Owner: owner, Status: "queued", media: media || isMediaSite(u), playlist: playlist}
	if err := dl.d.DB.QueryRow(ctx, `INSERT INTO downloads(url,dest,owner,status) VALUES($1,$2,$3,'queued') RETURNING id,created_at`, rawURL, dest, owner).Scan(&x.ID, &x.CreatedAt); err != nil {
		return x, err
	}
	rctx, cancel := context.WithCancel(context.Background())
	dl.mu.Lock()
	dl.cancels[x.ID] = cancel
	dl.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			dl.mu.Lock()
			delete(dl.cancels, x.ID)
			dl.mu.Unlock()
		}()
		dl.run(rctx, x, u, dir)
	}()
	return x, nil
}

func (dl *Downloader) set(id int64, status string, bytes, total int64, errMsg, dest string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var fin any
	if status == "done" || status == "failed" || status == "cancelled" {
		fin = time.Now()
	}
	_, _ = dl.d.DB.Exec(ctx, `UPDATE downloads SET status=$2,bytes=$3,total=$4,error=$5,dest=CASE WHEN $6='' THEN dest ELSE $6 END,finished_at=$7 WHERE id=$1`,
		id, status, bytes, total, errMsg, dest, fin)
	dl.d.Emit("download.progress", map[string]any{"id": id, "status": status, "bytes": bytes, "total": total, "error": errMsg, "dest": dest})
}

func (dl *Downloader) finish(x Download, status string, bytes, total int64, err error, dest string) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	dl.set(x.ID, status, bytes, total, msg, dest)
	if dl.d.Notify != nil && status != "cancelled" {
		text := fmt.Sprintf("Download finished: %s (%s)", filepath.Base(dest), humanBytes(bytes))
		if status == "failed" {
			text = fmt.Sprintf("Download failed: %s — %s", x.URL, msg)
		}
		dl.d.Notify(context.Background(), firstNonEmpty(x.Owner, "Atlas"), text)
	}
}

func humanBytes(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f PB", f)
}

func (dl *Downloader) run(ctx context.Context, x Download, u *url.URL, dir string) {
	if x.media && (u.Scheme == "http" || u.Scheme == "https") {
		dl.runYtdlp(ctx, x, dir)
		return
	}
	if u.Scheme == "magnet" || u.Scheme == "ftp" || u.Scheme == "ftps" || strings.HasSuffix(strings.ToLower(u.Path), ".torrent") {
		dl.runAria(ctx, x, dir)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		dl.finish(x, "failed", 0, 0, fmt.Errorf("unsupported scheme %q", u.Scheme), x.Dest)
		return
	}
	dl.set(x.ID, "running", 0, 0, "", "")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, x.URL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (PRISM downloader)")
	dest := x.Dest
	var have int64
	if dest != "" {
		if st, err := os.Stat(dest + ".part"); err == nil {
			have = st.Size()
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
	}
	hc := netguard.Client(dl.d.allowPrivate(ctx), 0)
	resp, err := hc.Do(req)
	if err != nil {
		dl.finish(x, statusFor(ctx, err), 0, 0, err, dest)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		have = 0
		resp.Body.Close()
		req.Header.Del("Range")
		if resp, err = hc.Do(req); err != nil {
			dl.finish(x, statusFor(ctx, err), 0, 0, err, dest)
			return
		}
		defer resp.Body.Close()
	}
	if resp.StatusCode/100 != 2 {
		dl.finish(x, "failed", 0, 0, fmt.Errorf("HTTP %d", resp.StatusCode), dest)
		return
	}
	if dest == "" {
		dest = filepath.Join(dir, safeName(filenameFor(resp, u)))
		if have == 0 {
			// avoid clobbering: add a suffix when the name is taken
			for i := 1; fileExists(dest); i++ {
				ext := filepath.Ext(dest)
				dest = strings.TrimSuffix(filepath.Join(dir, safeName(filenameFor(resp, u))), ext) + fmt.Sprintf(" (%d)", i) + ext
			}
		}
	}
	flags := os.O_WRONLY | os.O_CREATE
	if resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		have = 0
	}
	f, err := os.OpenFile(dest+".part", flags, 0o644)
	if err != nil {
		dl.finish(x, "failed", 0, 0, err, dest)
		return
	}
	total := resp.ContentLength
	if total > 0 {
		total += have
	}
	written := have
	buf := make([]byte, 128<<10)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				dl.finish(x, "failed", written, total, werr, dest)
				return
			}
			written += int64(n)
		}
		if time.Since(last) > time.Second {
			last = time.Now()
			dl.set(x.ID, "running", written, total, "", dest)
		}
		if rerr != nil {
			f.Close()
			switch {
			case rerr == io.EOF:
				if err := os.Rename(dest+".part", dest); err != nil {
					dl.finish(x, "failed", written, total, err, dest)
					return
				}
				dl.finish(x, "done", written, max(total, written), nil, dest)
			default:
				dl.finish(x, statusFor(ctx, rerr), written, total, rerr, dest)
			}
			return
		}
	}
}

func statusFor(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func filenameFor(resp *http.Response, u *url.URL) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
			return params["filename"]
		}
	}
	if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
		if dec, err := url.PathUnescape(b); err == nil {
			return dec
		}
		return b
	}
	return "download-" + time.Now().Format("20060102-150405")
}

var pctRe = regexp.MustCompile(`\((\d+)%\)`)
var sizeRe = regexp.MustCompile(`([\d.]+)([KMG]?i?B)/([\d.]+)([KMG]?i?B)`)

func (dl *Downloader) runAria(ctx context.Context, x Download, dir string) {
	bin, err := exec.LookPath("aria2c")
	if err != nil {
		dl.finish(x, "failed", 0, 0, errors.New("magnet/torrent/FTP downloads need aria2c (brew install aria2)"), x.Dest)
		return
	}
	dl.set(x.ID, "running", 0, 0, "", "")
	cmd := exec.CommandContext(ctx, bin, "--dir="+dir, "--summary-interval=1", "--seed-time=0", "--console-log-level=warn", "--file-allocation=none", "-x4", x.URL)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		dl.finish(x, "failed", 0, 0, err, x.Dest)
		return
	}
	sc := bufio.NewScanner(stdout)
	var lastPct int64
	tail := ""
	for sc.Scan() {
		line := sc.Text()
		tail = line
		if m := pctRe.FindStringSubmatch(line); m != nil {
			p, _ := strconv.ParseInt(m[1], 10, 64)
			lastPct = p
			dl.set(x.ID, "running", p, 100, "", "")
		}
	}
	werr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		dl.finish(x, "cancelled", lastPct, 100, ctx.Err(), x.Dest)
	case werr != nil:
		dl.finish(x, "failed", lastPct, 100, fmt.Errorf("aria2c: %v (%s)", werr, tail), x.Dest)
	default:
		dl.finish(x, "done", 100, 100, nil, firstNonEmpty(x.Dest, dir))
	}
}

func registerDownloads(reg *tools.Registry, d Deps) *Downloader {
	dl := &Downloader{d: d, cancels: map[int64]context.CancelFunc{}}
	reg.Register(
		&tools.Tool{
			Name: "download_start", Category: "downloads", Risk: tools.RiskWrite,
			Description: "Download in the background and get a download id at once; the user is notified when it finishes and download_status shows real progress. " +
				"Handles plain files (http/https natively with resume), magnet/torrent/ftp (aria2c) and VIDEO/AUDIO pages — YouTube, Rutube, VK Video, Vimeo, Twitch, Dailymotion, Bilibili and 1000+ more — through yt-dlp (chosen automatically for the well-known sites, or set media). " +
				"For a video use this, not a shell yt-dlp: progress, resume and notification come built in. Set playlist to fetch a whole playlist/season page; filename can be a folder for media.",
			Params: tools.Obj("url", tools.Str("url", "http(s)/ftp/magnet URL, or a video page"), tools.Str("filename", "optional file name or absolute path inside a writable root (a folder when downloading media)"),
				tools.Bool("media", "force yt-dlp (video/audio site)"), tools.Bool("playlist", "with media: download the whole playlist/season, not one video")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL, Filename   string
					Media, Playlist bool
				}](raw)
				if err != nil {
					return "", err
				}
				x, err := dl.StartOpts(ctx, a.URL, a.Filename, env.Agent, a.Media, a.Playlist)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Download #%d started.", x.ID), nil
			},
		},
		&tools.Tool{
			Name: "download_status", Category: "downloads", Risk: tools.RiskRead,
			Description: "Progress of a download by id, or the list of recent downloads.",
			Params:      tools.Obj("", tools.Int("id", "download id (omit to list)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				var xs []Download
				if a.ID != 0 {
					x, err := dl.Get(ctx, a.ID)
					if err != nil {
						return "", err
					}
					xs = []Download{x}
				} else if xs, err = dl.List(ctx, 15); err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, x := range xs {
					pct := ""
					if x.Total > 0 {
						pct = fmt.Sprintf(" %d%%", x.Bytes*100/x.Total)
					}
					fmt.Fprintf(&sb, "#%d %s%s — %s → %s %s\n", x.ID, x.Status, pct, x.URL, x.Dest, x.Error)
				}
				if sb.Len() == 0 {
					return "No downloads.", nil
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "download_cancel", Category: "downloads", Risk: tools.RiskWrite,
			Description: "Cancel a running download.",
			Params:      tools.Obj("id", tools.Int("id", "download id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				return "cancelling", dl.Cancel(ctx, a.ID)
			},
		},
	)
	return dl
}
