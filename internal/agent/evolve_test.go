package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"prism/internal/settings"
	"prism/internal/tools"
)

func TestAutonomyDialDecidesWhatEvolutionMayChangeAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	soul := "You are Scout, a web researcher.\nAlways cite sources.\nPrefer primary sources.\nKeep answers short.\nNever guess prices.\nAsk when a request is unclear."
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Scout", Group: "Web", Description: "web research", Soul: soul, Tools: []string{"clock"}, Traits: []string{"web"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	propose := func(agent string, args map[string]any) string {
		args["agent"] = agent
		args["rationale"] = "test"
		tool, _ := h.e.Tools.Get("evolve_propose")
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, &tools.Env{Agent: "Metis"}, b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	small := soul + "\nAlways state the date of a price."
	big := "You are Scout. Completely different text.\nNew line one.\nNew line two.\nNew line three.\nNew line four."

	if out := propose("Scout", map[string]any{"kind": "soul", "soul": small}); !strings.Contains(out, "awaiting") {
		t.Fatalf("safe (default): everything waits: %q", out)
	}
	_ = h.e.Settings.Set(ctx, settings.KeyAutonomy, settings.Autonomy{EvolveMode: "guarded"})
	if out := propose("Scout", map[string]any{"kind": "soul", "soul": big}); !strings.Contains(out, "awaiting") {
		t.Fatalf("guarded: a large rewrite still waits: %q", out)
	}
	out := propose("Scout", map[string]any{"kind": "soul", "soul": small + " "}) // same as the pending small one but applies as it is small
	_ = out
	p, _ := h.e.Profiles.Get(ctx, "Scout")
	if !strings.Contains(p.Soul, "state the date") {
		// the first small proposal may have been identical; propose a different small edit
		propose("Scout", map[string]any{"kind": "soul", "soul": soul + "\nSay when a source is old."})
		p, _ = h.e.Profiles.Get(ctx, "Scout")
	}
	if !strings.Contains(p.Soul, "state the date") && !strings.Contains(p.Soul, "source is old") {
		t.Fatalf("guarded: a small edit is applied: %q", p.Soul)
	}
	var watching int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM evolution_watch WHERE agent='Scout' AND status='watching'`).Scan(&watching)
	if watching != 1 {
		t.Fatalf("an automatic change is watched: %d", watching)
	}
	if out := propose("Metis", map[string]any{"kind": "traits", "traits": []string{"evolution", "x"}}); !strings.Contains(out, "awaiting") {
		t.Fatalf("staff always wait in guarded mode: %q", out)
	}
	if out := propose("Scout", map[string]any{"kind": "tools", "tools": []string{"clock", "shell"}}); !strings.Contains(out, "awaiting") {
		t.Fatalf("adding a tool that is not read-only waits: %q", out)
	}
	if out := propose("Scout", map[string]any{"kind": "traits", "traits": []string{"web", "prices"}}); !strings.Contains(out, "applied automatically") {
		t.Fatalf("a trait change goes through in guarded mode: %q", out)
	}

	// a clear drop in results rolls the soul change back
	var wid int64
	_ = h.e.DB.QueryRow(ctx, `SELECT proposal_id FROM evolution_watch WHERE agent='Scout' AND kind='soul'`).Scan(&wid)
	_, _ = h.e.DB.Exec(ctx, `UPDATE evolution_watch SET before_done=9, before_total=10 WHERE proposal_id=$1`, wid)
	for i := 0; i < 7; i++ {
		st := "failed"
		if i == 0 {
			st = "done"
		}
		_, _ = h.e.DB.Exec(ctx, `INSERT INTO tasks (from_kind, from_name, to_agent, title, input, status, depth, root_id) VALUES ('user','user','Scout','t','x',$1,0,0)`, st)
	}
	h.e.checkEvolutionWatches(ctx)
	p, _ = h.e.Profiles.Get(ctx, "Scout")
	if strings.Contains(p.Soul, "state the date") || strings.Contains(p.Soul, "source is old") {
		t.Fatalf("the change was rolled back: %q", p.Soul)
	}
	var st string
	_ = h.e.DB.QueryRow(ctx, `SELECT status FROM evolution_watch WHERE proposal_id=$1`, wid).Scan(&st)
	if st != "rolled_back" {
		t.Fatalf("status %q", st)
	}

	// armed applies even a big rewrite; a legacy auto_evolve switch means armed
	_ = h.e.Settings.Set(ctx, settings.KeyAutonomy, settings.Autonomy{AutoEvolve: true})
	if out := propose("Scout", map[string]any{"kind": "soul", "soul": big}); !strings.Contains(out, "applied automatically") {
		t.Fatalf("armed: %q", out)
	}
	var aid int64
	_ = h.e.DB.QueryRow(ctx, `SELECT proposal_id FROM evolution_watch WHERE agent='Scout' AND status='watching' ORDER BY proposal_id DESC LIMIT 1`).Scan(&aid)
	if err := h.e.UndoEvolution(ctx, aid); err != nil {
		t.Fatal(err)
	}
	p, _ = h.e.Profiles.Get(ctx, "Scout")
	if strings.Contains(p.Soul, "Completely different") {
		t.Fatal("the user's undo restores the previous soul")
	}
	_ = time.Now
}
