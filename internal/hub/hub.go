// Package hub is the WebSocket transport between the Go backend and the Svelte
// UI: request/response RPC plus server-pushed events over a single socket.
package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     int64  `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type event struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// Handler serves one RPC method.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Client is one connected browser tab.
type Client struct {
	conn *websocket.Conn
	send chan []byte
	hub  *Hub
	ctx  context.Context
	sem  chan struct{} // bounds in-flight RPCs so one runaway client cannot exhaust the backend
	id   int64
}

type clientKey struct{}

// ClientFrom returns the connection an RPC arrived on (nil outside a request).
func ClientFrom(ctx context.Context) *Client { c, _ := ctx.Value(clientKey{}).(*Client); return c }

// ID identifies the connection (for editor ownership).
func (c *Client) ID() int64 { return c.id }

// maxInflight is the per-connection cap on concurrently running RPC handlers.
const maxInflight = 48

type Hub struct {
	mu       sync.RWMutex
	clients  map[*Client]struct{}
	handlers map[string]Handler
	// AllowedHosts are additional Host header values accepted (besides loopback).
	AllowedHosts   []string
	AllowedOrigins []string
	// AllowIPHosts accepts any IP-literal Host header. DNS rebinding needs a hostname, so an IP
	// literal cannot be a rebinding attack; this makes a LAN-bound instance reachable by address.
	AllowIPHosts bool
	Token        string // if set, required as ?token= (or the X-Prism-Token header)
	OnConnect    func(c *Client)

	nextID int64
	editor *Client // the one tab allowed to change things (single-user app: other tabs are read-only)
}

func New() *Hub {
	h := &Hub{clients: map[*Client]struct{}{}, handlers: map[string]Handler{}}
	h.init()
	return h
}

func (h *Hub) init() {
	h.Handle("editor.claim", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if c := ClientFrom(ctx); c != nil {
			h.Claim(c)
		}
		return true, nil
	})
}

func (h *Hub) Handle(method string, fn Handler) {
	h.mu.Lock()
	h.handlers[method] = fn
	h.mu.Unlock()
}

// Call parses params into P, invokes fn and returns its result: a typed adapter for Handle.
func Typed[P any, R any](fn func(ctx context.Context, p P) (R, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("bad params: %w", err)
			}
		}
		return fn(ctx, p)
	}
}

// Broadcast pushes an event to every client. Slow clients are disconnected
// (they reconnect and resync) rather than blocking the backend.
func (h *Hub) Broadcast(typ string, data any) {
	b, err := json.Marshal(event{Event: typ, Data: data})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default:
			go c.conn.Close(websocket.StatusPolicyViolation, "slow consumer")
		}
	}
}

// Send pushes an event to a single client.
func (c *Client) Send(typ string, data any) {
	b, err := json.Marshal(event{Event: typ, Data: data})
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	default:
	}
}

func (h *Hub) NumClients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// HostAllowed guards against DNS rebinding: only loopback names and configured hosts are served.
func (h *Hub) HostAllowed(r *http.Request) bool {
	host := hostOnly(r.Host)
	if isLoopback(host) {
		return true
	}
	for _, a := range h.AllowedHosts {
		if strings.EqualFold(hostOnly(a), host) {
			return true
		}
	}
	return h.AllowIPHosts && net.ParseIP(host) != nil
}

// Authorized reports whether the request carries the access token (always true when none is set).
func (h *Hub) Authorized(r *http.Request) bool {
	if h.Token == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if got == "" {
		got = r.Header.Get("X-Prism-Token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.Token)) == 1
}

// ── single editor ───────────────────────────────────────────────────────────

// editorState is what a tab is told about who may edit.
func (h *Hub) editorState(c *Client) map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return map[string]any{"editor": h.editor == c, "taken": h.editor != nil}
}

// Claim makes c the editor; every other tab is told it is now read-only.
func (h *Hub) Claim(c *Client) {
	h.mu.Lock()
	h.editor = c
	h.mu.Unlock()
	h.broadcastEditor()
}

func (h *Hub) broadcastEditor() {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	ed := h.editor
	h.mu.RUnlock()
	for _, c := range clients {
		c.Send("editor", map[string]any{"editor": c == ed, "taken": ed != nil})
	}
}

// ServeHTTP upgrades to a WebSocket and runs the RPC loop.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.HostAllowed(r) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	if !h.Authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	patterns := append([]string{}, h.AllowedOrigins...)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: patterns}) // same-origin always allowed
	if err != nil {
		log.Printf("ws accept: %v", err)
		return
	}
	conn.SetReadLimit(40 << 20) // chat messages can carry a few pictures (base64)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &Client{conn: conn, send: make(chan []byte, 1024), hub: h, ctx: ctx, sem: make(chan struct{}, maxInflight)}
	h.mu.Lock()
	h.nextID++
	c.id = h.nextID
	h.clients[c] = struct{}{}
	if h.editor == nil {
		h.editor = c // the first tab to connect edits; later tabs start read-only
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		lost := h.editor == c
		if lost {
			h.editor = nil
		}
		h.mu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "")
		if lost {
			h.broadcastEditor() // let a waiting tab take over
		}
	}()
	go func() { // writer
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case b := <-c.send:
				wctx, wc := context.WithTimeout(ctx, 15*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wc()
				if err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, pc := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				pc()
				if err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	c.Send("editor", h.editorState(c))
	if h.OnConnect != nil {
		h.OnConnect(c)
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}
		select {
		case c.sem <- struct{}{}:
			go func() {
				defer func() { <-c.sem }()
				h.dispatch(ctx, c, req)
			}()
		default: // shed load instead of piling up goroutines
			b, _ := json.Marshal(response{ID: req.ID, Error: "too many concurrent requests"})
			select {
			case c.send <- b:
			default:
			}
		}
	}
}

func (h *Hub) dispatch(ctx context.Context, c *Client, req request) {
	h.mu.RLock()
	fn := h.handlers[req.Method]
	h.mu.RUnlock()
	resp := response{ID: req.ID}
	if fn == nil {
		resp.Error = "unknown method " + req.Method
	} else {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("rpc %s panic: %v", req.Method, r)
					resp.Error = fmt.Sprintf("internal error: %v", r)
				}
			}()
			res, err := fn(context.WithValue(ctx, clientKey{}, c), req.Params)
			switch {
			case err != nil && errors.Is(err, context.Canceled):
				return
			case err != nil:
				resp.Error = err.Error()
			default:
				resp.Result = res
				if res == nil {
					resp.Result = true
				}
			}
		}()
	}
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(response{ID: req.ID, Error: "encode: " + err.Error()})
	}
	select {
	case c.send <- b:
	case <-ctx.Done():
	}
}
