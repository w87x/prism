package app

import (
	"context"
	"encoding/json"
	"time"

	"prism/internal/settings"
)

// LED states follow the UI palette: ok (green), standby (blue), attention (orange), warn (yellow), error (red), off.
type LED struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type Status struct {
	Setup      bool   `json:"setup"` // true while no database is connected
	Onboarded  bool   `json:"onboarded"`
	Version    string `json:"version"`
	LEDs       []LED  `json:"leds"`
	Thinking   int    `json:"thinking"`
	QueueDepth int    `json:"queue"`
	Running    int    `json:"running"`
	Waiting    int    `json:"waiting"`
	Asks       int    `json:"asks"`
	RawPending int    `json:"raw_pending"`
	Vector     bool   `json:"pgvector"`
}

// Status computes the current status snapshot for the status bar.
func (a *App) Status(ctx context.Context) Status {
	st := Status{Version: Version}
	if !a.Ready() {
		st.Setup = true
		st.LEDs = []LED{{ID: "db", Label: "DB", State: "attention", Detail: "not configured"}}
		return st
	}
	st.Vector = a.DB.Vector
	st.Onboarded = settings.Load(ctx, a.Settings, settings.KeyOnboarding, settings.Onboarding{}).Done
	dbState := "ok"
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := a.DB.Ping(pctx); err != nil {
		dbState = "error"
	}
	st.LEDs = append(st.LEDs, LED{ID: "db", Label: "DB", State: dbState, Detail: map[bool]string{true: "postgres + pgvector", false: "postgres"}[a.DB.Vector]})

	runs := a.Engine.ActiveRuns()
	st.Thinking = len(runs)
	llmState, llmDetail := "standby", "idle"
	switch {
	case !a.LLM.HasChat(ctx):
		llmState, llmDetail = "attention", "no chat model configured"
	case len(runs) > 0 || a.LLM.Active() > 0:
		llmState, llmDetail = "ok", "generating"
	}
	st.LEDs = append(st.LEDs, LED{ID: "llm", Label: "LLM", State: llmState, Detail: llmDetail})
	emb := "off"
	if a.LLM.HasEmbedding(ctx) {
		emb = "standby"
	}
	st.LEDs = append(st.LEDs, LED{ID: "embed", Label: "EMB", State: emb})

	c := a.Tasks.Counts(ctx)
	st.QueueDepth, st.Running, st.Waiting = c.Queued, c.Running, c.Waiting
	st.Asks = len(a.Engine.PendingAsks())
	st.RawPending = a.Memory.RawCount(ctx)
	q := "standby"
	if c.Queued+c.Running > 0 {
		q = "ok"
	}
	st.LEDs = append(st.LEDs, LED{ID: "queue", Label: "QUEUE", State: q})
	if st.Asks > 0 {
		st.LEDs = append(st.LEDs, LED{ID: "ask", Label: "ASK", State: "attention", Detail: "waiting for you"})
	}
	st.LEDs = append(st.LEDs, a.extensionLEDs(ctx)...)
	return st
}

func (a *App) statusLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.Hub.NumClients() == 0 {
				continue
			}
			sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			st := a.Status(sctx)
			cancel()
			b, _ := json.Marshal(st)
			if s := string(b); s != a.lastStatus {
				a.lastStatus = s
				a.Emit("status", st)
			}
		}
	}
}
