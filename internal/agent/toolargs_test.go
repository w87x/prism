package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/llm"
	"prism/internal/tools"
)

func TestValidToolArguments(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{`{}`, true}, {`{"path":"/tmp/example","n":2}`, true},
		{`{"path":`, false}, {`[]`, false}, {`null`, false}, {`"text"`, false},
	} {
		if got := validToolArguments(tc.input); got != tc.valid {
			t.Errorf("validToolArguments(%q) = %v", tc.input, got)
		}
	}
}

func TestMalformedToolArgumentsAreRejectedBeforeApprovalOrExecution(t *testing.T) {
	registry := tools.NewRegistry(nil)
	called := false
	registry.Register(&tools.Tool{Name: "write_example", Risk: tools.RiskWrite, Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) {
		called = true
		return "done", nil
	}})
	e := NewEngine(Deps{Tools: registry})
	res := e.execOne(context.Background(), &activeRun{}, &tools.Env{Agent: "Scout"}, llm.ToolCall{Name: "write_example", Arguments: `{"path":`}, false, &loopGuard{}, nil, RunSpec{})
	if called || !strings.Contains(res.text, "valid JSON object") {
		t.Fatalf("malformed call was not rejected clearly: called=%v result=%q", called, res.text)
	}
}
