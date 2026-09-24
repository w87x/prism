package metrics

import (
	"context"
	"sort"
	"time"
)

// DelegationChain is one root run's cost breakdown. A root run is one with no parent (an interactive chat
// turn, or a top-level task) — RootMS is that run's own model time. Since the entry agent (Atlas) has no
// direct-work tools of its own (internal/agent/seed.go — only delegate/agent_find/task_status), RootMS on a
// chain that Delegated is, by construction, entirely routing-and-synthesis overhead, not answer content;
// ChildMS is the actual work the delegated specialist(s) did.
type DelegationChain struct {
	RootRun    int64  `json:"root_run"`
	RootAgent  string `json:"root_agent"`
	RootMS     int64  `json:"root_ms"`
	Delegated  bool   `json:"delegated"`
	ChildMS    int64  `json:"child_ms"`
	ChildCalls int    `json:"child_calls"`
}

// Overhead is ChildMS==0's own time when Delegated is false (nothing to divide by); otherwise the share of
// this chain's total LLM time that was the entry agent's own calls rather than the specialist's.
func (c DelegationChain) Overhead() float64 {
	total := c.RootMS + c.ChildMS
	if !c.Delegated || total == 0 {
		return 0
	}
	return float64(c.RootMS) / float64(total)
}

// DelegationReport summarizes delegation overhead over a window: for each independent root run it separates
// the entry agent's own time from time spent in runs it delegated to.
//
// Caveat: run ids are only unique within one process lifetime (Engine.runSeq resets on restart), so a
// window spanning several restarts can occasionally merge two unrelated runs that happened to get the same
// id — acceptable for a trend/overhead estimate, not exact accounting.
type DelegationReport struct {
	Since         time.Time         `json:"since"`
	Roots         int               `json:"roots"`            // total root runs seen
	DelegatedRuns int               `json:"delegated_runs"`   // roots that delegated to at least one child run
	RootMS        int64             `json:"root_ms"`          // sum of entry-agent-own ms, delegated roots only
	ChildMS       int64             `json:"child_ms"`         // sum of delegated-run ms, all delegated roots
	Chains        []DelegationChain `json:"chains,omitempty"` // worst overhead first, capped
}

// Overhead is the overall share of LLM time, across every delegated root in the window, that was the entry
// agent's own routing/synthesis calls rather than the specialist doing the work.
func (r DelegationReport) Overhead() float64 {
	total := r.RootMS + r.ChildMS
	if total == 0 {
		return 0
	}
	return float64(r.RootMS) / float64(total)
}

// DelegationReport builds the report from llm_calls in the window (tool time is a separate, smaller
// question — RecordTool carries the same run_id/parent_run for whoever wants it).
func (s *Store) DelegationReport(ctx context.Context, since time.Duration) (*DelegationReport, error) {
	from := time.Now().Add(-since)
	rows, err := s.DB.Query(ctx, `SELECT run_id, parent_run, agent, ms FROM llm_calls WHERE ts >= $1 AND run_id IS NOT NULL ORDER BY run_id`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		run, parent int64
		agent       string
		ms          int
	}
	var all []row
	for rows.Next() {
		var r row
		var parent *int64
		if err := rows.Scan(&r.run, &parent, &r.agent, &r.ms); err != nil {
			return nil, err
		}
		if parent != nil {
			r.parent = *parent
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	parentByRun := map[int64]int64{}
	agentByRun := map[int64]string{}
	for _, r := range all {
		parentByRun[r.run] = r.parent
		if agentByRun[r.run] == "" {
			agentByRun[r.run] = r.agent
		}
	}
	rootOf := map[int64]int64{}
	var findRoot func(id int64) int64
	findRoot = func(id int64) int64 {
		if r, ok := rootOf[id]; ok {
			return r
		}
		p := parentByRun[id] // 0 when this run's own calls carried no parent, or the parent fell outside the window
		if p == 0 {
			rootOf[id] = id
			return id
		}
		r := findRoot(p)
		rootOf[id] = r
		return r
	}

	chains := map[int64]*DelegationChain{}
	for _, r := range all {
		root := findRoot(r.run)
		c := chains[root]
		if c == nil {
			c = &DelegationChain{RootRun: root, RootAgent: agentByRun[root]}
			chains[root] = c
		}
		if r.run == root {
			c.RootMS += int64(r.ms)
		} else {
			c.ChildMS += int64(r.ms)
			c.ChildCalls++
			c.Delegated = true
		}
	}

	rep := &DelegationReport{Since: from, Roots: len(chains)}
	list := make([]DelegationChain, 0, len(chains))
	for _, c := range chains {
		list = append(list, *c)
		if c.Delegated {
			rep.DelegatedRuns++
			rep.RootMS += c.RootMS
			rep.ChildMS += c.ChildMS
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Overhead() > list[j].Overhead() })
	if len(list) > 30 {
		list = list[:30]
	}
	rep.Chains = list
	return rep, nil
}
