package agent

import (
	"strings"
	"testing"

	"prism/internal/tasks"
	"prism/internal/tools"
)

// A delegate's answer lands in the PARENT's context and is resent every remaining turn, so it is capped
// there — but task_status must still return the whole thing.
func TestFormatDelegatedCapsButTaskStatusFormatDoesNot(t *testing.T) {
	task := tasks.Task{ID: 7, ToAgent: "Scout", Status: tasks.Done, Result: strings.Repeat("x", 20000)}
	capped := formatDelegated(task)
	if len(capped) > maxDelegatedResult+400 || !strings.Contains(capped, "task_status(7)") {
		t.Fatalf("delegate result not capped with a pointer (len %d)", len(capped))
	}
	if full := formatTaskResult(task); len(full) < 20000 {
		t.Fatalf("task_status format must stay uncapped (len %d)", len(full))
	}
	small := tasks.Task{ID: 8, ToAgent: "Scout", Status: tasks.Done, Result: "short"}
	if strings.Contains(formatDelegated(small), "not shown") {
		t.Fatalf("a short result must pass through")
	}
}

func TestDelegatedBudgetUsesAvailableModelWindow(t *testing.T) {
	ar := &activeRun{}
	ar.window.Store(262144)
	ar.ctxTokens.Store(10000)
	e := &Engine{runs: map[int64]*activeRun{1: ar}}
	env := &tools.Env{RunID: 1}
	single, batch := e.delegatedResultBudget(env, 1), e.delegatedResultBudget(env, 8)
	if single <= maxDelegatedResult || batch <= maxDelegatedResult || batch*8 > single {
		t.Fatalf("budgets single=%d batch=%d", single, batch)
	}
}
