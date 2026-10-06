// Command prism runs the PRISM personal assistant backend and serves the web UI.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"prism/internal/app"
	"prism/internal/config"
	"prism/internal/hub"
	"prism/internal/server"
	"prism/web"
)

func main() {
	listen := flag.String("listen", "", "listen address (default from config, 127.0.0.1:7777)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}

	h := hub.New()
	h.AllowedOrigins = cfg.AllowedOrigins
	host, port, _ := net.SplitHostPort(cfg.Listen)
	h.AllowedHosts = append([]string{net.JoinHostPort(host, port)}, cfg.AllowedHosts...)
	// PRISM can run commands on this machine: refuse to expose it beyond loopback without a token.
	if config.Exposed(host) {
		if cfg.NoAuth {
			log.Printf("listening beyond loopback WITHOUT an access token (no_auth): make sure only trusted devices (e.g. your VPN) can reach %s", cfg.Listen)
		} else if cfg.AuthToken == "" {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			cfg.AuthToken = hex.EncodeToString(b)
			_ = cfg.Save()
			log.Printf("listening beyond loopback: generated access token %s (stored in config.json); open the UI with ?token=…", cfg.AuthToken)
		}
		if !cfg.NoAuth {
			h.Token = cfg.AuthToken
		}
		h.AllowIPHosts = true // reachable by LAN address; the token guards it, and IP-literal hosts cannot be DNS-rebound
	}

	a := app.New(cfg, h)
	ui, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: server.New(a, h, ui).Handler(), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		log.Printf("PRISM %s listening on http://%s (data: %s)", app.Version, cfg.Listen, cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()
	// Open the database and start the services in the background: the UI is served at once and shows what the start-up
	// is doing (migrations, vector indexing, …) instead of a browser error until it is all done.
	if cfg.DSN != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			if err := a.Connect(ctx, cfg.DSN); err != nil {
				log.Printf("database not reachable (%v) — starting in setup mode", err)
			}
		}()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Print("shutting down…")
	// Keep serving while we stop, so an open page can show what is happening; but never hang: a stuck step (a
	// subprocess that will not exit, a query that will not finish) must not keep the port, or the restart, waiting.
	a.BeginShutdown()
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		log.Printf("shutdown took longer than 15s (stuck at: %s) — exiting anyway", a.CurrentStage())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Print("stopped")
}
