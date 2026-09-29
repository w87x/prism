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

// OnDenied is the one hook that sees a stranger probing the server: it must fire with the real remote
// address and a reason, for both ways a connection can be turned away before a Client ever exists.
func TestOnDeniedFiresForBadHostAndBadToken(t *testing.T) {
	h := New()
	h.Token = "s3cret"
	type denial struct{ addr, reason string }
	var denials []denial
	h.OnDenied = func(addr, reason string) { denials = append(denials, denial{addr, reason}) }

	req := httptest.NewRequest("GET", "http://evil.example/ws", nil)
	req.RemoteAddr = "203.0.113.5:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("expected 403 for a foreign host, got %d", rec.Code)
	}

	req2 := httptest.NewRequest("GET", "http://127.0.0.1:7777/ws", nil)
	req2.RemoteAddr = "198.51.100.9:1234"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != 401 {
		t.Fatalf("expected 401 for a missing token, got %d", rec2.Code)
	}

	if len(denials) != 2 {
		t.Fatalf("expected 2 denials, got %+v", denials)
	}
	if denials[0].addr != "203.0.113.5:54321" || denials[0].reason != "forbidden host" {
		t.Fatalf("host denial: %+v", denials[0])
	}
	if denials[1].addr != "198.51.100.9:1234" || denials[1].reason != "bad or missing token" {
		t.Fatalf("token denial: %+v", denials[1])
	}
}

// OnConnect is where a legitimate connection becomes visible; Remote/RemoteAddr are what the caller (the
// app layer) uses to decide whether to tell the user about it — this machine talking to itself must not
// look remote.
func TestOnConnectReportsRemoteAddrAndLoopback(t *testing.T) {
	h := New()
	connected := make(chan *Client, 1)
	h.OnConnect = func(c *Client) { connected <- c }
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	select {
	case cl := <-connected:
		if cl.RemoteAddr == "" {
			t.Fatal("RemoteAddr should be populated")
		}
		if cl.Remote {
			t.Fatalf("a loopback test connection must not be reported as Remote: %+v", cl)
		}
	case <-ctx.Done():
		t.Fatal("OnConnect never fired")
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
