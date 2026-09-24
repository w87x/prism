// Package mcp is a small Model Context Protocol client (stdio and streamable
// HTTP transports). Server tools are registered as deferred tools: only their
// names sit in agent prompts; schemas load on demand through tool_search.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type rpcMsg struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  any              `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Config describes how to reach a server.
type Config struct {
	Name      string
	Transport string // stdio | http
	Command   string
	Args      []string
	Env       map[string]string
	URL       string
	Headers   map[string]string
	// Token supplies the OAuth bearer token (force = the server just rejected the last one: refresh); nil → no OAuth.
	Token func(ctx context.Context, force bool) (string, error)
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnly bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

// Client is one live connection.
type Client struct {
	cfg     Config
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan rpcMsg
	// stdio
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// http
	http      *http.Client
	sessionID string
	closed    atomic.Bool
	Stderr    *tailBuf
}

type tailBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2000 {
		t.buf = t.buf[len(t.buf)-2000:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func expandPath() string {
	p := os.Getenv("PATH")
	for _, x := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		if !strings.Contains(p, x) {
			p += ":" + x
		}
	}
	return p
}

// Connect starts/attaches the transport and performs the initialize handshake.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	c := &Client{cfg: cfg, pending: map[int64]chan rpcMsg{}, Stderr: &tailBuf{}}
	switch cfg.Transport {
	case "http":
		if cfg.URL == "" {
			return nil, errors.New("url is required for http transport")
		}
		c.http = &http.Client{Timeout: 0}
	default:
		if cfg.Command == "" {
			return nil, errors.New("command is required for stdio transport")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = append(os.Environ(), "PATH="+expandPath())
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		cmd.Stderr = c.Stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
		}
		c.cmd, c.stdin = cmd, in
		go c.readLoop(out)
		go func() {
			_ = cmd.Wait()
			c.closed.Store(true)
			c.failAll(errors.New("server process exited: " + c.Stderr.String()))
		}()
	}
	ictx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	res, err := c.call(ictx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "prism", "version": "0.1"},
	})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	_ = json.Unmarshal(res, &init)
	_ = c.notify("notifications/initialized", nil)
	return c, nil
}

func (c *Client) Close() {
	c.closed.Store(true)
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		go func(p *os.Process) {
			time.Sleep(2 * time.Second)
			_ = p.Kill()
		}(c.cmd.Process)
	}
	c.failAll(errors.New("closed"))
}

func (c *Client) Alive() bool { return !c.closed.Load() }

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		select {
		case ch <- rpcMsg{Error: &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{-1, err.Error()}}:
		default:
		}
		delete(c.pending, id)
	}
}

func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		c.dispatch(sc.Bytes())
	}
}

func (c *Client) dispatch(line []byte) {
	var m rpcMsg
	if json.Unmarshal(line, &m) != nil || m.ID == nil {
		return // notifications and garbage are ignored
	}
	if m.Method != "" { // server → client request (roots/sampling…): decline politely
		_ = c.reply(*m.ID, nil, "method not supported")
		return
	}
	var id int64
	if json.Unmarshal(*m.ID, &id) != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *Client) reply(id json.RawMessage, result any, errMsg string) error {
	m := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
	if errMsg != "" {
		m["error"] = map[string]any{"code": -32601, "message": errMsg}
	} else {
		m["result"] = result
	}
	return c.send(m)
}

func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if c.cfg.Transport == "http" {
		return nil // handled in httpCall
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *Client) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	if c.cfg.Transport == "http" {
		_, err := c.httpPost(context.Background(), msg, false)
		return err
	}
	return c.send(msg)
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if c.cfg.Transport == "http" {
		m, err := c.httpPost(ctx, msg, true)
		if err != nil {
			return nil, err
		}
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return m.Result, nil
	}
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.send(msg); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// httpPost implements the streamable-HTTP transport: JSON or SSE replies.
func (c *Client) httpPost(ctx context.Context, msg map[string]any, wantReply bool) (rpcMsg, error) {
	return c.post(ctx, msg, wantReply, false)
}

func (c *Client) post(ctx context.Context, msg map[string]any, wantReply, refreshed bool) (rpcMsg, error) {
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	if c.cfg.Token != nil {
		tok, err := c.cfg.Token(ctx, refreshed)
		if err != nil {
			var ae *AuthRequiredError
			if errors.As(err, &ae) {
				ae.URL = c.cfg.URL
			}
			return rpcMsg{}, err
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	c.mu.Lock()
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	c.mu.Unlock()
	resp, err := c.http.Do(req)
	if err != nil {
		return rpcMsg{}, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}
	if resp.StatusCode == http.StatusUnauthorized {
		if c.cfg.Token != nil && !refreshed { // the token may simply have expired: refresh once and retry
			io.Copy(io.Discard, resp.Body)
			return c.post(ctx, msg, wantReply, true)
		}
		return rpcMsg{}, authRequired(c.cfg.URL, resp.Header)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return rpcMsg{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !wantReply {
		io.Copy(io.Discard, resp.Body)
		return rpcMsg{}, nil
	}
	wantID := msg["id"]
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 32<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m rpcMsg
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &m) == nil && m.ID != nil && m.Method == "" {
				var id int64
				_ = json.Unmarshal(*m.ID, &id)
				if fmt.Sprint(id) == fmt.Sprint(wantID) {
					return m, nil
				}
			}
		}
		return rpcMsg{}, errors.New("stream ended without a response")
	}
	var m rpcMsg
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return rpcMsg{}, err
	}
	return m, nil
}

// ListTools returns every tool the server offers (following pagination).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for i := 0; i < 20; i++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Tools...)
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	return all, nil
}

// CallTool invokes a tool and flattens its content blocks to text.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var a any = map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": a})
	if err != nil {
		return "", err
	}
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return string(raw), nil
	}
	var parts []string
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s content, %s — not shown]", b.Type, b.MimeType))
		case "resource":
			parts = append(parts, b.Resource.URI+"\n"+b.Resource.Text)
		}
	}
	out := strings.Join(parts, "\n")
	if r.IsError {
		return "", errors.New(out)
	}
	return out, nil
}
