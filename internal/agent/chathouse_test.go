package agent

import (
	"context"
	"strings"
	"testing"

	"prism/internal/testutil"
)

func TestChatHousekeepingSuggestsAndDoes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		switch {
		case strings.Contains(sys, "SAME subject"):
			return testutil.Reply{Content: "SAME"}
		case strings.Contains(sys, "DIFFERENT subject"):
			return testutil.Reply{Content: "DIFFERENT"}
		case strings.Contains(sys, "Name the subject"):
			return testutil.Reply{Content: `{"title":"Film evenings"}`}
		}
		return testutil.Reply{Content: "ok"}
	}
	say := func(topic string, msgs ...string) {
		for _, m := range msgs {
			h.e.logChat(ctx, "user", "", m, "web", topic, 0)
			h.e.logChat(ctx, "agent", "Atlas", "answer to: "+m, "web", topic, 0)
		}
	}
	// an empty chat made long ago is removed, a young one stays
	oldEmpty, _ := h.e.CreateChat(ctx, "Oops", 0)
	youngEmpty, _ := h.e.CreateChat(ctx, "Just now", 0)
	_, _ = h.e.DB.Exec(ctx, `UPDATE chats SET created_at = now() - interval '3 days' WHERE id=$1`, oldEmpty.ID)
	h.e.removeEmptyChats(ctx)
	if _, err := h.e.GetChat(ctx, oldEmpty.Topic); err == nil {
		t.Fatal("the old empty chat must be removed")
	}
	if _, err := h.e.GetChat(ctx, youngEmpty.Topic); err != nil {
		t.Fatal("a young empty chat stays")
	}

	// two chats about one subject → a merge suggestion; a third about something else is left alone
	a, _ := h.e.CreateChat(ctx, "FEVM price tracker", 0)
	b, _ := h.e.CreateChat(ctx, "FEVM FAEX2 tracker prices", 0)
	c, _ := h.e.CreateChat(ctx, "Georgia trip", 0)
	say(a.Topic, "track avito prices for FEVM FAEX2 mini pc", "refresh the avito listings for FEVM FAEX2 prices")
	say(b.Topic, "FEVM FAEX2 avito prices tracker listings", "any cheaper FEVM FAEX2 listings on avito")
	say(c.Topic, "flights to tbilisi in november", "hotel in batumi near the sea")
	if n := h.e.suggestMerges(ctx, h.e.chatNotes(ctx)); n != 1 {
		t.Fatalf("one merge suggestion expected, got %d", n)
	}
	if n := h.e.suggestMerges(ctx, h.e.chatNotes(ctx)); n != 0 {
		t.Fatalf("the same pair is not suggested twice, got %d", n)
	}

	// a stale chat → archive; accepting archives it
	old, _ := h.e.CreateChat(ctx, "Old idea", 0)
	say(old.Topic, "think about the garden plan for spring")
	_, _ = h.e.DB.Exec(ctx, `UPDATE chats SET last_at = now() - interval '45 days' WHERE id=$1`, old.ID)
	if n := h.e.suggestStale(ctx, h.e.chatNotes(ctx)); n != 1 {
		t.Fatalf("stale: %d", n)
	}

	// a chat with a second subject → split
	mix, _ := h.e.CreateChat(ctx, "Avito price watch", 0)
	say(mix.Topic, "avito price watch FEVM listings today", "avito price watch cheaper FEVM listings", "avito listings FEVM price drop", "avito FEVM price history listings",
		"avito watch FEVM listings new", "movies tonight watch movies thriller films", "movies tonight watch movies comedy films", "movies tonight watch movies drama films")
	if n := h.e.suggestSplits(ctx, h.e.chatNotes(ctx)); n != 1 {
		t.Fatalf("split: %d", n)
	}

	sugs, _ := h.e.ChatSuggestions(ctx)
	kinds := map[string]int64{}
	for _, s := range sugs {
		kinds[s.Kind] = s.ID
	}
	if len(kinds) != 3 || kinds["merge"] == 0 || kinds["archive"] == 0 || kinds["split"] == 0 {
		t.Fatalf("suggestions: %+v", sugs)
	}
	if err := h.e.AcceptChatSuggestion(ctx, kinds["archive"]); err != nil {
		t.Fatal(err)
	}
	if ch, _ := h.e.GetChat(ctx, old.Topic); !ch.Archived {
		t.Fatal("the stale chat must be archived")
	}
	if err := h.e.AcceptChatSuggestion(ctx, kinds["split"]); err != nil {
		t.Fatal(err)
	}
	left, _ := h.e.ChatHistory(ctx, "web", mix.Topic, 60)
	for _, m := range left {
		if strings.Contains(m.Text, "movies") {
			t.Fatalf("a film message stayed behind: %q", m.Text)
		}
	}
	cs, _ := h.e.Chats(ctx, false)
	moved := 0
	for _, ch := range cs {
		if ch.Title == "Film evenings" {
			ms, _ := h.e.ChatHistory(ctx, "web", ch.Topic, 60)
			for _, m := range ms {
				if m.MovedFrom == mix.Topic {
					moved++
				}
			}
		}
	}
	if moved != 6 { // three messages and their three answers
		t.Fatalf("moved rows: %d", moved)
	}
	if err := h.e.AcceptChatSuggestion(ctx, kinds["split"]); err == nil {
		t.Fatal("a suggestion can be accepted once")
	}
	if err := h.e.DismissChatSuggestion(ctx, kinds["merge"]); err != nil {
		t.Fatal(err)
	}
	if n := h.e.suggestMerges(ctx, h.e.chatNotes(ctx)); n != 0 {
		t.Fatal("a dismissed suggestion does not come back")
	}
}
