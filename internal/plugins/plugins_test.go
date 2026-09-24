package plugins

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/testutil"
	"prism/internal/tools"
)

func setup(t *testing.T) (*Manager, *tools.Registry, string) {
	t.Helper()
	d := testutil.DB(t)
	reg := tools.NewRegistry(d.Pool)
	home := t.TempDir()
	m := &Manager{DB: d.Pool, Reg: reg, DataDir: t.TempDir(), DenyReads: func(context.Context) []string { return []string{filepath.Join(home, ".ssh")} }}
	RegisterTools(reg, m)
	return m, reg, home
}

const adder = `import json
def run(args):
    return {"sum": sum(args["numbers"]), "note": args.get("note", "")}
`

func mustCreate(t *testing.T, m *Manager, name, code string, network bool) Plugin {
	t.Helper()
	p, err := m.Create(context.Background(), Plugin{Name: name, Description: "test plugin " + name, Code: code, Network: network, TimeoutS: 10, CreatedBy: "Atlas",
		Params: json.RawMessage(`{"type":"object","properties":{"numbers":{"type":"array","items":{"type":"number"}}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func call(t *testing.T, reg *tools.Registry, name string, args string) (string, error) {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tool.Run(context.Background(), &tools.Env{Agent: "Atlas"}, json.RawMessage(args))
}

func TestPluginValidation(t *testing.T) {
	m, _, _ := setup(t)
	ctx := context.Background()
	for name, p := range map[string]Plugin{
		"bad name":        {Name: "Bad Name!", Description: "d", Code: adder},
		"no description":  {Name: "x1", Code: adder},
		"no run function": {Name: "x2", Description: "d", Code: "print('hi')"},
		"empty code":      {Name: "x3", Description: "d"},
		"syntax error":    {Name: "x4", Description: "d", Code: "def run(args):\n    return (\n"},
		"huge":            {Name: "x5", Description: "d", Code: "def run(args):\n    pass\n#" + strings.Repeat("x", 70000)},
		"bad timeout":     {Name: "x6", Description: "d", Code: adder, TimeoutS: 9999},
		"bad params":      {Name: "x7", Description: "d", Code: adder, Params: json.RawMessage(`"not an object"`)},
	} {
		if _, err := m.Create(ctx, p); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	mustCreate(t, m, "adder", adder, false)
	if _, err := m.Create(ctx, Plugin{Name: "adder", Description: "again", Code: adder}); err == nil {
		t.Fatal("duplicate names are refused")
	}
}

// Nothing runs before approval; approval registers a deferred exec tool; a changed row or a switch-off removes it.
func TestApprovalGatesExecutionAndHashPinsTheCode(t *testing.T) {
	if !(&Manager{}).Sandboxed() {
		t.Skip("plugins need the macOS sandbox")
	}
	m, reg, _ := setup(t)
	ctx := context.Background()
	p := mustCreate(t, m, "adder", adder, false)
	if p.Status != StatusPending {
		t.Fatalf("new plugins are pending: %s", p.Status)
	}
	if _, ok := reg.Get("plugin_adder"); ok {
		t.Fatal("a pending plugin must not be a tool")
	}
	if err := m.SetStatus(ctx, p.ID, StatusApproved); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("plugin_adder")
	if !ok || tool.Risk != tools.RiskExec || !tool.Deferred || tool.Source != "plugin:adder" || tool.Untrusted {
		t.Fatalf("approved plugin tool: %+v", tool)
	}
	out, err := call(t, reg, "plugin_adder", `{"numbers":[1,2,3.5],"note":"it's \"quoted\"\nwith a newline"}`)
	if err != nil || !strings.Contains(out, `"sum": 6.5`) && !strings.Contains(out, `"sum":6.5`) || !strings.Contains(out, "quoted") {
		t.Fatalf("run: %q %v", out, err)
	}
	// errors from the plugin come back readable
	if _, err := call(t, reg, "plugin_adder", `{}`); err == nil || !strings.Contains(err.Error(), "KeyError") {
		t.Fatalf("plugin exception: %v", err)
	}

	// someone edits the stored code after approval: it must not run, and Sync drops the tool
	if _, err := m.DB.Exec(ctx, `UPDATE plugins SET code=$2 WHERE id=$1`, p.ID, "def run(args):\n    return 'evil'\n"); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("plugin_adder"); ok {
		t.Fatal("a plugin whose code changed after approval must not be a tool")
	}
	_, _ = m.DB.Exec(ctx, `UPDATE plugins SET code=$2 WHERE id=$1`, p.ID, adder)
	_ = m.Sync(ctx)
	if _, ok := reg.Get("plugin_adder"); !ok {
		t.Fatal("restoring the approved code restores the tool")
	}
	if err := m.SetStatus(ctx, p.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("plugin_adder"); ok {
		t.Fatal("a disabled plugin is not a tool")
	}
	_ = m.SetStatus(ctx, p.ID, StatusApproved)
	if err := m.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("plugin_adder"); ok {
		t.Fatal("a deleted plugin is gone")
	}
}

// The sandbox is what makes approval-by-reading enough: no network by default, no writes outside the plugin's
// own folder, no reading of protected locations, and a time limit.
func TestSandboxConfinesPlugins(t *testing.T) {
	if !(&Manager{}).Sandboxed() {
		t.Skip("plugins need the macOS sandbox")
	}
	m, reg, _ := setup(t)
	ctx := context.Background()
	// "outside" must really be outside the folders the sandbox allows (tmp is allowed), so use the real home
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	home, err := os.MkdirTemp(realHome, ".prism-plugin-test-")
	if err != nil {
		t.Skip("cannot create a directory in the home folder")
	}
	defer os.RemoveAll(home)
	m.DenyReads = func(context.Context) []string { return []string{filepath.Join(home, ".ssh")} }
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id"), []byte("SECRET-KEY"), 0o600)
	victim := filepath.Join(home, "victim.txt")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	code := `import socket, os, json
def run(args):
    r = {}
    try:
        socket.create_connection(("127.0.0.1", args["port"]), timeout=3).close(); r["network"] = "ALLOWED"
    except Exception as e:
        r["network"] = "denied"
    try:
        open(args["victim"], "w").write("pwned"); r["write_outside"] = "ALLOWED"
    except Exception:
        r["write_outside"] = "denied"
    try:
        open("scratch.txt", "w").write("ok"); r["write_inside"] = "allowed"
    except Exception:
        r["write_inside"] = "DENIED"
    try:
        r["read_secret"] = open(args["secret"]).read()
    except Exception:
        r["read_secret"] = "denied"
    return r
`
	offline := mustCreate(t, m, "probe", code, false)
	online := mustCreate(t, m, "probenet", code, true)
	_ = m.SetStatus(ctx, offline.ID, StatusApproved)
	_ = m.SetStatus(ctx, online.ID, StatusApproved)
	args, _ := json.Marshal(map[string]any{"port": ln.Addr().(*net.TCPAddr).Port, "victim": victim, "secret": filepath.Join(home, ".ssh", "id")})

	out, err := call(t, reg, "plugin_probe", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]string
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%q", out)
	}
	if r["network"] != "denied" || r["write_outside"] != "denied" || r["write_inside"] != "allowed" || r["read_secret"] != "denied" {
		t.Fatalf("an offline plugin must be confined: %v", r)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatal("the plugin wrote outside its folder")
	}
	// network=true opens only the network; the rest stays closed and the tool is marked untrusted
	out, err = call(t, reg, "plugin_probenet", string(args))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(out), &r)
	if r["network"] != "ALLOWED" || r["write_outside"] != "denied" || r["read_secret"] != "denied" {
		t.Fatalf("network plugin: %v", r)
	}
	if tool, _ := reg.Get("plugin_probenet"); !tool.Untrusted {
		t.Fatal("a plugin with network access returns untrusted output")
	}

	// time limit and output cap
	slow := mustCreate(t, m, "slow", "import time\ndef run(args):\n    time.sleep(30)\n", false)
	_, _ = m.DB.Exec(ctx, `UPDATE plugins SET timeout_s=1 WHERE id=$1`, slow.ID)
	_ = m.SetStatus(ctx, slow.ID, StatusApproved)
	if _, err := call(t, reg, "plugin_slow", `{}`); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("timeout: %v", err)
	}
	big := mustCreate(t, m, "big", "def run(args):\n    return 'x' * 100000\n", false)
	_ = m.SetStatus(ctx, big.ID, StatusApproved)
	if out, err := call(t, reg, "plugin_big", `{}`); err != nil || len(out) > 20200 || !strings.Contains(out, "truncated") {
		t.Fatalf("output cap: %d %v", len(out), err)
	}
}

// Agents can propose plugins but never approve their own from inside an untrusted conversation.
func TestPluginCreateToolNeedsTheUser(t *testing.T) {
	if !(&Manager{}).Sandboxed() {
		t.Skip("plugins need the macOS sandbox")
	}
	m, reg, _ := setup(t)
	ctx := context.Background()
	args := func(name string) string {
		b, _ := json.Marshal(map[string]any{"name": name, "description": "adds numbers", "code": adder, "params": map[string]any{"type": "object", "properties": map[string]any{"numbers": map[string]any{"type": "array"}}}})
		return string(b)
	}
	run := func(env *tools.Env, name string) (string, error) {
		tool, _ := reg.Get("plugin_create")
		return tool.Run(ctx, env, json.RawMessage(args(name)))
	}
	statusOf := func(name string) string {
		ps, _ := m.List(ctx, false)
		for _, p := range ps {
			if p.Name == name {
				return p.Status
			}
		}
		return "missing"
	}
	var shown string
	answer := "deny"
	env := &tools.Env{Agent: "Atlas", Ask: func(_ context.Context, q tools.Question) (string, error) { shown = q.Text; return answer, nil }}

	// denied: stays pending, never a tool; the user was shown the code
	if out, err := run(env, "adder"); err != nil || !strings.Contains(out, "PENDING") || statusOf("adder") != StatusPending {
		t.Fatalf("denied: %q %v status=%s", out, err, statusOf("adder"))
	}
	if !strings.Contains(shown, "def run(args)") || !strings.Contains(shown, "no network") {
		t.Fatalf("the user must see the code and the permissions: %q", shown)
	}
	if _, ok := reg.Get("plugin_adder"); ok {
		t.Fatal("denied plugins are not tools")
	}
	// approved from a clean conversation → a tool
	answer = "allow"
	if out, err := run(env, "adder2"); err != nil || !strings.Contains(out, "approved") || statusOf("adder2") != StatusApproved {
		t.Fatalf("approved: %q %v", out, err)
	}
	if _, ok := reg.Get("plugin_adder2"); !ok {
		t.Fatal("approved plugin must be a tool")
	}
	// a tainted conversation cannot approve it, not even by clicking allow
	asked := false
	tainted := &tools.Env{Agent: "Atlas", Tainted: true, Ask: func(context.Context, tools.Question) (string, error) { asked = true; return "allow", nil }}
	if out, err := run(tainted, "adder3"); err != nil || !strings.Contains(out, "PENDING") || asked || statusOf("adder3") != StatusPending {
		t.Fatalf("tainted: %q %v asked=%v", out, err, asked)
	}
	// no one to ask (unattended run): pending, not approved
	if out, err := run(&tools.Env{Agent: "Atlas"}, "adder4"); err != nil || !strings.Contains(out, "PENDING") || statusOf("adder4") != StatusPending {
		t.Fatalf("unattended: %q %v", out, err)
	}
	// listing and deleting (with confirmation)
	if out, _ := call(t, reg, "plugin_list", `{}`); !strings.Contains(out, "plugin_adder2 [approved]") || !strings.Contains(out, "plugin_adder3 [pending]") {
		t.Fatalf("list: %s", out)
	}
	answer = "deny"
	del, _ := reg.Get("plugin_delete")
	if _, err := del.Run(ctx, env, json.RawMessage(`{"name":"adder2"}`)); err == nil || statusOf("adder2") != StatusApproved {
		t.Fatalf("a denied delete keeps the plugin: %v", err)
	}
	answer = "allow"
	if _, err := del.Run(ctx, env, json.RawMessage(`{"name":"plugin_adder2"}`)); err != nil || statusOf("adder2") != "missing" {
		t.Fatalf("delete: %v", err)
	}
}
