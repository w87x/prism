package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/memory"
	"prism/internal/testutil"
)

func TestSelectedChatIsNotReroutedByPlanner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.Memory.Store(ctx, memory.StoreReq{Bank: "project:homelab", Text: "The NAS is in the office", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	selected, err := h.e.CreateChat(ctx, "My chat", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"First message", "Second message", "Third message"} {
		h.e.logChat(ctx, "user", "", text, "web", selected.Topic, 0)
	}
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "front door of a personal assistant") {
			return testutil.Reply{Content: `{"class":"question","project":"homelab","chat":"Main","confidence":"high","tasks":[],"asks":[]}`}
		}
		return testutil.Reply{Content: "Here is the answer"}
	}
	if err := h.e.UserMessage(ctx, UserMsg{Text: "Please tell me about my homelab NAS", Topic: selected.Topic}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, func() bool { return !h.e.ChatBusy(ChatKey("web", selected.Topic)) })
	selectedHistory, err := h.e.ChatHistory(ctx, "web", selected.Topic, 30)
	if err != nil {
		t.Fatal(err)
	}
	mainHistory, err := h.e.ChatHistory(ctx, "web", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	var user, answer bool
	for _, m := range selectedHistory {
		user = user || (m.Role == "user" && strings.Contains(m.Text, "homelab NAS"))
		answer = answer || (m.Role == "agent" && strings.Contains(m.Text, "Here is the answer"))
	}
	if !user || !answer {
		t.Fatalf("selected chat lost the message or answer: %+v", selectedHistory)
	}
	for _, m := range mainHistory {
		if strings.Contains(m.Text, "homelab NAS") || strings.Contains(m.Text, "Here is the answer") {
			t.Fatalf("message leaked to Main: %+v", mainHistory)
		}
	}
	if h.events.count("chat.routed") != 0 {
		t.Fatal("planner moved a message from the selected chat")
	}
}
