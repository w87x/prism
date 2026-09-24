package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRPCAndLoadShedding(t *testing.T) {
	h := New()
	release := make(chan struct{})
	var running atomic.Int32
	h.Handle("echo", func(ctx context.Context, p json.RawMessage) (any, error) { return json.RawMessage(p), nil })
	h.Handle("slow", func(ctx context.Context, p json.RawMessage) (any, error) {
		running.Add(1)
		<-release
		return true, nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	send := func(id int, method, params string) {
		b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(params)})
		_ = c.Write(ctx, websocket.MessageText, b)
	}
	read := func() map[string]any {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if m["event"] == nil { // skip pushed events (editor state); we want RPC responses
				return m
			}
		}
	}
	send(1, "echo", `{"a":1}`)
	if m := read(); m["id"].(float64) != 1 || m["result"].(map[string]any)["a"].(float64) != 1 {
		t.Fatalf("echo: %v", m)
	}
	send(2, "nope", `{}`)
	if m := read(); !strings.Contains(m["error"].(string), "unknown method") {
		t.Fatalf("unknown: %v", m)
	}
	// flood with blocking calls: beyond the cap they are shed immediately
	for i := 0; i < maxInflight+10; i++ {
		send(100+i, "slow", `{}`)
	}
	shed := 0
	for shed < 10 {
		m := read()
		if e, _ := m["error"].(string); strings.Contains(e, "too many concurrent") {
			shed++
		}
	}
	close(release)
}

func TestHostGuard(t *testing.T) {
	h := New()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req := httptest.NewRequest("GET", "http://evil.example/ws", nil)
	if h.HostAllowed(req) {
		t.Fatal("foreign Host must be rejected (DNS rebinding)")
	}
	for _, ok := range []string{"http://127.0.0.1:7777/ws", "http://localhost:5173/ws", "http://[::1]:7777/ws"} {
		if !h.HostAllowed(httptest.NewRequest("GET", ok, nil)) {
			t.Fatalf("%s should be allowed", ok)
		}
	}
}

func TestLANHostsAndToken(t *testing.T) {
	h := New()
	h.AllowedHosts = []string{"0.0.0.0:7777", "mac.local:7777"}
	h.Token = "s3cret"
	if h.HostAllowed(httptest.NewRequest("GET", "http://192.168.1.20:7777/", nil)) {
		t.Fatal("LAN IP must be rejected unless IP hosts are allowed")
	}
	h.AllowIPHosts = true
	for _, u := range []string{"http://192.168.1.20:7777/", "http://[fd00::5]:7777/", "http://mac.local:7777/"} {
		if !h.HostAllowed(httptest.NewRequest("GET", u, nil)) {
			t.Fatalf("%s should be allowed", u)
		}
	}
	if h.HostAllowed(httptest.NewRequest("GET", "http://evil.example:7777/", nil)) {
		t.Fatal("foreign names stay rejected even with IP hosts allowed (DNS rebinding)")
	}
	if h.Authorized(httptest.NewRequest("GET", "/artifacts/1", nil)) || h.Authorized(httptest.NewRequest("GET", "/artifacts/1?token=nope", nil)) {
		t.Fatal("missing/wrong token must be refused")
	}
	if !h.Authorized(httptest.NewRequest("GET", "/artifacts/1?token=s3cret", nil)) {
		t.Fatal("right token must pass")
	}
	r := httptest.NewRequest("GET", "/artifacts/1", nil)
	r.Header.Set("X-Prism-Token", "s3cret")
	if !h.Authorized(r) {
		t.Fatal("header token must pass")
	}
}

func TestSingleEditorTab(t *testing.T) {
	h := New()
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// next waits for an "editor" event and reports whether this tab may edit
	next := func(c *websocket.Conn) (editor, taken bool) {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Event string
				Data  struct{ Editor, Taken bool }
			}
			_ = json.Unmarshal(b, &m)
			if m.Event == "editor" {
				return m.Data.Editor, m.Data.Taken
			}
		}
	}
	a := dial()
	defer a.CloseNow()
	if ed, _ := next(a); !ed {
		t.Fatal("the first tab must be the editor")
	}
	b := dial()
	defer b.CloseNow()
	if ed, taken := next(b); ed || !taken {
		t.Fatalf("a second tab must start read-only: editor=%v taken=%v", ed, taken)
	}
	// B takes over: A is told it is read-only
	req, _ := json.Marshal(map[string]any{"id": 1, "method": "editor.claim", "params": nil})
	_ = b.Write(ctx, websocket.MessageText, req)
	if ed, _ := next(a); ed {
		t.Fatal("A must lose editing after B claims")
	}
	// the editor leaves: the remaining tab is told nobody holds the pen
	_ = b.CloseNow()
	if ed, taken := next(a); ed || taken {
		t.Fatalf("after the editor left: editor=%v taken=%v", ed, taken)
	}
}
