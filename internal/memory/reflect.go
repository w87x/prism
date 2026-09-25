package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Reflection turns facts into conclusions: durable generalisations that several facts agree on ("User
// prefers short answers and gets annoyed by follow-up questions"), each with the facts it rests on
// (evidence links), a proof count and a confidence. Conclusions are revised, not overwritten: when new
// facts strengthen one it gains evidence, when its evidence is retired it turns stale and the next pass
// revises or retires it. This follows the consolidation step of Hindsight-style memories (observations
// with proof counts) and the reflection step of generative agents.
const (
	ConclusionKind    = "conclusion"
	DefaultReflectMin = 8 // new facts in a bank before it is reflected on automatically
	maxReflectFacts   = 80
	maxConclusionConf = 0.9
	reflectSlowAfter  = 12 * time.Hour
	reflectSlowMin    = 3
)

const reflectPrompt = `You are the reflective part of an assistant's memory. You are given FACTS (numbered) from one memory bank and the CONCLUSIONS drawn from them so far.

A conclusion is a durable generalisation that at least two DIFFERENT facts support: a stable preference, a recurring pattern, a lesson, a relationship between things. It is not a restatement of one fact, not small talk, not a guess. Write it as one self-contained sentence in the third person.

For every change give the ids of the facts it rests on (evidence). Actions:
- "new": a conclusion not yet drawn (needs 2+ evidence ids).
- "strengthen": existing conclusion <id> gains further evidence (list only the NEW supporting fact ids).
- "revise": existing conclusion <id> is imprecise, outdated or STALE (some evidence was retired) — give its corrected text and its current evidence.
- "retire": existing conclusion <id> is no longer supported.

Rules: never invent; only cite ids you were given; prefer few, high-value conclusions (at most 5 changes); if nothing warrants a change return an empty list. "confidence" is how sure you are (0.0-1.0).
Answer JSON only: {"changes":[{"action":"new|strengthen|revise|retire","id":<existing conclusion id or null>,"text":"...","evidence":[<fact ids>],"confidence":0.0}]}`

type ReflectResult struct {
	Bank         string `json:"bank"`
	Considered   int    `json:"considered"`
	Added        int    `json:"added"`
	Strengthened int    `json:"strengthened"`
	Revised      int    `json:"revised"`
	Retired      int    `json:"retired"`
	Skipped      string `json:"skipped,omitempty"`
}

func (r ReflectResult) String() string {
	if r.Skipped != "" {
		return fmt.Sprintf("%s: skipped (%s)", r.Bank, r.Skipped)
	}
	return fmt.Sprintf("%s: %d new, %d strengthened, %d revised, %d retired (from %d facts)", r.Bank, r.Added, r.Strengthened, r.Revised, r.Retired, r.Considered)
}

func (r ReflectResult) Changes() int { return r.Added + r.Strengthened + r.Revised + r.Retired }

type conclusionRow struct {
	Fact
	evidence []int64
}

// Reflect looks at one bank and draws, strengthens, revises or retires conclusions. Without force it
// waits until the bank has enough new facts (or a stale conclusion needs attention).
func (s *Service) Reflect(ctx context.Context, bankID int64, force bool, minNew int) (ReflectResult, error) {
	var res ReflectResult
	var b Bank
	var reflected *time.Time
	if err := s.db.QueryRow(ctx, `SELECT id,kind,name,owner,description,status,created_at,reflected_at FROM memory_banks WHERE id=$1`, bankID).
		Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt, &reflected); err != nil {
		return res, errors.New("memory bank not found")
	}
	res.Bank = b.Label()
	if s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no fast model configured"
		return res, nil
	}
	if minNew <= 0 {
		minNew = DefaultReflectMin
	}

	// Facts learned from untrusted content (confidence < 0.5) never serve as evidence: a poisoned fact must
	// not be laundered into a confident conclusion.
	frows, err := s.db.Query(ctx, `SELECT id,text,created_at FROM memory_facts
		WHERE bank_id=$1 AND kind='fact' AND valid_to IS NULL AND confidence>=0.5 ORDER BY rank DESC, id DESC LIMIT $2`, bankID, maxReflectFacts)
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
	for frows.Next() {
		var f fr
		if frows.Scan(&f.id, &f.text, &f.created) == nil {
			facts = append(facts, f)
			byID[f.id] = true
			if reflected == nil || f.created.After(*reflected) {
				fresh++
			}
		}
	}
	frows.Close()
	res.Considered = len(facts)

	concl, err := s.conclusions(ctx, bankID)
	if err != nil {
		return res, err
	}
	stale := 0
	for _, c := range concl {
		if c.Stale {
			stale++
		}
	}
	if len(facts) < 2 {
		res.Skipped = "fewer than two facts"
		return res, nil
	}
	// a bank that has been quiet for half a day is reflected on after a handful of facts, not a full batch
	slow := reflected != nil && time.Since(*reflected) > reflectSlowAfter && fresh >= reflectSlowMin
	if !force && fresh < minNew && stale == 0 && !slow {
		res.Skipped = fmt.Sprintf("only %d new facts", fresh)
		return res, nil
	}

	var sb strings.Builder
	sb.WriteString("FACTS:\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "%d: %s\n", f.id, f.text)
	}
	sb.WriteString("\nCONCLUSIONS:\n")
	if len(concl) == 0 {
		sb.WriteString("(none yet)\n")
	}
	own := map[int64]*conclusionRow{}
	for i := range concl {
		c := &concl[i]
		own[c.ID] = c
		note := ""
		if c.Stale {
			note = " STALE — some evidence was retired"
		}
		fmt.Fprintf(&sb, "%d: %s [confidence %.2f, evidence %v]%s\n", c.ID, c.Text, c.Confidence, c.evidence, note)
	}
	var parsed struct {
		Changes []struct {
			Action     string  `json:"action"`
			ID         *int64  `json:"id"`
			Text       string  `json:"text"`
			Evidence   []int64 `json:"evidence"`
			Confidence float64 `json:"confidence"`
		} `json:"changes"`
	}
	if err := s.llm.CompleteJSON(ctx, "role:fast", reflectPrompt, s.guidance(ctx)+"Bank: "+b.Label()+"\n\n"+sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("reflection: %w", err)
	}
	if len(parsed.Changes) > 5 {
		parsed.Changes = parsed.Changes[:5]
	}
	for _, ch := range parsed.Changes {
		ev := validEvidence(ch.Evidence, byID)
		text := strings.TrimSpace(ch.Text)
		if len(text) > 400 {
			text = text[:400]
		}
		switch strings.ToLower(strings.TrimSpace(ch.Action)) {
		case "new":
			if len(ev) < 2 || text == "" {
				continue
			}
			if dup := similarConclusion(text, concl); dup != nil { // already drawn: this is more evidence for it
				if s.addEvidence(ctx, dup.ID, ev, ch.Confidence) {
					res.Strengthened++
				}
				continue
			}
			if _, err := s.insertConclusion(ctx, bankID, text, ev, ch.Confidence, nil); err == nil {
				res.Added++
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
			all := append(append([]int64{}, own[*ch.ID].evidence...), ev...)
			ev = validEvidence(all, byID) // retired evidence does not carry over
			if len(ev) < 2 {
				continue
			}
			if _, err := s.insertConclusion(ctx, bankID, text, ev, ch.Confidence, ch.ID); err == nil {
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
	_, _ = s.db.Exec(ctx, `UPDATE memory_banks SET reflected_at=now() WHERE id=$1`, bankID)
	if res.Changes() > 0 {
		s.changed()
	}
	return res, nil
}

// ReflectDue reflects on the banks that have gathered enough new facts (at most maxBanks per call).
func (s *Service) ReflectDue(ctx context.Context, minNew, maxBanks int) ([]ReflectResult, error) {
	if minNew <= 0 {
		minNew = DefaultReflectMin
	}
	rows, err := s.db.Query(ctx, `SELECT b.id FROM memory_banks b WHERE b.status='active' AND (
			(SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.kind='fact' AND f.valid_to IS NULL AND f.confidence>=0.5
				AND (b.reflected_at IS NULL OR f.created_at>b.reflected_at)) >= $1
			OR (b.reflected_at < now()-interval '12 hours' AND (SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.kind='fact'
				AND f.valid_to IS NULL AND f.confidence>=0.5 AND f.created_at>b.reflected_at) >= 3)
			OR EXISTS (SELECT 1 FROM memory_facts c WHERE c.bank_id=b.id AND c.kind='conclusion' AND c.valid_to IS NULL AND EXISTS (
				SELECT 1 FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=c.id THEN e.b ELSE e.a END
				WHERE (e.a=c.id OR e.b=c.id) AND e.kind='evidence' AND x.kind='fact' AND x.valid_to IS NOT NULL)))
		ORDER BY b.reflected_at NULLS FIRST LIMIT $2`, minNew, maxBanks)
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
	var out []ReflectResult
	for _, id := range ids {
		r, err := s.Reflect(ctx, id, false, minNew)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) conclusions(ctx context.Context, bankID int64) ([]conclusionRow, error) {
	rows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.bank_id=$1 AND f.kind='conclusion' AND f.source NOT IN ('analysis','synthesis','principle') AND f.valid_to IS NULL ORDER BY f.id`, bankID)
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
		sort.Slice(out[i].evidence, func(a, b int) bool { return out[i].evidence[a] < out[i].evidence[b] })
	}
	return out, nil
}

func validEvidence(ids []int64, ok map[int64]bool) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if ok[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func similarConclusion(text string, cs []conclusionRow) *conclusionRow {
	for i := range cs {
		if jaccard(text, cs[i].Text) >= 0.8 {
			return &cs[i]
		}
	}
	return nil
}

// conclusionConfidence blends the model's own estimate with how much evidence there is: five agreeing
// facts make a conclusion much more trustworthy than two, whatever the model says.
func conclusionConfidence(llmConf float64, proof int) float64 {
	if llmConf <= 0 || llmConf > 1 {
		llmConf = 0.6
	}
	c := 0.6*math.Min(llmConf, 0.95) + 0.4*math.Min(1, float64(proof)/5)
	return math.Max(0.5, math.Min(maxConclusionConf, c))
}

// insertConclusion stores a conclusion with its evidence links; replaces retires the conclusion it revises.
func (s *Service) insertConclusion(ctx context.Context, bankID int64, text string, evidence []int64, llmConf float64, replaces *int64) (int64, error) {
	return s.insertDerived(ctx, bankID, text, evidence, llmConf, replaces, "reflection", nil, maxConclusionConf)
}

// insertDerived is insertConclusion for any derived belief: source names the pass that made it ("reflection"
// or "analysis"), tags carry the insight type, and capConf keeps speculative kinds below firm conclusions.
func (s *Service) insertDerived(ctx context.Context, bankID int64, text string, evidence []int64, llmConf float64, replaces *int64, source string, tags []string, capConf float64) (int64, error) {
	conf := float32(math.Min(capConf, conclusionConfidence(llmConf, len(evidence))))
	if tags == nil {
		tags = []string{}
	}
	emb, model := s.embedOne(ctx, text)
	var id int64
	var err error
	if s.VectorOn {
		err = s.db.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,embedding,vec,confidence,source,kind,rank,supersedes,embed_model,tags)
			VALUES($1,$2,$3,$4::vector,$5,$8,'conclusion',1.3,$6,$7,$9) RETURNING id`, bankID, text, emb, s.vecArg(emb), conf, replaces, model, source, tags).Scan(&id)
	} else {
		err = s.db.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,embedding,confidence,source,kind,rank,supersedes,embed_model,tags)
			VALUES($1,$2,$3,$4,$7,'conclusion',1.3,$5,$6,$8) RETURNING id`, bankID, text, emb, conf, replaces, model, source, tags).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	if replaces != nil {
		_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND valid_to IS NULL`, *replaces, id)
	}
	for _, e := range evidence {
		_ = s.Link(ctx, id, e, LinkEvidence, "", "auto", 0.8)
	}
	return id, nil
}

