package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Deep analysis goes beyond reflection's "several facts agree": it reads a whole bank the way an analyst
// would and derives what is not stated outright — patterns that recur (inductive), things that follow from
// what is known (deductive), the most likely explanation of an oddity (abductive, labelled as a hypothesis
// and capped in confidence), how something changed over time, risks, and the questions the memory cannot
// answer yet. It also finds facts that contradict each other (they land in the review inbox), near-duplicate
// facts (the weaker is retired) and keeps a short profile card of the bank up to date. The design follows
// Honcho's deriver/dreamer split and Hindsight's evidence-backed observations; every derived belief keeps
// evidence links to the facts it rests on and is revised when they change. Insights are conclusions whose
// source is "analysis" and whose tag names the type, so the Memory page, graph and stale-review flow already
// handle them.
const (
	DefaultAnalyzeMin = 12
	maxAnalyzeFacts   = 120
	maxHypothesisConf = 0.6
)

var insightNeeds = map[string]int{"pattern": 3, "deduction": 2, "hypothesis": 2, "trend": 2, "preference": 2, "risk": 2, "question": 1}

const analyzePrompt = `You are the analytical mind of an assistant's long-term memory. You get FACTS (numbered, dated) from ONE memory bank, the CONCLUSIONS already drawn from them by reflection (read-only), the INSIGHTS you derived earlier (you may change them) and the bank's current profile CARD.

Think like a careful analyst. Look for:
- "pattern": something that recurs across 3+ facts (habits, tastes, recurring problems, how the user works). Inductive.
- "deduction": something that necessarily follows from the facts together, which no single fact states. Deductive.
- "hypothesis": the most plausible explanation of a fact or a cluster of facts (a motive, a cause, an unstated goal). Abductive. Phrase it with "probably" or "may"; it is a guess, never state it as known.
- "trend": how something changed over time (use the dates): a growing interest, an abandoned plan, a shifted preference.
- "preference": a stable like, dislike or working style that several facts show.
- "risk": something likely to go wrong, be forgotten or become a problem, judging from the facts.
- "question": an important thing the memory does NOT know but that would matter (gap). Write it as a question.
Also report:
- "contradictions": pairs of FACT ids that cannot both be true now (not just different topics). Say why.
- "duplicates": groups of FACT ids that say the same thing; keep the best-worded one.
- "card": a compact profile of what this bank is about (for the user's bank: who they are, what they do, what they care about, how they like to work) — plain sentences, max 700 characters, only what the facts support. Return "" if nothing changed.

Rules: never invent; cite only fact ids you were given, and never a conclusion or insight id as evidence; every insight is one self-contained sentence in the third person; do not restate a single fact or an existing conclusion; prefer a few sharp insights over many vague ones (at most 8 changes). Insight actions: "new", "strengthen" (id of an existing insight + only the NEW evidence ids), "revise" (id + corrected text + evidence), "retire" (id). "confidence" is 0.0-1.0.
Answer JSON only: {"insights":[{"action":"new|strengthen|revise|retire","id":null,"type":"pattern|deduction|hypothesis|trend|preference|risk|question","text":"...","evidence":[ids],"confidence":0.0}],"contradictions":[{"a":1,"b":2,"note":"..."}],"duplicates":[{"keep":1,"drop":[2]}],"card":"..."}`

type AnalyzeResult struct {
	Bank           string `json:"bank"`
	Considered     int    `json:"considered"`
	Insights       int    `json:"insights"`
	Strengthened   int    `json:"strengthened"`
	Revised        int    `json:"revised"`
	Retired        int    `json:"retired"`
	Contradictions int    `json:"contradictions"`
	Duplicates     int    `json:"duplicates"`
	CardUpdated    bool   `json:"card_updated"`
	Skipped        string `json:"skipped,omitempty"`
}

func (r AnalyzeResult) Changes() int {
	n := r.Insights + r.Strengthened + r.Revised + r.Retired + r.Contradictions + r.Duplicates
	if r.CardUpdated {
		n++
	}
	return n
}

func (r AnalyzeResult) String() string {
	if r.Skipped != "" {
		return fmt.Sprintf("%s: skipped (%s)", r.Bank, r.Skipped)
	}
	return fmt.Sprintf("%s: %d insights (+%d strengthened, %d revised, %d retired), %d contradictions, %d duplicates%s (from %d facts)",
		r.Bank, r.Insights, r.Strengthened, r.Revised, r.Retired, r.Contradictions, r.Duplicates, map[bool]string{true: ", card updated"}[r.CardUpdated], r.Considered)
}

