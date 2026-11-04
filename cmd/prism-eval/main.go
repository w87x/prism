// prism-eval probes a model with synthetic, read-only front-door planning cases.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"prism/internal/agent"
	"prism/internal/llm"
)

type evalCase struct {
	name, input, class string
	noQuestions        bool
}

var cases = []evalCase{
	{"greeting", "Thanks, that was helpful!", "chat", true},
	{"simple_question", "What is the capital of France?", "question", true},
	{"research", "Find the current US price of the Apple MacBook Air 13-inch M4 with 16GB RAM and 256GB storage, in USD, and summarize two sources.", "task", true},
	{"dependent_work", "Check the build, then summarize any errors you find.", "task", true},
	{"memory_lookup", "What folder did I tell you I use for movies?", "question", true},
	{"avito_watch", "Search Avito for FEVM FAEX2 PCs under 500000 rubles and check daily at 4 AM in my configured timezone.", "task", true},
	{"pricing_comparison", "Compare current Gemini and OpenAI plans and their token limits using official sources.", "task", true},
	{"model_tracker", "Every Friday check for new base language models that fit on my Mac with 96 GB RAM in Q4; use my existing notification settings.", "task", true},
	{"monitor", "Check the announced release date of Neuromancer season 2 every week and notify me when it changes; use my existing notification settings.", "task", true},
}

type result struct {
	Name         string `json:"case"`
	Pass         bool   `json:"pass"`
	Reason       string `json:"reason,omitempty"`
	LatencyMS    int64  `json:"latency_ms"`
	PromptTokens int    `json:"prompt_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
}

func check(c evalCase, raw string) string {
	var p agent.Plan
	if err := json.Unmarshal([]byte(llm.ExtractJSON(raw)), &p); err != nil {
		return "invalid JSON"
	}
	if strings.ToLower(strings.TrimSpace(p.Class)) != c.class {
		return "incorrect classification"
	}
	if c.class == "task" && (len(p.Tasks) == 0 || strings.TrimSpace(p.Tasks[0].Title) == "") {
		return "no concrete task"
	}
	if c.noQuestions && len(p.Asks) != 0 {
		return "unnecessary clarification"
	}
	return ""
}

func run(ctx context.Context, endpoint llm.Endpoint, client *llm.Client, selected []evalCase) []result {
	results := make([]result, 0, len(selected))
	for _, c := range selected {
		start := time.Now()
		request := "PROJECTS:\n(none)\nTHE CHAT: Main\nTHE CHAT'S OWN PROJECT: none\nRECENT MESSAGES (oldest first):\n(none)\nNEW MESSAGE:\n" + c.input
		resp, err := client.Chat(ctx, endpoint, llm.Request{JSON: true, Messages: []llm.Message{{Role: "system", Content: agent.PlannerPrompt()}, {Role: "user", Content: request}}}, nil)
		r := result{Name: c.name, LatencyMS: time.Since(start).Milliseconds()}
		if err != nil {
			r.Reason = "model request failed"
		} else {
			r.Reason = check(c, resp.Content)
			r.PromptTokens, r.OutputTokens = resp.Usage.Prompt, resp.Usage.Completion
		}
		r.Pass = r.Reason == ""
		results = append(results, r)
	}
	return results
}

// runParallel keeps results in case order even when requests complete out of order.
func runParallel(ctx context.Context, endpoint llm.Endpoint, client *llm.Client, selected []evalCase, concurrency int) []result {
	results := make([]result, len(selected))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = run(ctx, endpoint, client, selected[index:index+1])[0]
			}
		}()
	}
	for i := range selected {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return results
}

func main() {
	base := flag.String("base-url", "", "OpenAI-compatible API base URL (required)")
	model := flag.String("model", "", "model identifier (required)")
	timeout := flag.Duration("timeout", 20*time.Minute, "total evaluation timeout")
	selected := flag.String("cases", "", "comma-separated case names (default: all)")
	maxOutput := flag.Int("max-output", 0, "optional maximum output tokens per call (0 uses server defaults)")
	repeat := flag.Int("repeat", 1, "repeat the selected cases (1-10)")
	concurrency := flag.Int("concurrency", 1, "parallel requests (1-4; default 1)")
	flag.Parse()
	if *base == "" || *model == "" {
		fmt.Fprintln(os.Stderr, "provide -base-url and -model; optional API key comes from PRISM_EVAL_API_KEY")
		os.Exit(2)
	}
	if *repeat < 1 || *repeat > 10 || *maxOutput < 0 || *concurrency < 1 || *concurrency > 4 {
		fmt.Fprintln(os.Stderr, "repeat must be 1-10, concurrency 1-4, and max-output non-negative")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	runCases := cases
	if strings.TrimSpace(*selected) != "" {
		requested := map[string]bool{}
		for _, name := range strings.Split(*selected, ",") {
			requested[strings.TrimSpace(name)] = true
		}
		runCases = nil
		for _, c := range cases {
			if requested[c.name] {
				runCases = append(runCases, c)
				delete(requested, c.name)
			}
		}
		if len(requested) > 0 || len(runCases) == 0 {
			fmt.Fprintln(os.Stderr, "unknown or empty case selection")
			os.Exit(2)
		}
	}
	client := llm.NewClient()
	endpoint := llm.Endpoint{BaseURL: *base, APIKey: os.Getenv("PRISM_EVAL_API_KEY"), Model: *model, Kind: "chat", MaxOut: *maxOutput}
	var results []result
	started := time.Now()
	for i := 0; i < *repeat; i++ {
		results = append(results, runParallel(ctx, endpoint, client, runCases, *concurrency)...)
		if ctx.Err() != nil {
			break
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		ElapsedMS int64    `json:"elapsed_ms"`
		Results   []result `json:"results"`
	}{time.Since(started).Milliseconds(), results})
	for _, r := range results {
		if !r.Pass {
			os.Exit(1)
		}
	}
}