// addEvidence attaches more supporting facts to a conclusion and raises its confidence accordingly.
func (s *Service) addEvidence(ctx context.Context, id int64, evidence []int64, llmConf float64) bool {
	added := 0
	for _, e := range evidence {
		var have bool
		a, b := order(id, e)
		_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_links WHERE a=$1 AND b=$2)`, a, b).Scan(&have)
		if have {
			continue
		}
		if s.Link(ctx, id, e, LinkEvidence, "", "auto", 0.8) == nil {
			added++
		}
	}
	if added == 0 {
		return false
	}
	var proof int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=$1 THEN e.b ELSE e.a END
		WHERE (e.a=$1 OR e.b=$1) AND e.kind='evidence' AND x.kind='fact' AND x.valid_to IS NULL`, id).Scan(&proof)
	_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET confidence=GREATEST(confidence,$2), last_used=now() WHERE id=$1`, id, float32(conclusionConfidence(llmConf, proof)))
	return true
}

// ReflectAll is the manual "reflect everywhere": every active bank is looked at (largest first) and each result, skips
// with their reason included, is returned, so a click that changes nothing still says why. force reflects even
// without new facts; at most maxRuns banks actually call the model.
func (s *Service) ReflectAll(ctx context.Context, force bool, maxRuns int) ([]ReflectResult, error) {
	ids, err := s.busyBanks(ctx)
	if err != nil {
		return nil, err
	}
	var out []ReflectResult
	runs := 0
	for _, id := range ids {
		r, err := s.Reflect(ctx, id, force && runs < maxRuns, 0)
		if err != nil {
			return out, err
		}
		if r.Skipped == "" {
			runs++
		}
		out = append(out, r)
	}
	return out, nil
}

// AnalyzeAll is ReflectAll for deep analysis (unforced: it only runs where enough is new, and says so elsewhere).
func (s *Service) AnalyzeAll(ctx context.Context, maxRuns int) ([]AnalyzeResult, error) {
	ids, err := s.busyBanks(ctx)
	if err != nil {
		return nil, err
	}
	var out []AnalyzeResult
	runs := 0
	for _, id := range ids {
		if runs >= maxRuns {
			break
		}
		r, err := s.Analyze(ctx, id, false, 0)
		if err != nil {
			return out, err
		}
		if r.Skipped == "" {
			runs++
		}
		out = append(out, r)
	}
	return out, nil
}

// busyBanks lists active banks by how many usable facts they hold, most first.
func (s *Service) busyBanks(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id FROM memory_banks b WHERE b.status='active' ORDER BY
		(SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.kind='fact' AND f.valid_to IS NULL AND f.confidence>=0.5) DESC, b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
