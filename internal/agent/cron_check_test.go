package agent

import (
	"context"
	"fmt"
	"prism/internal/llm"
	"strings"
	"testing"
)

func TestMissingCreatedCronOnlyChecksClaimedSuccessfulCreation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sess, err := h.e.Sessions.Create(ctx, "Hearth", "task", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = h.e.DB.QueryRow(ctx, `INSERT INTO crons(name,agent,expr,prompt) VALUES('test','Atlas','0 4 * * *','check') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Msg{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "create", Name: "cron_create", Arguments: `{}`}}}}, {Message: llm.Message{Role: "tool", ToolCallID: "create", Content: fmt.Sprintf("Cron #%d created; runs as Atlas.", id)}}}
	for _, m := range msgs {
		if _, err = h.e.Sessions.Append(ctx, sess.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	result := fmt.Sprintf("Created cron #%d", id)
	if got, err := h.e.missingCreatedCron(ctx, sess.ID, result); err != nil || got != "" {
		t.Fatalf("existing: %q %v", got, err)
	}
	if _, err = h.e.DB.Exec(ctx, `DELETE FROM crons WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := h.e.missingCreatedCron(ctx, sess.ID, result); err != nil || !strings.Contains(got, "no longer exists") {
		t.Fatalf("missing: %q %v", got, err)
	}
	if got, err := h.e.missingCreatedCron(ctx, sess.ID, "No cron was requested"); err != nil || got != "" {
		t.Fatalf("unclaimed: %q %v", got, err)
	}
}
