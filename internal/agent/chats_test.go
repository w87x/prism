package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
)

// Web chats are independent: their own history, their own running turn (two can be busy at once), and a
// "do not remember" chat leaves nothing for memory.
func TestWebChatsAreIndependentAndCanRunTogether(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, err := h.e.CreateChat(ctx, "Trip planning", 0)
	if err != nil || a.Topic == "" || a.Title != "Trip planning" {
		t.Fatalf("create: %+v %v", a, err)
	}
	b, _ := h.e.CreateChat(ctx, "", 0)
	if a.Topic == b.Topic {
		t.Fatal("chats need distinct topics")
	}
	no := false
	if _, err := h.e.UpdateChat(ctx, b.ID, ChatPatch{Remember: &no}); err != nil {
		t.Fatal(err)
	}
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "ok", DelayMS: 1500} // slow enough for the two turns to overlap
	}
	if err := h.e.UserMessage(ctx, UserMsg{Text: "book the hotel", Topic: a.Topic}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.UserMessage(ctx, UserMsg{Text: "private thoughts about my salary", Topic: b.Topic}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return h.e.ChatBusy(ChatKey("web", a.Topic)) && h.e.ChatBusy(ChatKey("web", b.Topic)) })
	if h.e.ChatBusy(ChatKey("web", "")) {
		t.Fatal("the main chat is idle")
	}
	waitFor(t, 8*time.Second, func() bool { return !h.e.ChatBusy(ChatKey("web", a.Topic)) && !h.e.ChatBusy(ChatKey("web", b.Topic)) })

	ha, _ := h.e.ChatHistory(ctx, "web", a.Topic, 20)
	hb, _ := h.e.ChatHistory(ctx, "web", b.Topic, 20)
	hm, _ := h.e.ChatHistory(ctx, "web", "", 20)
	join := func(ms []ChatMsg) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.Text)
		}
		return strings.Join(s, "|")
	}
	if !strings.Contains(join(ha), "book the hotel") || strings.Contains(join(ha), "salary") || strings.Contains(join(hm), "hotel") || !strings.Contains(join(hb), "salary") {
		t.Fatalf("histories leaked: a=%q b=%q main=%q", join(ha), join(hb), join(hm))
	}
	var raw int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM memory_raw WHERE text LIKE '%salary%'`).Scan(&raw)
	if raw != 0 {
		t.Fatalf("a chat marked do-not-remember must leave nothing for memory (%d raw rows)", raw)
	}
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM memory_raw WHERE text LIKE '%hotel%' AND topic=$1`, a.Topic).Scan(&raw)
	if raw == 0 {
		t.Fatal("a normal chat is remembered, tagged with its topic")
	}
	list, _ := h.e.Chats(ctx, false)
	if len(list) != 3 || list[0].Topic != "" || list[0].Title != "Main" {
		t.Fatalf("list = %+v", list)
	}
	if err := h.e.DeleteChat(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := h.e.ChatHistory(ctx, "web", a.Topic, 20); len(left) != 0 {
		t.Fatalf("a deleted chat keeps no messages: %+v", left)
	}
	if err := h.e.DeleteChat(ctx, list[0].ID); err == nil {
		t.Fatal("the main chat cannot be deleted")
	}
}

