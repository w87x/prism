package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Provenance answers "where did this fact come from and what happened to it since": how it entered memory, the task
// or document behind it, the web sites that back it, every earlier or later wording of it, what it rests on (for a
// conclusion) and what rests on it, and how much it has been used and checked.
type Version struct {
	ID         int64      `json:"id"`
	Text       string     `json:"text"`
	Confidence float64    `json:"confidence"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to"`
	Source     string     `json:"source"`
	Current    bool       `json:"current"`
}

type ProvTask struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

type Provenance struct {
	Fact        Fact      `json:"fact"`
	Origin      string    `json:"origin"`  // one plain sentence: how it got here
	Channel     string    `json:"channel"` // you | conversation | agent | document | web | reflection | analysis | confirmed
	Task        *ProvTask `json:"task,omitempty"`
	Document    string    `json:"document,omitempty"`
	Sites       []string  `json:"sites"`
	History     []Version `json:"history"`  // oldest first, ends with the newest
	Evidence    []FactRef `json:"evidence"` // what a conclusion rests on
	UsedBy      []FactRef `json:"used_by"`  // conclusions this fact supports
	Contradicts []FactRef `json:"contradicts"`
	Verified    string    `json:"verified"` // last verification attempt, if any
	Events      []string  `json:"events"`   // a readable timeline
}

func describeSource(src string, tainted bool) (channel, text string) {
	switch {
	case src == "user":
		return "you", "You added it yourself."
	case src == "user (confirmed)":
		return "confirmed", "You confirmed it (a hypothesis or open question you answered)."
	case strings.HasPrefix(src, "document: "):
		return "document", "Learned from the document " + strings.TrimPrefix(src, "document: ") + "."
	case src == "reflection":
		return "reflection", "Drawn by reflection from several facts (see what it rests on below)."
	case src == "analysis":
		return "analysis", "Derived by deep analysis of the facts in its bank (see what it rests on below)."
	case strings.HasPrefix(src, "raw"):
		t := "Distilled from a conversation by the memory digest."
		if strings.Contains(src, "tainted") {
			t += " Untrusted content was in that conversation, so it is treated with caution."
		}
		return "conversation", t
	case strings.HasPrefix(src, "agent:"):
		name := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(src, " (", 2)[0], "agent:"))
		t := "Stored by the agent " + name + " while working."
		if strings.Contains(src, "tainted") {
			t += " It had read web content in that run, so the fact starts unverified until a second site confirms it."
		}
		return "agent", t
	}
	if src == "" {
		return "", "Origin not recorded."
	}
	return "", "Source: " + src + "."
}

func (s *Service) Provenance(ctx context.Context, id int64) (*Provenance, error) {
	f, err := s.GetFact(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("fact #%d does not exist", id)
	}
	p := &Provenance{Fact: f, Sites: f.Origins, History: []Version{}, Evidence: []FactRef{}, UsedBy: []FactRef{}, Contradicts: []FactRef{}, Events: []string{}}
	if p.Sites == nil {
		p.Sites = []string{}
	}
	p.Channel, p.Origin = describeSource(f.Source, false)
	if len(f.Origins) > 0 {
		p.Channel = "web"
		p.Origin += fmt.Sprintf(" Learned from the web (%s).", strings.Join(f.Origins, ", "))
	}
	if f.TaskID != 0 {
		var t ProvTask
		if err := s.db.QueryRow(ctx, `SELECT id,title,to_agent,status FROM tasks WHERE id=$1`, f.TaskID).Scan(&t.ID, &t.Title, &t.Agent, &t.Status); err == nil {
			p.Task = &t
		}
	}
	if d, ok := strings.CutPrefix(f.Source, "document: "); ok {
		p.Document = d
	}
	for _, t := range f.Tags {
		if d, ok := strings.CutPrefix(t, "vt:"); ok {
			p.Verified = "an agent tried to verify it on " + d
		}
	}

	// the chain of wordings: walk back through supersedes, then forward through superseded_by
	chain := []int64{id}
	seen := map[int64]bool{id: true}
	for cur := f; cur.Supersedes != nil && !seen[*cur.Supersedes] && len(chain) < 30; {
		prev, err := s.GetFact(ctx, *cur.Supersedes)
		if err != nil {
			break
		}
		seen[prev.ID] = true
		chain = append([]int64{prev.ID}, chain...)
		cur = prev
	}
	for cur := f; cur.SupersededBy != nil && !seen[*cur.SupersededBy] && len(chain) < 60; {
		next, err := s.GetFact(ctx, *cur.SupersededBy)
		if err != nil {
			break
		}
		seen[next.ID] = true
		chain = append(chain, next.ID)
		cur = next
	}
	for _, cid := range chain {
		v, err := s.GetFact(ctx, cid)
		if err != nil {
			continue
		}
		p.History = append(p.History, Version{ID: v.ID, Text: v.Text, Confidence: v.Confidence, ValidFrom: v.ValidFrom, ValidTo: v.ValidTo, Source: v.Source, Current: v.ID == id})
	}

	ref := func(q string, args ...any) []FactRef {
		out := []FactRef{}
		rows, err := s.db.Query(ctx, q, args...)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var r FactRef
			var conf, rank float32
			if rows.Scan(&r.ID, &r.Bank, &r.Text, &conf, &rank) == nil {
				r.Confidence, r.Rank = float64(conf), float64(rank)
				out = append(out, r)
			}
		}
		return out
	}
	const cols = `x.id, b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END, x.text, x.confidence, x.rank`
	other := `CASE WHEN l.a=$1 THEN l.b ELSE l.a END`
	if f.Kind == ConclusionKind {
		p.Evidence = ref(`SELECT `+cols+` FROM memory_links l JOIN memory_facts x ON x.id=`+other+` JOIN memory_banks b ON b.id=x.bank_id
			WHERE (l.a=$1 OR l.b=$1) AND l.kind='evidence' ORDER BY x.valid_to IS NOT NULL, x.id`, id)
	} else {
		p.UsedBy = ref(`SELECT `+cols+` FROM memory_links l JOIN memory_facts x ON x.id=`+other+` JOIN memory_banks b ON b.id=x.bank_id
			WHERE (l.a=$1 OR l.b=$1) AND l.kind='evidence' AND x.kind='conclusion' ORDER BY x.valid_to IS NOT NULL, x.id`, id)
	}
	p.Contradicts = ref(`SELECT `+cols+` FROM memory_links l JOIN memory_facts x ON x.id=`+other+` JOIN memory_banks b ON b.id=x.bank_id
		WHERE (l.a=$1 OR l.b=$1) AND l.kind='contradicts' ORDER BY x.id`, id)

	ev := func(t time.Time, format string, a ...any) {
		p.Events = append(p.Events, t.Format("2006-01-02 15:04")+"  "+fmt.Sprintf(format, a...))
	}
	ev(f.CreatedAt, "stored (%s, confidence %.0f%%)", firstNonEmptyStr(nil, f.Source), f.Confidence*100)
	if len(p.History) > 1 {
		for _, v := range p.History {
			if v.ID != id && v.ValidTo != nil {
				ev(*v.ValidTo, "wording #%d replaced", v.ID)
			}
		}
	}
	if f.Hits > 0 {
		ev(time.Now(), "used %d time(s) in answers", f.Hits)
	}
	if f.ValidTo != nil {
		what := "retired"
		if f.SupersededBy != nil {
			what = fmt.Sprintf("replaced by #%d", *f.SupersededBy)
		}
		ev(*f.ValidTo, "%s", what)
	}
	return p, nil
}
