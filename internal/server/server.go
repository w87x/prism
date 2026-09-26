// Package server exposes the HTTP endpoints: the WebSocket RPC hub, artifact
// downloads and the embedded Svelte UI.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"prism/internal/app"
	"prism/internal/hub"
)

type Server struct {
	App *app.App
	Hub *hub.Hub
	UI  fs.FS
}

func New(a *app.App, h *hub.Hub, ui fs.FS) *Server {
	s := &Server{App: a, Hub: h, UI: ui}
	s.registerCore()
	s.registerMemory()
	s.registerLLM()
	s.registerTools()
	s.registerMisc()
	s.registerExtensions()
	h.OnConnect = func(c *hub.Client) {
		c.Send("status", a.Status(context.Background()))
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/ws", s.Hub)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/artifacts/", s.serveArtifact)
	mux.HandleFunc("/oauth/callback", s.oauthCallback)
	mux.HandleFunc("/", s.serveUI)
	return s.guard(mux)
}

// guard rejects requests whose Host is not loopback/configured (DNS rebinding defence).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Hub.HostAllowed(r) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.UI.Open(p); err == nil {
		st, _ := f.Stat()
		f.Close()
		if st != nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.UI, p)
			return
		}
	}
	// a missing asset or file must be a 404: answering with index.html makes the browser reject it with a baffling
	// "not a valid JavaScript/CSS MIME type" (what a binary built without the UI, or with a stale one, shows)
	if strings.HasPrefix(p, "assets/") || path.Ext(p) != "" {
		http.NotFound(w, r)
		return
	}
	if _, err := s.UI.Open("index.html"); err != nil {
		http.Error(w, "The web UI is not built into this binary. Run `make ui` and then `make build`, and restart PRISM.", http.StatusServiceUnavailable)
		return
	}
	// SPA fallback
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.UI, "index.html")
}

// oauthCallback is where an MCP server's authorization page sends the browser back. It is not behind the access
// token (the browser arrives from another site); the one-time, unguessable `state` from PRISM's own sign-in
// is what authorizes it.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	page := func(status int, title, msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.WriteHeader(status)
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>PRISM</title><body style="font:15px ui-monospace,Menlo,monospace;background:#030806;color:#3ee8a6;padding:12vh 10vw"><h2>%s</h2><p style="color:#2bb887;line-height:1.6">%s</p>`, html.EscapeString(title), html.EscapeString(msg))
	}
	if !s.App.Ready() || s.App.Ext.MCP == nil {
		page(http.StatusServiceUnavailable, "PRISM is not ready", "Finish PRISM's setup first.")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		page(http.StatusBadRequest, "Sign-in was not completed", e+" "+q.Get("error_description"))
		return
	}
	name, err := s.App.Ext.MCP.OAuthCallback(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		page(http.StatusBadRequest, "Sign-in failed", err.Error())
		return
	}
	s.App.Emit("mcp.update", nil)
	page(http.StatusOK, "Signed in", "PRISM is now connected to "+name+". You can close this tab and go back to PRISM.")
}

func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.Hub.Authorized(r) { // artifacts are agent output: as private as the rest of the UI
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.App.Ready() {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/artifacts/"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var name, mime, p string
	if err := s.App.DB.QueryRow(r.Context(), `SELECT name,mime,path FROM artifacts WHERE id=$1`, id).Scan(&name, &mime, &p); err != nil {
		http.NotFound(w, r)
		return
	}
	// artifacts are agent-authored: never render them inline as HTML in our origin
	w.Header().Set("Content-Type", mime)
	if strings.Contains(strings.ToLower(mime), "html") || strings.Contains(strings.ToLower(mime), "svg") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
	http.ServeFile(w, r, p)
}

// ── RPC helpers ─────────────────────────────────────────────────────────────

type none struct{}

// rpc registers a typed method that requires a connected database.
func rpc[P any, R any](s *Server, name string, fn func(ctx context.Context, p P) (R, error)) {
	s.Hub.Handle(name, hub.Typed(func(ctx context.Context, p P) (R, error) {
		var zero R
		if !s.App.Ready() {
			return zero, fmt.Errorf("database is not connected yet (finish setup)")
		}
		return fn(ctx, p)
	}))
}

// rpcOpen registers a method available in setup mode too.
func rpcOpen[P any, R any](s *Server, name string, fn func(ctx context.Context, p P) (R, error)) {
	s.Hub.Handle(name, hub.Typed(fn))
}

func toJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