// A chat bound to a project reports it (recall and distillation use it); the model can suggest a binding, which
// the user accepts, creating the project when it is new.
func TestChatProjectBindingAndSuggestion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	bank, err := h.e.Memory.EnsureBank(ctx, "project", "Berlin Trip", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := h.e.CreateChat(ctx, "planning", bank.ID)
	if got := h.e.ChatProject(ctx, "web", c.Topic); got != "project:Berlin Trip" {
		t.Fatalf("ChatProject = %q", got)
	}
	if got := h.e.ChatProject(ctx, "web", ""); got != "" {
		t.Fatalf("the main chat is unbound: %q", got)
	}
	if got := h.e.ChatProject(ctx, "telegram", c.Topic); got != "" {
		t.Fatal("only web chats carry a project")
	}
	zero := int64(0)
	if u, _ := h.e.UpdateChat(ctx, c.ID, ChatPatch{ProjectBankID: &zero}); u.Project != "" {
		t.Fatalf("unbind: %+v", u)
	}

	// naming + suggestion of a NEW project
	n, _ := h.e.CreateChat(ctx, "", 0)
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		if strings.Contains(fmt.Sprint(req["messages"]), "You name a chat") {
			return testutil.Reply{Content: `{"title":"Greenhouse build","project":"","new_project":"Greenhouse"}`}
		}
		return testutil.Reply{Content: "Sure — let's plan the greenhouse."}
	}
	say := func(text string) {
		if err := h.e.UserMessage(ctx, UserMsg{Text: text, Topic: n.Topic}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 8*time.Second, func() bool { return !h.e.ChatBusy(ChatKey("web", n.Topic)) })
	}
	say("I want to build a greenhouse behind the house")
	if x, _ := h.e.GetChat(ctx, n.Topic); !strings.HasPrefix(x.Title, "I want to build a greenhouse") || x.SuggestedProject != "" {
		t.Fatalf("after one message: a provisional title from the message, no model, no suggestion yet: %+v", x)
	}
	say("it should be about 3 by 4 meters")
	say("and it needs a small heater and a watering timer")
	waitFor(t, 8*time.Second, func() bool {
		x, _ := h.e.GetChat(ctx, n.Topic)
		return x.Title == "Greenhouse build" && x.SuggestedProject == "Greenhouse" && x.SuggestedNew
	})
	acc, err := h.e.UpdateChat(ctx, n.ID, ChatPatch{AcceptSuggest: true})
	if err != nil || acc.Project != "project:Greenhouse" || acc.SuggestedProject != "" {
		t.Fatalf("accepting the suggestion: %+v %v", acc, err)
	}
}

