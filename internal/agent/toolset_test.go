package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/tasks"
	"prism/internal/testutil"
)

// Agents have fixed toolsets. A registered tool outside the agent's list must be refused even when the model asks
// for it (it used to be callable by anything the registry knew), tool_search only looks things up and says who
// holds the tool, and an agent that cannot finish ends with report_blocked — handing material over by reference.
func TestAgentsCannotUseToolsOutsideTheirToolset(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Scout", Group: "Web", Description: "web research", Soul: "You are Scout, a web researcher.",
		Traits: []string{"web"}, Tools: []string{"clock"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Cipher", Group: "Coding", Description: "runs shell commands and scripts", Soul: "You are Cipher, an engineer.",
		Traits: []string{"shell"}, Tools: []string{"shell"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	var seen []string
	artRe := regexp.MustCompile(`#(\d+)`)
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		role, content, _ := msgAt(req, -1)
		switch {
		case strings.Contains(sys, "You are Atlas") && role == "user":
			return testutil.Reply{Tools: []llm.ToolCall{tc("d1", "delegate", map[string]any{"tasks": []map[string]string{{"agent": "Scout", "instruction": "run uname for me"}}})}}
		case strings.Contains(sys, "You are Atlas") && role == "tool":
			seen = append(seen, "atlas:"+content)
			return testutil.Reply{Content: "routed"}
		case strings.Contains(sys, "You are Scout") && role == "user":
			return testutil.Reply{Tools: []llm.ToolCall{tc("s1", "shell", map[string]any{"command": "uname"})}} // not in her toolset
		case strings.Contains(sys, "You are Scout") && role == "tool" && strings.Contains(content, "not in your toolset"):
			seen = append(seen, "denied:"+content)
			return testutil.Reply{Tools: []llm.ToolCall{tc("s2", "tool_search", map[string]any{"query": "shell command"})}}
		case strings.Contains(sys, "You are Scout") && role == "tool" && strings.Contains(content, "held by"):
			seen = append(seen, "search:"+content)
			return testutil.Reply{Tools: []llm.ToolCall{tc("s3", "artifact_save", map[string]any{"name": "notes.md", "content": "partial results", "ttl_minutes": 30})}}
		case strings.Contains(sys, "You are Scout") && role == "tool" && strings.Contains(content, "Temporary artifact"):
			id := artRe.FindStringSubmatch(content)[1]
			var n int64
			for _, c := range id {
				n = n*10 + int64(c-'0')
			}
			return testutil.Reply{Tools: []llm.ToolCall{tc("s4", "report_blocked", map[string]any{"missing": "shell", "why": "run uname", "done": "looked around", "refs": []int64{n}})}}
		}
		return testutil.Reply{Content: "unexpected: " + content}
	}
	if err := h.e.UserMessage(ctx, UserMsg{Text: "what kernel is this?"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, func() bool { return h.events.count("chat.message") >= 2 && !h.e.ChatBusy("web") })

	joined := strings.Join(seen, "\n---\n")
	if !strings.Contains(joined, "denied:Error: shell is not in your toolset") || !strings.Contains(joined, "ask_colleague Cipher") {
		t.Fatalf("a tool outside the toolset must be refused, pointing at the agent that holds it:\n%s", joined)
	}
	if !strings.Contains(joined, "search:") || strings.Contains(joined, "Loaded:") || !strings.Contains(joined, "held by: Cipher") {
		t.Fatalf("tool_search must look up and name holders, never load:\n%s", joined)
	}
	if !strings.Contains(joined, "MISSING CAPABILITY: shell") || !strings.Contains(joined, "Agents that hold it: Cipher") || !strings.Contains(joined, "artifact #") || !strings.Contains(joined, "notes.md") {
		t.Fatalf("report_blocked must reach the requester with what is missing, who holds it and the reference:\n%s", joined)
	}
	all, _ := h.e.Tasks.List(ctx, tasks.Filter{})
	var child *tasks.Task
	for i := range all {
		if all[i].ToAgent == "Scout" {
			child = &all[i]
		}
	}
	if child == nil || child.Status != tasks.WaitingInput {
		t.Fatalf("the blocked task must wait for its requester, not finish or fail: %+v", child)
	}
}

func TestArtifactRefsMustExist(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var id int64
	if err := h.e.DB.QueryRow(ctx, `INSERT INTO artifacts(name,path,size,created_by) VALUES('plan.md','/tmp/x',12,'Scout') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got, err := h.e.artifactRefs(ctx, []int64{id})
	if err != nil || !strings.Contains(got, "plan.md") || !strings.Contains(got, "artifact_read") {
		t.Fatalf("refs: %q %v", got, err)
	}
	if _, err := h.e.artifactRefs(ctx, []int64{id, 999999}); err == nil || !strings.Contains(err.Error(), "999999") {
		t.Fatalf("an invented reference must be refused: %v", err)
	}
	if s, err := h.e.artifactRefs(ctx, nil); s != "" || err != nil {
		t.Fatalf("no refs, no text: %q %v", s, err)
	}
	// expired hand-offs are gone
	if _, err := h.e.DB.Exec(ctx, `UPDATE artifacts SET expires_at=now()-interval '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.artifactRefs(ctx, []int64{id}); err == nil {
		t.Fatalf("an expired artifact must not be referenced")
	}
}

func TestUnheldToolsAreReported(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	un := h.e.UnheldTools(ctx)
	has := func(n string) bool {
		for _, x := range un {
			if x == n {
				return true
			}
		}
		return false
	}
	if !has("python") { // the harness registers it, and no seeded agent holds it
		t.Fatalf("python should be unheld in a bare install: %v", un)
	}
	if has("shell") || has("clock") {
		t.Fatalf("held or base tools must not be reported: %v", un)
	}
}

// An upgraded database already has the built-in agents; a tool the seed gained since must reach them (agents cannot
// load tools), but only once: a tool the user removed afterwards must not come back at the next start.
func TestSeedAddsNewToolsToExistingBuiltInAgentsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m, err := h.e.Profiles.Get(ctx, "Mnemosyne")
	if err != nil {
		t.Fatal(err)
	}
	has := func(p *Profile, tool string) bool {
		for _, x := range p.Tools {
			if x == tool {
				return true
			}
		}
		return false
	}
	if !has(m, "memory_share") {
		t.Fatalf("a fresh seed gives Mnemosyne memory_share: %v", m.Tools)
	}
	// simulate an old database: the profile predates the tool and nothing was ever recorded
	old := *m
	old.Tools = nil
	for _, x := range m.Tools {
		if x != "memory_share" && x != "memory_analyze" {
			old.Tools = append(old.Tools, x)
		}
	}
	if _, err := h.e.Profiles.Save(ctx, old, "test: old profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.DB.Exec(ctx, `DELETE FROM settings WHERE key='seed_tools'`); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Profiles.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ = h.e.Profiles.Get(ctx, "Mnemosyne")
	if !has(m, "memory_share") || !has(m, "memory_analyze") {
		t.Fatalf("the missing seed tools must be added: %v", m.Tools)
	}
	// the user removes one on purpose; the next start leaves it removed
	kept := *m
	kept.Tools = nil
	for _, x := range m.Tools {
		if x != "memory_share" {
			kept.Tools = append(kept.Tools, x)
		}
	}
	if _, err := h.e.Profiles.Save(ctx, kept, "user edit"); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Profiles.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ = h.e.Profiles.Get(ctx, "Mnemosyne"); has(m, "memory_share") {
		t.Fatalf("a tool the user removed must not be re-added: %v", m.Tools)
	}
}

// A new agent used to show the default robot until the (possibly slow, local) model answered. It now gets a
// purpose-based icon immediately, the model's pick replaces it, and an icon the user chose meanwhile is kept.
func TestNewAgentGetsAnIconAtOnceThenTheModelRefinesIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{DelayMS: 500, Content: `{"icon":"pen-nib"}`}
	}
	p, _ := h.e.Profiles.Save(ctx, Profile{Name: "Ledger", Soul: "You keep the books.", Description: "calculator for accounting and math", Enabled: true}, "")
	done := make(chan struct{})
	go func() { h.e.AssignIcon(ctx, p.ID); close(done) }()
	waitFor(t, 3*time.Second, func() bool { q, _ := h.e.Profiles.GetID(ctx, p.ID); return q.Icon != "" })
	q, _ := h.e.Profiles.GetID(ctx, p.ID)
	if q.Icon != "calculator" {
		t.Fatalf("the immediate icon must match the purpose, got %q", q.Icon)
	}
	<-done
	if q, _ = h.e.Profiles.GetID(ctx, p.ID); q.Icon != "pen-nib" {
		t.Fatalf("the model's pick replaces the guess, got %q", q.Icon)
	}

	// an icon chosen by the user while the model is still thinking is not overwritten
	r, _ := h.e.Profiles.Save(ctx, Profile{Name: "Scribe2", Soul: "x", Description: "writing and editing documents", Enabled: true}, "")
	done = make(chan struct{})
	go func() { h.e.AssignIcon(ctx, r.ID); close(done) }()
	waitFor(t, 3*time.Second, func() bool { x, _ := h.e.Profiles.GetID(ctx, r.ID); return x.Icon != "" })
	if _, err := h.e.DB.Exec(ctx, `UPDATE agent_profiles SET icon='hammer' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	<-done
	if x, _ := h.e.Profiles.GetID(ctx, r.ID); x.Icon != "hammer" {
		t.Fatalf("a user-chosen icon must survive the model's answer, got %q", x.Icon)
	}
}
