package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/tools"
)

// Opt-in end-to-end evaluation: a real model controls Atlas and a specialist,
// while the database and all task data are disposable and the catalog is a fixture.
// Run with PRISM_TEST_DSN, PRISM_EVAL_BASE_URL and PRISM_EVAL_MODEL set.
type liveFixture struct {
	agent, group, description, soul, tool, result, request string
	expected                                               []string
}

func TestLiveModelCatalogWorkflow(t *testing.T) {
	runLiveWorkflowFixture(t, liveFixture{agent: "CatalogScout", group: "Shopping", description: "Searches the test marketplace catalog for product listings and prices", soul: "You are CatalogScout. Search the test catalog using fixture_catalog_search and report the listing, price and source. Do not guess results.", tool: "fixture_catalog_search", result: "Test catalog: FEVM FAEX2, price 450000 RUB, source https://example.test/listing/fevm", request: "Search the test marketplace for FEVM FAEX2 and report its price and source.", expected: []string{"450", "example.test"}})
}

func TestLiveModelScheduleWorkflow(t *testing.T) {
	runLiveWorkflowFixture(t, liveFixture{agent: "ScheduleScout", group: "Scheduling", description: "Registers test-only simulated recurring checks", soul: "You are ScheduleScout. Use fixture_schedule_create to register a simulated check and report its confirmation and time. This is a test fixture, not a real scheduler.", tool: "fixture_schedule_create", result: "Simulated schedule TEST-CRON-47 created: daily at 04:00 UTC. No real schedule was created.", request: "Using the test scheduler, register a simulated daily check at 04:00 UTC and report its confirmation. This is only a simulation.", expected: []string{"TEST-CRON-47", "04:00"}})
}

func runLiveWorkflowFixture(t *testing.T, f liveFixture) {
	t.Helper()
	base, model := os.Getenv("PRISM_EVAL_BASE_URL"), os.Getenv("PRISM_EVAL_MODEL")
	if base == "" || model == "" {
		t.Skip("set PRISM_EVAL_BASE_URL and PRISM_EVAL_MODEL to run the local model evaluation")
	}
	if os.Getenv("PRISM_TEST_DSN") == "" {
		t.Fatal("PRISM_TEST_DSN is required for an isolated evaluation database")
	}
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pid, err := h.e.LLM.Store().SaveProvider(ctx, llm.Provider{Name: "eval-local", Kind: "custom", BaseURL: base, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.e.LLM.Store().SaveModel(ctx, llm.Model{Name: "eval-local", ProviderID: pid, ModelID: model, Kind: "chat", ContextWindow: 262144, SupportsTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.e.Settings.Set(ctx, settings.KeyModelRoles, settings.ModelRoles{Chat: "eval-local", Fast: "eval-local", Embedding: "embed"}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Settings.Set(ctx, settings.KeyGeneral, settings.General{RoutingOff: true, ChatHousekeepingOff: true}); err != nil {
		t.Fatal(err)
	}
	// The model may only delegate to the fixture specialist; no real-world
	// integrations or execution-capable agents are available in this sandbox.
	if _, err := h.e.DB.Exec(ctx, `UPDATE agent_profiles SET enabled=false WHERE name<>'Atlas'`); err != nil {
		t.Fatal(err)
	}
	var searches atomic.Int32
	h.e.Tools.Register(&tools.Tool{Name: f.tool, Description: f.description, Params: tools.Obj("query", tools.Str("query", "the requested test operation")), Risk: tools.RiskRead, Run: func(_ context.Context, _ *tools.Env, _ json.RawMessage) (string, error) {
		searches.Add(1)
		return f.result, nil
	}})
	_, err = h.e.Profiles.Save(ctx, Profile{Name: f.agent, Group: f.group, Role: RoleWorker, Enabled: true, Description: f.description, Soul: f.soul, Tools: []string{f.tool}, MaxIterations: 8}, "")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	h.e.LLM.OnCall(func(llm.Call) { calls.Add(1) })
	err = h.e.UserMessage(ctx, UserMsg{Channel: "web", Text: f.request})
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for h.e.ChatBusy("web") {
		if asks := h.e.PendingAsks(); len(asks) > 0 {
			t.Fatalf("unnecessary user question: %+v", asks)
		}
		select {
		case <-ctx.Done():
			auditCtx, auditCancel := context.WithTimeout(context.Background(), 3*time.Second)
			taskList, _ := h.e.Tasks.List(auditCtx, tasks.Filter{})
			auditCancel()
			var states []string
			for _, task := range taskList {
				states = append(states, task.ToAgent+":"+task.Status)
			}
			t.Fatalf("workflow timed out; model calls=%d fixture calls=%d streamed deltas=%d tasks=%s", calls.Load(), searches.Load(), h.events.count("run.delta"), strings.Join(states, ","))
		case <-ticker.C:
		}
	}
	history, err := h.e.ChatHistory(ctx, "web", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	answer := ""
	for _, m := range history {
		if m.Role == "agent" {
			answer = m.Text
		}
	}
	t.Logf("workflow model_calls=%d catalog_calls=%d answer=%q", calls.Load(), searches.Load(), answer)
	if searches.Load() == 0 {
		t.Fatalf("workflow did not call the fixture: %q", answer)
	}
	for _, want := range f.expected {
		if !strings.Contains(answer, want) {
			t.Fatalf("workflow did not report fixture evidence %q: %q", want, answer)
		}
	}
}
