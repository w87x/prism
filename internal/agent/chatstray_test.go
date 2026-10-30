package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
)

// A message about another subject, written into an established chat, is taken to the chat it belongs in — or to a new one — when the
// planner is sure, asked about when it is not, and never touched in a chat that has hardly any history.
func TestAStrayMessageIsTakenToTheChatItBelongsIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ads, err := h.e.CreateChat(ctx, "Ads tracker", 0)
	if err != nil {
		t.Fatal(err)
	}
	films, err := h.e.CreateChat(ctx, "Film downloads", 0)
	if err != nil {
		t.Fatal(err)
	}
	planJSON := ""
	var prompts []string
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		_, content, _ := msgAt(req, -1)
		if strings.Contains(sys, "front door of a personal assistant") {
			prompts = append(prompts, content)
			return testutil.Reply{Content: planJSON}
		}
		return testutil.Reply{Content: "ok"}
	}
	for _, c := range []string{"Track Avito prices for the FEVM mini PC", "Refresh the listings", "Any cheaper ones today?"} {
		h.e.logChat(ctx, "user", "", c, "web", ads.Topic, 0)
	}
	h.e.logChat(ctx, "user", "", "Find S01 of Rick and Morty", "web", films.Topic, 0)

	// 1. sure: it moves to the film chat and the reply lands there
	planJSON = `{"class":"question","project":"","chat":"Film downloads","confidence":"high","tasks":[],"asks":[]}`
	if err := h.e.UserMessage(ctx, UserMsg{Text: "which Guy Ritchie movies are worth watching tonight?", Topic: ads.Topic}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, func() bool { return h.events.count("chat.routed") == 1 && !h.e.ChatBusy(ChatKey("web", films.Topic)) })
	has := func(topic string) bool {
		ms, _ := h.e.ChatHistory(ctx, "web", topic, 30)
		for _, m := range ms {
			if m.Role == "user" && strings.Contains(m.Text, "Guy Ritchie") {
				return true
			}
		}
		return false
	}
	if !has(films.Topic) || has(ads.Topic) {
		t.Fatalf("the message must be in the film chat only: films=%v ads=%v", has(films.Topic), has(ads.Topic))
	}
	if len(prompts) == 0 || !strings.Contains(prompts[0], "THE CHAT: Ads tracker") || !strings.Contains(prompts[0], "Film downloads") {
		t.Fatalf("the planner is shown the chat and the others: %v", prompts)
	}

	// 2. a chat with hardly any history is never judged
	fresh, _ := h.e.CreateChat(ctx, "Brand new", 0)
	planJSON = `{"class":"question","project":"","chat":"Film downloads","confidence":"high","tasks":[],"asks":[]}`
	if err := h.e.UserMessage(ctx, UserMsg{Text: "what is the best thriller of the last decade?", Topic: fresh.Topic}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, func() bool { return !h.e.ChatBusy(ChatKey("web", fresh.Topic)) })
	if h.events.count("chat.routed") != 1 {
		t.Fatal("a young chat must not be split")
	}

	// 3. a different subject that fits no chat, and the planner is unsure: the user is asked; yes makes a new chat
	planJSON = `{"class":"question","project":"","chat":"new: Travel plans","confidence":"low","tasks":[],"asks":[]}`
	if err := h.e.UserMessage(ctx, UserMsg{Text: "how do I get from Tbilisi to Batumi in November?", Topic: ads.Topic}); err != nil {
		t.Fatal(err)
	}
	var ask map[string]any
	waitFor(t, 20*time.Second, func() bool {
		for _, a := range h.e.PendingAsks() {
			ask = a
		}
		return ask != nil
	})
	if !strings.Contains(strings.ToLower(ask["text"].(string)), "travel plans") {
		t.Fatalf("the question names the new chat: %v", ask)
	}
	h.e.AnswerAsk(ask["id"].(int64), "Take it to a new chat “Travel plans”")
	waitFor(t, 20*time.Second, func() bool { return h.events.count("chat.routed") == 2 })
	cs, _ := h.e.Chats(ctx, false)
	found := false
	for _, c := range cs {
		if c.Title == "Travel plans" {
			found = true
		}
	}
	if !found {
		t.Fatal("the new chat must exist")
	}
}
