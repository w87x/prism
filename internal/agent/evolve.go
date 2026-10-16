package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/tools"
)

// The autonomy dial for evolution. Agents (Metis above all) propose changes to agents' souls, tools and traits; how far those may
// go without asking is a setting, like the SAFE/ARMED switch on tools:
//
//	safe     every proposal waits for you (the default)
//	guarded  a small, low-risk change is applied at once and watched: a trait change; a soul edit that touches a third or less of
//	         it and does not grow it much; a toolset change that only removes tools or adds read-only ones. Staff and Atlas always
//	         wait. Everything else waits for you.
//	armed    every proposal is applied at once and watched
//
// Whatever is applied by itself is watched: the agent's task results are compared with the two weeks before, and a clear drop
// rolls the change back by itself and tells you. The previous version is always kept, so any change can be undone by hand too.

const (
	watchMinTasks   = 6                   // tasks after a change before it is judged
	watchMaxAge     = 14 * 24 * time.Hour // judged anyway after this long (if there is any evidence at all)
	watchDrop       = 0.25                // success rate falling by this much against the two weeks before rolls it back
	watchAbsoluteLo = 0.34                // with no baseline: rolled back when no more than this share of the new tasks succeed
)

// changedShare is how much of new is not in old, by lines (0 = identical, 1 = entirely new).
func changedShare(old, new string) float64 {
	have := map[string]bool{}
	for _, l := range lines(old) {
		have[l] = true
	}
	nl := lines(new)
	if len(nl) == 0 {
		return 1
	}
	n := 0
	for _, l := range nl {
		if !have[l] {
			n++
		}
	}
	return float64(n) / float64(len(nl))
}

// autoApply decides whether a proposal may be applied without asking, under the current dial.
func (e *Engine) autoApply(ctx context.Context, p *Profile, kind, body string) (bool, string) {
	switch settings.Load(ctx, e.Settings, settings.KeyAutonomy, settings.DefaultAutonomy()).EvolveLevel() {
	case "armed":
		return true, "armed"
	case "guarded":
	default:
		return false, ""
	}
	if p.Role == RoleMaint || p.Role == RoleEntry || p.System {
		return false, ""
	}
	switch kind {
	case KindTraits:
		return true, "guarded: a trait change"
	case KindSoul:
		old, nw := strings.TrimSpace(p.Soul), strings.TrimSpace(body)
		if len(nw) > len(old)*13/10+200 {
			return false, ""
		}
		if sh := changedShare(old, nw); sh <= 0.34 {
			return true, fmt.Sprintf("guarded: a small soul edit (%d%% of it changed)", int(sh*100))
		}
	case KindTools:
		have := map[string]bool{}
		for _, t := range p.Tools {
			have[t] = true
		}
		for _, t := range lines(body) {
			if have[t] {
				continue
			}
			tl, ok := e.Tools.Get(t)
			if !ok || tl.Risk != tools.RiskRead {
				return false, ""
			}
		}
		return true, "guarded: tools removed or read-only ones added"
	}
	return false, ""
}

type watchPrev struct {
	Soul   string   `json:"soul"`
	Tools  []string `json:"tools"`
	Traits []string `json:"traits"`
	// After is the field as the change left it (soul text, or the tools / traits joined); a rollback only goes ahead while the
	// profile still has it, so a later edit by the user or another change is never silently overwritten.
	After string `json:"after,omitempty"`
}

func watchField(p *Profile, kind string) string {
	switch kind {
	case KindTools:
		return strings.Join(p.Tools, "\n")
	case KindTraits:
		return strings.Join(p.Traits, "\n")
	}
	return strings.TrimSpace(p.Soul)
}

// noteApplied records what the change left in place, once it has been applied.
func (e *Engine) noteApplied(ctx context.Context, proposalID int64, profileID int64, kind string) {
	p, err := e.Profiles.GetID(ctx, profileID)
	if err != nil {
		return
	}
	_, _ = e.DB.Exec(ctx, `UPDATE evolution_watch SET prev = jsonb_set(prev, '{after}', to_jsonb($2::text)) WHERE proposal_id=$1`, proposalID, watchField(p, kind))
}

// startWatch records what an automatic change replaced and how the agent was doing, so the change can be judged and undone.
func (e *Engine) startWatch(ctx context.Context, proposalID int64, before *Profile, kind string) {
	prev, _ := json.Marshal(watchPrev{Soul: before.Soul, Tools: before.Tools, Traits: before.Traits})
	var bd, bt int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='done'), count(*) FROM tasks WHERE lower(to_agent)=lower($1) AND status IN ('done','failed','partial') AND created_at > now()-interval '14 days'`, before.Name).Scan(&bd, &bt)
	_, _ = e.DB.Exec(ctx, `INSERT INTO evolution_watch (proposal_id, profile_id, agent, kind, prev, before_done, before_total) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
		proposalID, before.ID, before.Name, kind, prev, bd, bt)
}

