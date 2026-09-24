package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
	"prism/internal/tools"
)

// TestMain doubles as a tiny stdio MCP server when PRISM_FAKE_MCP is set.
func TestMain(m *testing.M) {
	if os.Getenv("PRISM_FAKE_MCP") == "1" {
		fakeServer()
		return
	}
	os.Exit(m.Run())
}

func fakeServer() {
	sc := bufio.NewScanner(os.Stdin)
	out := func(id json.RawMessage, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
		fmt.Println(string(b))
	}
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			out(req.ID, map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake"}})
		case "tools/list":
			out(req.ID, map[string]any{"tools": []any{
				map[string]any{"name": "echo", "description": "Echo text back", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
					"annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "delete-all", "description": "dangerous", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			out(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + fmt.Sprint(req.Params.Arguments["text"])}}})
		}
	}
}

func TestStdioServerRegistersDeferredTools(t *testing.T) {
	d := testutil.DB(t)
	reg := tools.NewRegistry(d.Pool)
	m := NewManager(d.Pool, reg)
	defer m.Stop()
	exe, _ := os.Executable()
	id, err := m.Save(context.Background(), Server{Name: "fake srv", Transport: "stdio", Command: exe, Env: map[string]string{"PRISM_FAKE_MCP": "1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	st := m.Statuses()[id]
	if st.State != "connected" || len(st.Tools) != 2 {
		t.Fatalf("status: %+v", st)
	}
	echo, ok := reg.Get("mcp__fake_srv__echo")
	if !ok || !echo.Deferred || !echo.Untrusted || echo.Risk != tools.RiskRead || echo.Source != fmt.Sprintf("mcp:%d", id) {
		t.Fatalf("echo tool: %+v", echo)
	}
	if del, _ := reg.Get("mcp__fake_srv__delete_all"); del == nil || del.Risk != tools.RiskExec {
		t.Fatal("unannotated MCP tools must default to exec risk")
	}
	out, err := echo.Run(ctx, &tools.Env{}, json.RawMessage(`{"text":"hi"}`))
	if err != nil || !strings.Contains(out, "echo: hi") {
		t.Fatalf("call: %q %v", out, err)
	}
	// disabling removes the tools
	_, _ = m.Save(ctx, Server{ID: id, Name: "fake srv", Transport: "stdio", Command: exe, Enabled: false})
	_ = m.Reload(ctx, id)
	if _, ok := reg.Get("mcp__fake_srv__echo"); ok {
		t.Fatal("tools should be unregistered when the server is disabled")
	}
}
