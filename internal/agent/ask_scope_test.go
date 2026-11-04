package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestPendingQuestionCarriesItsChatInEventsAndSnapshots(t *testing.T) {
	events := make(chan map[string]any, 1)
	e := NewEngine(Deps{Emit: func(kind string, data any) {
		if kind == "ask.request" {
			events <- data.(map[string]any)
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.ask(ctx, RunInfo{ID: 42, Agent: "Atlas", IsChat: true, ChatChannel: "web", ChatTopic: "c2"}, tools.Question{Kind: "clarify", Text: "Which one?"})
	}()
	select {
	case event := <-events:
		if event["chat_topic"] != "c2" || event["chat_channel"] != "web" || event["is_chat"] != true {
			t.Fatalf("event scope: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("question not emitted")
	}
	pending := e.PendingAsks()
	if len(pending) != 1 || pending[0]["chat_topic"] != "c2" || pending[0]["chat_channel"] != "web" {
		t.Fatalf("snapshot scope: %+v", pending)
	}
	cancel()
	<-done
}

func TestStopChatCancelsPlanningQuestion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "front door of a personal assistant") {
			return testutil.Reply{Content: `{"class":"task","project":"","chat":"","confidence":"high","tasks":[],"asks":[{"question":"Which item?","options":["one","two"]}]}`}
		}
		return testutil.Reply{Content: "ok"}
	}
	if err := h.e.UserMessage(ctx, UserMsg{Text: "Please process the selected item", Channel: "web"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return len(h.e.PendingAsks()) == 1 })
	if !h.e.StopChat(ChatKey("web", "")) {
		t.Fatal("planning question could not be stopped")
	}
	waitFor(t, 10*time.Second, func() bool {
		history, _ := h.e.ChatHistory(ctx, "web", "", 10)
		return !h.e.ChatBusy(ChatKey("web", "")) && len(history) > 0 && history[len(history)-1].Text == "Stopped."
	})
	history, err := h.e.ChatHistory(ctx, "web", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || history[len(history)-1].Text != "Stopped." {
		t.Fatalf("history: %+v", history)
	}
}
