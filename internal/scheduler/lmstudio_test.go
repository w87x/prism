package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"prism/internal/agent"
	"prism/internal/testutil"
	"prism/internal/tracker"
)

// Runs the job-review prompts against a real model. Skipped unless PRISM_LMSTUDIO (e.g. http://192.168.88.2:1234) is set;
// PRISM_LMSTUDIO_MODEL names the model. The database is the usual throw-away test one.
func realModel(t *testing.T, fake *testutil.FakeLLM) {
	base := os.Getenv("PRISM_LMSTUDIO")
	if base == "" {
		t.Skip("PRISM_LMSTUDIO not set")
	}
	model := os.Getenv("PRISM_LMSTUDIO_MODEL")
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		req["model"] = model
		req["stream"] = false
		delete(req, "tools")
		delete(req, "response_format") // LM Studio only takes json_schema or text
		b, _ := json.Marshal(req)
		c := &http.Client{Timeout: 5 * time.Minute}
		resp, err := c.Post(base+"/v1/chat/completions", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Errorf("model call: %v", err)
			return testutil.Reply{Content: "{}"}
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(raw, &out)
		if len(out.Choices) == 0 {
			t.Errorf("model answered %s", string(raw))
			return testutil.Reply{Content: "{}"}
		}
		return testutil.Reply{Content: out.Choices[0].Message.Content}
	}
}

func TestRealModelJobReview(t *testing.T) {
	s, _, fake := setupFake(t)
	realModel(t, fake)
	ctx := context.Background()
	s.Trackers = &tracker.Service{DB: s.DB}
	if _, err := s.Engine.Profiles.Save(ctx, agent.Profile{Name: "Organizer", Group: "Home", Description: "keeps track", Soul: "You keep trackers.", Tools: []string{"tracker_snapshot", "tracker_changes", "tracker_rows"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	mk := func(name, prompt string) {
		if _, err := s.SaveCron(ctx, Cron{Name: name, Agent: "Atlas", Expr: "@daily", Prompt: prompt, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	// 1: three price checks over different items -> a tracker
	mk("avito x", "Check the Avito price for the FEVM X box (Strix Halo mini PC) and tell me if the cheapest listing is under 700000 RUB. Reply NO_REPLY if nothing.")
	mk("avito y", "Check the Avito price for the FEVM Y box (Strix Halo mini PC) and tell me if the cheapest listing is under 800000 RUB. Reply NO_REPLY if nothing.")
	mk("avito z", "Check the Avito price for the FEVM Z box (Strix Halo mini PC) and tell me if the cheapest listing is under 900000 RUB. Reply NO_REPLY if nothing.")
	// 2: two plain duplicates -> a merge, not a tracker
	mk("water plants", "Remind me every morning to water the plants on the balcony. Reply NO_REPLY if it is not morning.")
	mk("water balcony plants", "Every morning remind me to water the balcony plants. Reply NO_REPLY if it is not morning.")
	// 3: similar wording, different logic -> neither
	mk("backup nas", "Check that last night's NAS backup finished and list any failed shares. Reply NO_REPLY if everything is fine.")
	mk("backup laptop", "Check that last night's laptop backup finished; if it is older than 3 days, tell me to plug the laptop in. Reply NO_REPLY if everything is fine.")

	n, err := s.ReviewJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := s.JobProposals(ctx, "")
	t.Logf("%d proposals", n)
	kinds := map[string]bool{}
	for _, p := range ps {
		kinds[strings.Join(p.Names, "+")] = true
		t.Logf("\n[%s] %s (agent %s)\nWHY: %s\nPROMPT: %s\nPLAN: %s", p.Kind, p.Title, p.NewAgent, p.Rationale, p.NewPrompt, string(p.Plan))
		if p.Kind == "tracker" && len(p.CronIDs) != 3 {
			t.Errorf("the tracker proposal should cover the three Avito jobs: %v", p.CronIDs)
		}
		if p.Kind == "tracker" {
			var plan trackerPlan
			_ = json.Unmarshal(p.Plan, &plan)
			if len(plan.Items) != 3 || !strings.Contains(p.NewPrompt, "tracker_snapshot") {
				t.Errorf("a usable plan has 3 items and a refresh prompt that snapshots: %+v / %s", plan.Items, p.NewPrompt)
			}
		}
	}
}
