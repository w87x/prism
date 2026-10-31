package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/llm"
)

func TestCheckPlannerResult(t *testing.T) {
	c := evalCase{name: "work", class: "task", noQuestions: true}
	for _, tt := range []struct {
		name, raw string
		pass      bool
	}{
		{"valid", `{"class":"task","tasks":[{"title":"Inspect the build"}]}`, true},
		{"wrong class", `{"class":"question"}`, false},
		{"missing task", `{"class":"task","tasks":[]}`, false},
		{"unnecessary question", `{"class":"task","tasks":[{"title":"Inspect"}],"asks":[{"question":"Continue?"}]}`, false},
		{"invalid", `{`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := check(c, tt.raw); (got == "") != tt.pass {
				t.Fatalf("check = %q", got)
			}
		})
	}
}

func TestRunUsesModelAndRecordsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"{\"class\":\"task\",\"tasks\":[{\"title\":\"Check build\"}]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	}))
	defer server.Close()
	got := run(context.Background(), llm.Endpoint{BaseURL: server.URL, Model: "test", Kind: "chat"}, llm.NewClient(), []evalCase{{name: "one", input: "Check the build", class: "task"}})
	if len(got) != 1 || !got[0].Pass || got[0].PromptTokens != 100 || got[0].OutputTokens != 20 {
		t.Fatalf("result: %+v", got)
	}
	parallel := runParallel(context.Background(), llm.Endpoint{BaseURL: server.URL, Model: "test", Kind: "chat"}, llm.NewClient(), []evalCase{{name: "first", class: "task"}, {name: "second", class: "task"}}, 2)
	if len(parallel) != 2 || parallel[0].Name != "first" || parallel[1].Name != "second" || !parallel[0].Pass || !parallel[1].Pass {
		t.Fatalf("parallel results: %+v", parallel)
	}
}