func TestMergingChatsMovesMessagesKeepsProjectAndCanBeUndone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(map[string]any, int) testutil.Reply {
		return testutil.Reply{Content: "Wants a 3x4 m greenhouse with a heater."}
	}
	proj1, _ := h.e.Memory.EnsureBank(ctx, "project", "Greenhouse", "", "")
	proj2, _ := h.e.Memory.EnsureBank(ctx, "project", "Trip", "", "")
	a, _ := h.e.CreateChat(ctx, "Greenhouse basics", proj1.ID)
	b, _ := h.e.CreateChat(ctx, "Greenhouse heater", proj1.ID)
	c, _ := h.e.CreateChat(ctx, "Trip", proj2.ID)
	target, _ := h.e.CreateChat(ctx, "Home projects", 0)
	post := func(topic, role, text string) {
		if _, err := h.e.DB.Exec(ctx, `INSERT INTO chat_messages(role,agent,text,channel,topic) VALUES($1,'',$2,'web',$3)`, role, text, topic); err != nil {
			t.Fatal(err)
		}
	}
	post(a.Topic, "user", "size of the greenhouse?")
	post(b.Topic, "user", "which heater?")
	post(a.Topic, "agent", "about 3 by 4 metres")
	post(target.Topic, "user", "what else is on the list?")

	// sources with different projects and an unbound target: the user must choose
	if _, err := h.e.MergeChats(ctx, []int64{a.ID, c.ID}, target.ID, nil); err == nil || !strings.Contains(err.Error(), "different projects") {
		t.Fatalf("conflicting projects: %v", err)
	}
	// a busy chat is never merged under a running turn
	h.e.mu.Lock()
	h.e.chatRun[ChatKey("web", b.Topic)] = &activeRun{}
	h.e.mu.Unlock()
	if _, err := h.e.MergeChats(ctx, []int64{b.ID}, target.ID, nil); err == nil || !strings.Contains(err.Error(), "working") {
		t.Fatalf("busy chat: %v", err)
	}
	h.e.mu.Lock()
	delete(h.e.chatRun, ChatKey("web", b.Topic))
	h.e.mu.Unlock()
	// the main chat cannot be a source
	list0, _ := h.e.Chats(ctx, false)
	if _, err := h.e.MergeChats(ctx, []int64{list0[0].ID}, target.ID, nil); err == nil {
		t.Fatal("the main chat is not mergeable as a source")
	}

	res, err := h.e.MergeChats(ctx, []int64{a.ID, b.ID}, target.ID, nil)
	if err != nil || res.Moved != 3 || res.Merged != 2 {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if res.Target.Project != "project:Greenhouse" {
		t.Fatalf("the sources' shared project is adopted by an unbound target: %+v", res.Target)
	}
	hist, _ := h.e.ChatHistory(ctx, "web", target.Topic, 20)
	if len(hist) != 4 || hist[0].Text != "size of the greenhouse?" && hist[0].Text != "what else is on the list?" {
		t.Fatalf("history = %+v", hist)
	}
	fromA := 0
	for _, m := range hist {
		if m.MovedFrom == a.Topic && m.MovedTitle == "Greenhouse basics" {
			fromA++
		}
	}
	if fromA != 2 {
		t.Fatalf("moved messages carry their origin: %+v", hist)
	}
	if left, _ := h.e.ChatHistory(ctx, "web", a.Topic, 20); len(left) != 0 {
		t.Fatalf("the source keeps no messages: %+v", left)
	}
	visible, _ := h.e.Chats(ctx, false)
	for _, x := range visible {
		if x.ID == a.ID || x.ID == b.ID {
			t.Fatal("merged chats are archived")
		}
	}
	sess, _ := h.e.Sessions.Chat(ctx, "Atlas", ChatKey("web", target.Topic))
	ms, _ := h.e.Sessions.Messages(ctx, sess.ID)
	notes := 0
	for _, m := range ms {
		if strings.Contains(m.Content, "[merged chat") && strings.Contains(m.Content, "greenhouse") {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("the target's agent context must receive a summary of each merged chat, got %d", notes)
	}

	// undo one of them
	arch, _ := h.e.Chats(ctx, true)
	var back Chat
	for _, x := range arch {
		if x.ID == a.ID {
			back = x
		}
	}
	if back.MergedInto != target.Topic || back.MergedTitle != "Home projects" {
		t.Fatalf("archived entry = %+v", back)
	}
	if err := h.e.UnmergeChat(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if restored, _ := h.e.ChatHistory(ctx, "web", a.Topic, 20); len(restored) != 2 {
		t.Fatalf("undo returns its messages: %+v", restored)
	}
	if t2, _ := h.e.ChatHistory(ctx, "web", target.Topic, 20); len(t2) != 2 {
		t.Fatalf("target after undo: %+v", t2)
	}

	// old merged-away chats are purged; restored ones are not
	_, _ = h.e.DB.Exec(ctx, `UPDATE chats SET merged_at=now()-interval '40 days' WHERE id=$1`, b.ID)
	if n := h.e.PurgeMergedChats(ctx, 30*24*time.Hour); n != 1 {
		t.Fatalf("purged %d", n)
	}
	// choosing the project explicitly resolves a conflict
	pid := proj2.ID
	c2, _ := h.e.CreateChat(ctx, "Another", 0)
	if r2, err := h.e.MergeChats(ctx, []int64{a.ID, c.ID}, c2.ID, &pid); err != nil || r2.Target.Project != "project:Trip" {
		t.Fatalf("explicit project: %+v %v", r2, err)
	}
}

func TestNewProjectCanBeCreatedFromAChatPatch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, _ := h.e.CreateChat(ctx, "Solar", 0)
	name := "Solar Panels"
	u, err := h.e.UpdateChat(ctx, c.ID, ChatPatch{NewProject: &name})
	if err != nil || u.Project != "project:Solar Panels" {
		t.Fatalf("new project: %+v %v", u, err)
	}
}

// A database that applied an early version of migration 035 (without the later columns) is repaired by 036, and
// chats work on it.
func TestChatsWorkOnADatabaseThatMissedLaterColumns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, q := range []string{
		`ALTER TABLE chats DROP COLUMN named_at`, `ALTER TABLE chats DROP COLUMN merged_into`, `ALTER TABLE chats DROP COLUMN merged_at`,
		`ALTER TABLE chat_messages DROP COLUMN moved_from`,
	} {
		if _, err := h.e.DB.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sql, err := os.ReadFile("../db/migrations/036_chats_columns.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.DB.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("036 must repair the schema: %v", err)
	}
	if _, err := h.e.DB.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("036 must be idempotent: %v", err)
	}
	c, err := h.e.CreateChat(ctx, "After repair", 0)
	if err != nil || c.Topic == "" {
		t.Fatalf("create: %+v %v", c, err)
	}
	if list, err := h.e.Chats(ctx, false); err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := h.e.ChatHistory(ctx, "web", c.Topic, 10); err != nil {
		t.Fatal(err)
	}
}