// rollbackEvolution restores what a watched change replaced.
func (e *Engine) rollbackEvolution(ctx context.Context, proposalID int64, status, why string) error {
	var pid int64
	var kind string
	var prevRaw []byte
	if err := e.DB.QueryRow(ctx, `SELECT profile_id, kind, prev FROM evolution_watch WHERE proposal_id=$1 AND status IN ('watching','kept')`, proposalID).Scan(&pid, &kind, &prevRaw); err != nil {
		return fmt.Errorf("nothing to undo for #%d", proposalID)
	}
	var prev watchPrev
	_ = json.Unmarshal(prevRaw, &prev)
	p, err := e.Profiles.GetID(ctx, pid)
	if err != nil {
		return err
	}
	if prev.After != "" && watchField(p, kind) != prev.After {
		return fmt.Errorf("%s was changed again after this change, so restoring the old version would undo that too — edit it by hand instead", p.Name)
	}
	switch kind {
	case KindTools:
		p.Tools = prev.Tools
	case KindTraits:
		p.Traits = prev.Traits
	default:
		p.Soul = prev.Soul
	}
	if _, err := e.Profiles.Save(ctx, *p, "rollback of evolution #"+fmt.Sprint(proposalID)+": "+why); err != nil {
		return err
	}
	_, _ = e.DB.Exec(ctx, `UPDATE evolution_watch SET status=$2, note=$3, decided_at=now() WHERE proposal_id=$1`, proposalID, status, why)
	_, _ = e.DB.Exec(ctx, `UPDATE evolution_proposals SET status='rolled_back' WHERE id=$1`, proposalID)
	return nil
}

// UndoEvolution is the user's undo for an automatically applied change.
func (e *Engine) UndoEvolution(ctx context.Context, proposalID int64) error {
	return e.rollbackEvolution(ctx, proposalID, "undone", "undone by the user")
}

// checkEvolutionWatches judges the changes being watched.
func (e *Engine) checkEvolutionWatches(ctx context.Context) {
	rows, err := e.DB.Query(ctx, `SELECT proposal_id, agent, applied_at, before_done, before_total FROM evolution_watch WHERE status='watching'`)
	if err != nil {
		return
	}
	type w struct {
		id     int64
		agent  string
		at     time.Time
		bd, bt int
	}
	var ws []w
	for rows.Next() {
		var x w
		if rows.Scan(&x.id, &x.agent, &x.at, &x.bd, &x.bt) == nil {
			ws = append(ws, x)
		}
	}
	rows.Close()
	for _, x := range ws {
		var ad, at int
		_ = e.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='done'), count(*) FROM tasks WHERE lower(to_agent)=lower($1) AND status IN ('done','failed','partial') AND created_at > $2`, x.agent, x.at).Scan(&ad, &at)
		_, _ = e.DB.Exec(ctx, `UPDATE evolution_watch SET after_done=$2, after_total=$3 WHERE proposal_id=$1`, x.id, ad, at)
		aged := time.Since(x.at) > watchMaxAge
		if at < watchMinTasks && !(aged && at > 0) {
			if aged {
				_, _ = e.DB.Exec(ctx, `UPDATE evolution_watch SET status='kept', note='no tasks to judge it by', decided_at=now() WHERE proposal_id=$1`, x.id)
			}
			continue
		}
		rate := float64(ad) / float64(max(at, 1))
		bad := false
		why := fmt.Sprintf("%d of %d tasks since the change succeeded", ad, at)
		if x.bt >= 5 {
			base := float64(x.bd) / float64(x.bt)
			bad = at >= watchMinTasks && rate < base-watchDrop
			why = fmt.Sprintf("success fell from %d%% (%d of %d) to %d%% (%d of %d)", int(base*100), x.bd, x.bt, int(rate*100), ad, at)
		} else {
			bad = at >= watchMinTasks && rate <= watchAbsoluteLo
		}
		if !bad {
			_, _ = e.DB.Exec(ctx, `UPDATE evolution_watch SET status='kept', note=$2, decided_at=now() WHERE proposal_id=$1`, x.id, why)
			continue
		}
		if err := e.rollbackEvolution(ctx, x.id, "rolled_back", why); err == nil {
			e.Notify(ctx, Notice{Level: "attention", Text: fmt.Sprintf("I rolled back the automatic change to %s (evolution #%d): %s. Its previous version is back.", x.agent, x.id, why)})
			e.Emit("evolution.rolledback", map[string]any{"id": x.id, "agent": x.agent})
		} else {
			_, _ = e.DB.Exec(ctx, `UPDATE evolution_watch SET status='kept', note=$2, decided_at=now() WHERE proposal_id=$1`, x.id, "results dropped ("+why+") but not rolled back: "+err.Error())
			e.Notify(ctx, Notice{Level: "attention", Text: fmt.Sprintf("Results of %s dropped after the automatic change #%d (%s), but I did not roll it back: %s.", x.agent, x.id, why, err.Error())})
		}
	}
}

// EvolutionWatchRow is one automatically applied change, for the settings page.
type EvolutionWatchRow struct {
	ProposalID int64     `json:"proposal_id"`
	Agent      string    `json:"agent"`
	Kind       string    `json:"kind"`
	AppliedAt  time.Time `json:"applied_at"`
	Status     string    `json:"status"`
	Note       string    `json:"note"`
	Before     string    `json:"before"`
	After      string    `json:"after"`
	Why        string    `json:"why"`
}

func (e *Engine) EvolutionWatches(ctx context.Context) []EvolutionWatchRow {
	rows, err := e.DB.Query(ctx, `SELECT w.proposal_id, w.agent, w.kind, w.applied_at, w.status, w.note, w.before_done, w.before_total, w.after_done, w.after_total, left(p.rationale, 240)
		FROM evolution_watch w LEFT JOIN evolution_proposals p ON p.id=w.proposal_id ORDER BY w.applied_at DESC LIMIT 40`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []EvolutionWatchRow{}
	for rows.Next() {
		var r EvolutionWatchRow
		var bd, bt, ad, at int
		var why *string
		if rows.Scan(&r.ProposalID, &r.Agent, &r.Kind, &r.AppliedAt, &r.Status, &r.Note, &bd, &bt, &ad, &at, &why) == nil {
			r.Before, r.After = fmt.Sprintf("%d/%d", bd, bt), fmt.Sprintf("%d/%d", ad, at)
			if why != nil {
				r.Why = *why
			}
			out = append(out, r)
		}
	}
	return out
}