// Insight is the type tag of an analysis-derived conclusion ("" for anything else).
func insightType(tags []string) string {
	for _, t := range tags {
		if _, ok := insightNeeds[t]; ok {
			return t
		}
	}
	return ""
}

func (s *Service) analysisInsights(ctx context.Context, bankID int64) ([]conclusionRow, error) {
	rows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.bank_id=$1 AND f.kind='conclusion' AND f.source='analysis' AND f.valid_to IS NULL ORDER BY f.id`, bankID)
	if err != nil {
		return nil, err
	}
	var out []conclusionRow
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, conclusionRow{Fact: f})
	}
	rows.Close()
	for i := range out {
		er, err := s.db.Query(ctx, `SELECT CASE WHEN e.a=$1 THEN e.b ELSE e.a END FROM memory_links e WHERE (e.a=$1 OR e.b=$1) AND e.kind='evidence'`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for er.Next() {
			var id int64
			if er.Scan(&id) == nil {
				out[i].evidence = append(out[i].evidence, id)
			}
		}
		er.Close()
	}
	return out, nil
}

// Analyze runs the deep analysis on one bank. Without force it waits for minNew new usable facts.
func (s *Service) Analyze(ctx context.Context, bankID int64, force bool, minNew int) (AnalyzeResult, error) {
	var res AnalyzeResult
	var b Bank
	var analyzed *time.Time
	var card string
	if err := s.db.QueryRow(ctx, `SELECT id,kind,name,owner,description,status,created_at,analyzed_at,card FROM memory_banks WHERE id=$1`, bankID).
		Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt, &analyzed, &card); err != nil {
		return res, errors.New("memory bank not found")
	}
	res.Bank = b.Label()
	if s.llm.RoleRef(ctx, "chat") == "" && s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no model configured"
		return res, nil
	}
	if minNew <= 0 {
		minNew = DefaultAnalyzeMin
	}
	rows, err := s.db.Query(ctx, `SELECT id,text,created_at FROM memory_facts
		WHERE bank_id=$1 AND kind='fact' AND valid_to IS NULL AND confidence>=0.5 ORDER BY created_at DESC, id DESC LIMIT $2`, bankID, maxAnalyzeFacts)
	if err != nil {
		return res, err
	}
	type fr struct {
		id      int64
		text    string
		created time.Time
	}
	var facts []fr
	fresh := 0
	byID := map[int64]bool{}
	for rows.Next() {
		var f fr
		if rows.Scan(&f.id, &f.text, &f.created) == nil {
			facts = append(facts, f)
			byID[f.id] = true
			if analyzed == nil || f.created.After(*analyzed) {
				fresh++
			}
		}
	}
	rows.Close()
	res.Considered = len(facts)
	if len(facts) < 4 {
		res.Skipped = "fewer than four trusted facts"
		return res, nil
	}
	if !force && fresh < minNew {
		res.Skipped = fmt.Sprintf("only %d new facts", fresh)
		return res, nil
	}
	concl, err := s.conclusions(ctx, bankID)
	if err != nil {
		return res, err
	}
	ins, err := s.analysisInsights(ctx, bankID)
	if err != nil {
		return res, err
	}
	var sb strings.Builder
	sb.WriteString("FACTS (newest first):\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "%d [%s]: %s\n", f.id, f.created.Format("2006-01-02"), f.text)
	}
	sb.WriteString("\nCONCLUSIONS (read-only):\n")
	for _, c := range concl {
		fmt.Fprintf(&sb, "- %s\n", c.Text)
	}
	if len(concl) == 0 {
		sb.WriteString("(none)\n")
	}
	sb.WriteString("\nINSIGHTS (yours):\n")
	own := map[int64]*conclusionRow{}
	for i := range ins {
		c := &ins[i]
		own[c.ID] = c
		fmt.Fprintf(&sb, "%d: [%s] %s [confidence %.2f, evidence %v]\n", c.ID, insightType(c.Tags), c.Text, c.Confidence, c.evidence)
	}
	if len(ins) == 0 {
		sb.WriteString("(none yet)\n")
	}
	fmt.Fprintf(&sb, "\nCARD:\n%s\n", strings.TrimSpace(card))
	var parsed struct {
		Insights []struct {
			Action     string  `json:"action"`
			ID         *int64  `json:"id"`
			Type       string  `json:"type"`
			Text       string  `json:"text"`
			Evidence   []int64 `json:"evidence"`
			Confidence float64 `json:"confidence"`
		} `json:"insights"`
		Contradictions []struct {
			A    int64  `json:"a"`
			B    int64  `json:"b"`
			Note string `json:"note"`
		} `json:"contradictions"`
		Duplicates []struct {
			Keep int64   `json:"keep"`
			Drop []int64 `json:"drop"`
		} `json:"duplicates"`
		Card string `json:"card"`
	}
	role := "role:chat"
	if s.llm.RoleRef(ctx, "chat") == "" {
		role = "role:fast"
	}
	if err := s.llm.CompleteJSON(ctx, role, analyzePrompt, s.guidance(ctx)+"Bank: "+b.Label()+"\n\n"+sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("analysis: %w", err)
	}
	if len(parsed.Insights) > 8 {
		parsed.Insights = parsed.Insights[:8]
	}
	for _, ch := range parsed.Insights {
		ev := validEvidence(ch.Evidence, byID)
		text := strings.TrimSpace(ch.Text)
		if len(text) > 400 {
			text = text[:400]
		}
		typ := strings.ToLower(strings.TrimSpace(ch.Type))
		need, known := insightNeeds[typ]
		capConf := maxConclusionConf
		if typ == "hypothesis" || typ == "question" {
			capConf = maxHypothesisConf
		}
		switch strings.ToLower(strings.TrimSpace(ch.Action)) {
		case "new":
			if !known || len(ev) < need || text == "" {
				continue
			}
			if dup := similarConclusion(text, ins); dup != nil {
				if s.addEvidence(ctx, dup.ID, ev, ch.Confidence) {
					res.Strengthened++
				}
				continue
			}
			if similarConclusion(text, concl) != nil {
				continue // reflection already says this
			}
			if _, err := s.insertDerived(ctx, bankID, text, ev, ch.Confidence, nil, "analysis", []string{"insight", typ}, capConf); err == nil {
				res.Insights++
			}
		case "strengthen":
			if ch.ID == nil || own[*ch.ID] == nil || len(ev) == 0 {
				continue
			}
			if s.addEvidence(ctx, *ch.ID, ev, ch.Confidence) {
				res.Strengthened++
			}
		case "revise":
			if ch.ID == nil || own[*ch.ID] == nil || text == "" {
				continue
			}
			old := own[*ch.ID]
			ev = validEvidence(append(append([]int64{}, old.evidence...), ev...), byID)
			t := insightType(old.Tags)
			if need := insightNeeds[t]; len(ev) < need {
				continue
			}
			if t == "hypothesis" || t == "question" {
				capConf = maxHypothesisConf
			}
			if _, err := s.insertDerived(ctx, bankID, text, ev, ch.Confidence, ch.ID, "analysis", old.Tags, capConf); err == nil {
				res.Revised++
			}
		case "retire":
			if ch.ID == nil || own[*ch.ID] == nil {
				continue
			}
			if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1 AND kind='conclusion' AND valid_to IS NULL`, *ch.ID); err == nil {
				res.Retired++
			}
		}
	}
	for i, c := range parsed.Contradictions {
		if i >= 6 || c.A == c.B || !byID[c.A] || !byID[c.B] {
			continue
		}
		var have bool
		x, y := order(c.A, c.B)
		_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_links WHERE a=$1 AND b=$2 AND kind=$3)`, x, y, LinkContradicts).Scan(&have)
		if have {
			continue
		}
		if s.Link(ctx, c.A, c.B, LinkContradicts, strings.TrimSpace(c.Note), "auto", 0.7) == nil {
			res.Contradictions++
		}
	}
	texts := map[int64]string{}
	for _, f := range facts {
		texts[f.id] = f.text
	}
	for _, d := range parsed.Duplicates {
		if !byID[d.Keep] {
			continue
		}
		for _, id := range d.Drop {
			if id == d.Keep || !byID[id] || jaccard(texts[id], texts[d.Keep]) < 0.3 {
				continue
			}
			if r, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND kind='fact' AND NOT pinned AND valid_to IS NULL`, id, d.Keep); err == nil && r.RowsAffected() > 0 {
				byID[id] = false
				res.Duplicates++
			}
		}
	}
	if c := strings.TrimSpace(parsed.Card); c != "" && c != strings.TrimSpace(card) {
		if len(c) > 900 {
			c = c[:900]
		}
		if _, err := s.db.Exec(ctx, `UPDATE memory_banks SET card=$2 WHERE id=$1`, bankID, c); err == nil {
			res.CardUpdated = true
		}
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_banks SET analyzed_at=now() WHERE id=$1`, bankID)
	if res.Changes() > 0 {
		s.changed()
	}
	return res, nil
}

// AnalyzeDue analyses the banks that gathered enough new facts since their last analysis (least recently
// analysed first, at most maxBanks per call).
func (s *Service) AnalyzeDue(ctx context.Context, minNew, maxBanks int) ([]AnalyzeResult, error) {
	if minNew <= 0 {
		minNew = DefaultAnalyzeMin
	}
	rows, err := s.db.Query(ctx, `SELECT b.id FROM memory_banks b WHERE b.status='active' AND
		(SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.kind='fact' AND f.valid_to IS NULL AND f.confidence>=0.5
			AND (b.analyzed_at IS NULL OR f.created_at>b.analyzed_at)) >= $1
		ORDER BY b.analyzed_at NULLS FIRST LIMIT $2`, minNew, maxBanks)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []AnalyzeResult
	for _, id := range ids {
		r, err := s.Analyze(ctx, id, false, minNew)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// BankHealth says, per bank, how close reflection and analysis are to running and why not — the answer to
// "why has nothing been concluded here?".
type BankHealth struct {
	ID           int64      `json:"id"`
	Bank         string     `json:"bank"`
	Facts        int        `json:"facts"`
	Usable       int        `json:"usable"`     // trusted facts (confidence >= 0.5) that can serve as evidence
	Unverified   int        `json:"unverified"` // learned from untrusted content, not yet corroborated
	FreshReflect int        `json:"fresh_reflect"`
	FreshAnalyze int        `json:"fresh_analyze"`
	Conclusions  int        `json:"conclusions"`
	Insights     int        `json:"insights"`
	ReflectedAt  *time.Time `json:"reflected_at"`
	AnalyzedAt   *time.Time `json:"analyzed_at"`
	Card         string     `json:"card"`
	Stale        int        `json:"stale"`
	Entities     int        `json:"entities"`
	Mentioned    int        `json:"mentioned"`
	Contradicts  int        `json:"contradictions"`
	Docs         int        `json:"docs"`
}

func (s *Service) Health(ctx context.Context) ([]BankHealth, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id,b.kind,b.name,b.owner,b.reflected_at,b.analyzed_at,b.card,
		count(*) FILTER (WHERE f.kind='fact'),
		count(*) FILTER (WHERE f.kind='fact' AND f.confidence>=0.5),
		count(*) FILTER (WHERE f.kind='fact' AND f.confidence<0.5),
		count(*) FILTER (WHERE f.kind='fact' AND f.confidence>=0.5 AND (b.reflected_at IS NULL OR f.created_at>b.reflected_at)),
		count(*) FILTER (WHERE f.kind='fact' AND f.confidence>=0.5 AND (b.analyzed_at IS NULL OR f.created_at>b.analyzed_at)),
		count(*) FILTER (WHERE f.kind='conclusion' AND f.source<>'analysis'),
		count(*) FILTER (WHERE f.kind='conclusion' AND f.source='analysis'),
		count(*) FILTER (WHERE f.kind='conclusion' AND EXISTS(SELECT 1 FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=f.id THEN e.b ELSE e.a END
			WHERE (e.a=f.id OR e.b=f.id) AND e.kind='evidence' AND x.kind='fact' AND x.valid_to IS NOT NULL)),
		(SELECT count(*) FROM memory_entities en WHERE en.bank_id=b.id),
		count(*) FILTER (WHERE f.kind='fact' AND EXISTS(SELECT 1 FROM memory_entity_mentions m WHERE m.fact_id=f.id)),
		(SELECT count(*) FROM memory_links l JOIN memory_facts a ON a.id=l.a JOIN memory_facts c ON c.id=l.b
			WHERE l.kind='contradicts' AND a.bank_id=b.id AND a.valid_to IS NULL AND c.valid_to IS NULL),
		count(*) FILTER (WHERE f.kind='fact' AND f.source LIKE 'document:%')
		FROM memory_banks b LEFT JOIN memory_facts f ON f.bank_id=b.id AND f.valid_to IS NULL
		WHERE b.status='active' GROUP BY b.id ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BankHealth{}
	for rows.Next() {
		var h BankHealth
		var b Bank
		if err := rows.Scan(&h.ID, &b.Kind, &b.Name, &b.Owner, &h.ReflectedAt, &h.AnalyzedAt, &h.Card, &h.Facts, &h.Usable, &h.Unverified,
			&h.FreshReflect, &h.FreshAnalyze, &h.Conclusions, &h.Insights, &h.Stale, &h.Entities, &h.Mentioned, &h.Contradicts, &h.Docs); err != nil {
			return nil, err
		}
		h.Bank = b.Label()
		out = append(out, h)
	}
	return out, rows.Err()
}

// userCard is the analysis' profile card of the user's own bank, "" if there is none yet.
func (s *Service) userCard(ctx context.Context) string {
	var c string
	_ = s.db.QueryRow(ctx, `SELECT card FROM memory_banks WHERE kind='user' AND status='active' ORDER BY id LIMIT 1`).Scan(&c)
	return strings.TrimSpace(c)
}
