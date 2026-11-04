package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
)

func TestBackgroundResultReturnsToItsOwnChatOnceAsUntrustedData(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.e.Sessions.Chat(ctx, "Atlas", ChatKey("web", "c1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.e.Sessions.Chat(ctx, "Atlas", ChatKey("web", "c2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.DB.Exec(ctx, `INSERT INTO chat_background_results(channel,topic,result) VALUES('web','c1','worker found the answer')`); err != nil {
		t.Fatal(err)
	}
	h.e.appendBackgroundResults(ctx, second.ID, "web", "c2")
	h.e.appendBackgroundResults(ctx, first.ID, "web", "c1")
	h.e.appendBackgroundResults(ctx, first.ID, "web", "c1")
	msgs, err := h.e.Sessions.Messages(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !msgs[0].Tainted || !strings.Contains(msgs[0].Content, "worker found the answer") {
		t.Fatalf("messages: %+v", msgs)
	}
	other, err := h.e.Sessions.Messages(ctx, second.ID)
	if err != nil || len(other) != 0 {
		t.Fatalf("other chat: %+v err=%v", other, err)
	}
}

func TestBackgroundSummaryIsSkippedOnceConsumed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "The first item is ready; the second is still missing."}
	}
	var id int64
	if err := h.e.DB.QueryRow(ctx, `INSERT INTO chat_background_results(channel,topic,result) VALUES('web','c1','First ready; second missing') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h.e.summarizeBackground(ctx, id, "web", "c1", 0, "First ready; second missing")
	hist, err := h.e.ChatHistory(ctx, "web", "c1", 10)
	if err != nil || len(hist) != 1 || !strings.Contains(hist[0].Text, "second is still missing") {
		t.Fatalf("history=%+v err=%v", hist, err)
	}
	if _, err := h.e.DB.Exec(ctx, `UPDATE chat_background_results SET consumed_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	h.e.summarizeBackground(ctx, id, "web", "c1", 0, "First ready; second missing")
	hist, _ = h.e.ChatHistory(ctx, "web", "c1", 10)
	if len(hist) != 1 {
		t.Fatalf("consumed result was reported again: %+v", hist)
	}
}

func TestBackgroundSummarySuppressesResultConsumedWhileModelRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		close(started)
		<-release
		return testutil.Reply{Content: "Result ready."}
	}
	var id int64
	if err := h.e.DB.QueryRow(ctx, `INSERT INTO chat_background_results(channel,topic,result) VALUES('web','c1','result') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); h.e.summarizeBackground(ctx, id, "web", "c1", 0, "result") }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("summary model did not start")
	}
	if _, err := h.e.DB.Exec(ctx, `UPDATE chat_background_results SET consumed_at=now() WHERE id=$1`, id); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("summary did not finish")
	}
	hist, err := h.e.ChatHistory(ctx, "web", "c1", 10)
	if err != nil || len(hist) != 0 {
		t.Fatalf("stale summary delivered: %+v err=%v", hist, err)
	}
}

func TestDeletingChatRemovesPendingBackgroundResults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chat, err := h.e.CreateChat(ctx, "temporary", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.DB.Exec(ctx, `INSERT INTO chat_background_results(channel,topic,result) VALUES('web',$1,'pending')`, chat.Topic); err != nil {
		t.Fatal(err)
	}
	if err := h.e.DeleteChat(ctx, chat.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.e.DB.QueryRow(ctx, `SELECT count(*) FROM chat_background_results WHERE topic=$1`, chat.Topic).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pending=%d err=%v", count, err)
	}
}
