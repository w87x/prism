package memory

import (
	"context"
	"fmt"
	"strings"
)

// Higher levels of thinking. Level 1 is what reflection and analysis derive from raw facts. Level 2 (synthesis)
// reads those level-1 conclusions and insights across every bank and derives what only shows when they are put
// side by side: shared causes, themes that connect different areas, implications for how to help, tensions between
// beliefs. Level 3 (principles) reads level 2 and keeps a short list of standing beliefs about the user and how to
// serve them, the tensions still unresolved, and what is still missing. Every item cites only items of the level
// directly below it (so a chain always bottoms out in real facts), its confidence is capped lower per level and can
// never exceed 90% of what its evidence has, and an item is retired once fewer than two of its supports remain.
const (
	SourceSynthesis = "synthesis"
	SourcePrinciple = "principle"

	DefaultSynthMin   = 6 // new lower-level items needed before a level runs on its own
	synthBatch        = 45
	maxSynthBatches   = 4
	maxSynthConf      = 0.8
	maxPrincipleConf  = 0.7
	evidenceDiscount  = 0.9
	minPrincipleBase  = 4 // level-2 items needed before principles are attempted
	maxPrinciplesLive = 12
)

var synthTypes = map[string]bool{"theme": true, "cause": true, "implication": true, "tension": true, "opportunity": true}
var principleTypes = map[string]bool{"principle": true, "tension": true, "gap": true}

const synthPrompt = `You are the synthesising mind of an assistant's long-term memory. You get LOWER-LEVEL BELIEFS (numbered, each a conclusion or insight already derived from facts, with its bank and confidence) from possibly several memory banks, and the SYNTHESES you made earlier (you may change them).

Work out what only appears when the beliefs are read together:
- "theme": something that runs through beliefs from different areas.
- "cause": a shared or deeper reason behind several beliefs or a change they show (say "probably" when it is a guess).
- "implication": what the beliefs together mean for how an assistant should act or help this person.
- "tension": two beliefs that pull against each other, or a goal that undermines another.
- "opportunity": something worth proposing that the beliefs together make possible.
Rules: never invent; cite ONLY belief ids you were given, at least 2 (from more than one bank when possible); do not restate a single belief; one self-contained third-person sentence each; a few sharp syntheses beat many vague ones (at most 6 changes). Actions: "new", "strengthen" (id of your earlier synthesis + only NEW evidence ids), "revise" (id + corrected text + evidence), "retire" (id). "confidence" is 0.0-1.0.
Answer JSON only: {"items":[{"action":"new|strengthen|revise|retire","id":null,"type":"theme|cause|implication|tension|opportunity","text":"...","evidence":[ids],"confidence":0.0}]}`

const principlePrompt = `You are the deepest layer of an assistant's long-term memory. You get SYNTHESES (numbered, level 2: cross-domain conclusions), and the PRINCIPLES you set earlier (you may change them).

Distil a short list of standing beliefs:
- "principle": a stable truth about this person or their world that should guide how the assistant behaves, phrased as a durable rule of thumb ("Prefers X; when Y, do Z").
- "tension": an unresolved conflict between syntheses that needs the user's judgement, phrased as a question or dilemma.
- "gap": an important thing the syntheses together show is still unknown.
Rules: never invent; cite ONLY synthesis ids you were given, at least 2; principles must be things you would still bet on in a month; at most 5 changes; keep the whole list short (retire what is no longer supported). Actions: "new", "strengthen", "revise", "retire" as before.
Answer JSON only: {"items":[{"action":"new|strengthen|revise|retire","id":null,"type":"principle|tension|gap","text":"...","evidence":[ids],"confidence":0.0}]}`

type SynthResult struct {
	Level    int    `json:"level"`
	Read     int    `json:"read"`
	New      int    `json:"new"`
	Updated  int    `json:"updated"`
	Retired  int    `json:"retired"`
	Skipped  string `json:"skipped,omitempty"`
	Batches  int    `json:"batches"`
	Consumed int    `json:"-"`
}

func (r SynthResult) Changes() int { return r.New + r.Updated + r.Retired }

func (r SynthResult) String() string {
	name := map[int]string{2: "synthesis", 3: "principles"}[r.Level]
	if r.Skipped != "" {
		return fmt.Sprintf("%s: skipped (%s)", name, r.Skipped)
	}
	return fmt.Sprintf("%s: %d new, %d updated, %d retired (from %d items in %d batches)", name, r.New, r.Updated, r.Retired, r.Read, r.Batches)
}

// levelExpr is SQL giving the thinking level of the fact aliased a: 0 fact, 1 reflection/analysis conclusion,
// 2 synthesis, 3 principle.
func levelExpr(a string) string {
	return `(CASE WHEN ` + a + `.kind<>'conclusion' THEN 0 WHEN ` + a + `.source='` + SourcePrinciple + `' THEN 3 WHEN ` + a + `.source='` + SourceSynthesis + `' THEN 2 ELSE 1 END)`
}

func levelSource(level int) string {
	if level == 3 {
		return SourcePrinciple
	}
	return SourceSynthesis
}

type synthItem struct {
	conclusionRow
	bank string
}

// items lists the live conclusions of one level across all active banks (newest first), with their supports.
func (s *Service) levelItems(ctx context.Context, level, limit int) ([]synthItem, error) {
	cond := `f.source NOT IN ('` + SourceSynthesis + `','` + SourcePrinciple + `') AND f.confidence>=0.55`
	switch level {
	case 2:
		cond = `f.source='` + SourceSynthesis + `'`
	case 3:
		cond = `f.source='` + SourcePrinciple + `'`
	}
	rows, err := s.db.Query(ctx, `SELECT `+factCols+`
		FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE b.status='active' AND f.kind='conclusion' AND f.valid_to IS NULL AND `+cond+` ORDER BY f.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var out []synthItem
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, synthItem{conclusionRow: conclusionRow{Fact: f}, bank: f.Bank})
	}
	rows.Close()
	return out, nil
}

// pruneSynth retires derived items of the level whose supports have mostly gone (fewer than two live ones below).
func (s *Service) pruneSynth(ctx context.Context, level int) int {
	r, err := s.db.Exec(ctx, `UPDATE memory_facts f SET valid_to=now() WHERE f.kind='conclusion' AND f.source=$1 AND f.valid_to IS NULL AND
		(SELECT count(*) FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=f.id THEN e.b ELSE e.a END
			WHERE (e.a=f.id OR e.b=f.id) AND e.kind='evidence' AND x.valid_to IS NULL AND `+levelExpr("x")+` < $2) < 2`, levelSource(level), level)
	if err != nil {
		return 0
	}
	return int(r.RowsAffected())
}

// supports lists the evidence ids of an item (neighbours one level or more below it).
func (s *Service) supports(ctx context.Context, id int64, level int) []int64 {
	rows, err := s.db.Query(ctx, `SELECT x.id FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=$1 THEN e.b ELSE e.a END
		WHERE (e.a=$1 OR e.b=$1) AND e.kind='evidence' AND `+levelExpr("x")+` < $2 ORDER BY x.id`, id, level)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var x int64
		if rows.Scan(&x) == nil {
			out = append(out, x)
		}
	}
	return out
}

// Synthesize runs level 2 or 3. Without force it waits until DefaultSynthMin (or minNew) lower-level items are new
// since the level last ran.
func (s *Service) Synthesize(ctx context.Context, level int, force bool, minNew int) (SynthResult, error) {
	res := SynthResult{Level: level}
	if level != 2 && level != 3 {
		return res, fmt.Errorf("level must be 2 or 3")
	}
	if s.llm.RoleRef(ctx, "chat") == "" && s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no model configured"
		return res, nil
	}
	if minNew <= 0 {
		minNew = DefaultSynthMin
	}
	res.Retired += s.pruneSynth(ctx, level)
	lower, err := s.levelItems(ctx, level-1, synthBatch*maxSynthBatches)
	if err != nil {
		return res, err
	}
	res.Read = len(lower)
	need := 4
	if level == 3 {
		need = minPrincipleBase
	}
	if len(lower) < need {
		res.Skipped = fmt.Sprintf("only %d level-%d items", len(lower), level-1)
		return res, nil
	}
	var since int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_facts f WHERE f.kind='conclusion' AND f.valid_to IS NULL AND `+levelExpr("f")+`=$1 AND
		f.created_at > COALESCE((SELECT ran_at FROM memory_synth WHERE level=$2),'epoch')`, level-1, level).Scan(&since)
	if !force && since < minNew {
		res.Skipped = fmt.Sprintf("only %d new level-%d items", since, level-1)
		return res, nil
	}
	role := "role:chat"
	if s.llm.RoleRef(ctx, "chat") == "" {
		role = "role:fast"
	}
	prompt, capConf := synthPrompt, maxSynthConf
	known := synthTypes
	if level == 3 {
		prompt, capConf, known = principlePrompt, maxPrincipleConf, principleTypes
	}
	for start := 0; start < len(lower) && res.Batches < maxSynthBatches; start += synthBatch {
		end := start + synthBatch
		if end > len(lower) {
			end = len(lower)
		}
		batch := lower[start:end]
		if len(batch) < need && start > 0 {
			break
		}
		byID := map[int64]bool{}
		bankOf := map[int64]int64{}
		var sb strings.Builder
		sb.WriteString("BELIEFS:\n")
		for _, it := range batch {
			byID[it.ID] = true
			bankOf[it.ID] = it.BankID
			fmt.Fprintf(&sb, "%d [%s, %.0f%%]: %s\n", it.ID, it.bank, it.Confidence*100, it.Text)
		}
		mine, err := s.levelItems(ctx, level, 60)
		if err != nil {
			return res, err
		}
		own := map[int64]*synthItem{}
		sb.WriteString("\nYOUR EARLIER ITEMS:\n")
		for i := range mine {
			m := &mine[i]
			m.evidence = s.supports(ctx, m.ID, level)
			own[m.ID] = m
			fmt.Fprintf(&sb, "%d: %s [confidence %.2f, evidence %v]\n", m.ID, m.Text, m.Confidence, m.evidence)
		}
		if len(mine) == 0 {
			sb.WriteString("(none yet)\n")
		}
		var parsed struct {
			Items []struct {
				Action     string  `json:"action"`
				ID         *int64  `json:"id"`
				Type       string  `json:"type"`
				Text       string  `json:"text"`
				Evidence   []int64 `json:"evidence"`
				Confidence float64 `json:"confidence"`
			} `json:"items"`
		}
		if err := s.llm.CompleteJSON(ctx, role, prompt, s.guidance(ctx)+sb.String(), &parsed); err != nil {
			return res, fmt.Errorf("level %d: %w", level, err)
		}
		res.Batches++
		if len(parsed.Items) > 6 {
			parsed.Items = parsed.Items[:6]
		}
		for _, ch := range parsed.Items {
			ev := validEvidence(ch.Evidence, byID)
			text := strings.TrimSpace(ch.Text)
			if len(text) > 400 {
				text = text[:400]
			}
			typ := strings.ToLower(strings.TrimSpace(ch.Type))
			switch strings.ToLower(strings.TrimSpace(ch.Action)) {
			case "new":
				if !known[typ] || len(ev) < 2 || text == "" {
					continue
				}
				if dup := similarSynth(text, mine); dup != nil {
					if s.addSynthEvidence(ctx, dup.ID, ev, ch.Confidence, level) {
						res.Updated++
					}
					continue
				}
				if level == 3 && len(mine) >= maxPrinciplesLive {
					continue
				}
				bank := s.homeBank(ctx, level, ev, bankOf)
				id, err := s.insertDerived(ctx, bank, text, ev, ch.Confidence, nil, levelSource(level), []string{"insight", typ, fmt.Sprintf("L%d", level)}, capConf)
				if err == nil {
					s.discountConfidence(ctx, id, ev)
					res.New++
					mine = append(mine, synthItem{conclusionRow: conclusionRow{Fact: Fact{ID: id, Text: text}}})
				}
			case "strengthen":
				if ch.ID == nil || own[*ch.ID] == nil || len(ev) == 0 {
					continue
				}
				if s.addSynthEvidence(ctx, *ch.ID, ev, ch.Confidence, level) {
					res.Updated++
				}
			case "revise":
				if ch.ID == nil || own[*ch.ID] == nil || text == "" {
					continue
				}
				old := own[*ch.ID]
				ev = validEvidence(append(append([]int64{}, old.evidence...), ev...), byID)
				if len(ev) < 2 {
					continue
				}
				id, err := s.insertDerived(ctx, old.BankID, text, ev, ch.Confidence, ch.ID, levelSource(level), old.Tags, capConf)
				if err == nil {
					s.discountConfidence(ctx, id, ev)
					res.Updated++
				}
			case "retire":
				if ch.ID == nil || own[*ch.ID] == nil {
					continue
				}
				if r, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1 AND kind='conclusion' AND valid_to IS NULL`, *ch.ID); err == nil && r.RowsAffected() > 0 {
					res.Retired++
				}
			}
		}
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO memory_synth(level,ran_at) VALUES($1,now()) ON CONFLICT (level) DO UPDATE SET ran_at=now()`, level)
	if level == 2 { // a changed level 2 can leave principles without support
		res.Retired += s.pruneSynth(ctx, 3)
	}
	if res.Changes() > 0 {
		s.changed()
	}
	return res, nil
}

// SynthesizeDue runs level 2 and then level 3 when each has gathered enough new material.
func (s *Service) SynthesizeDue(ctx context.Context, minNew int) ([]SynthResult, error) {
	var out []SynthResult
	for _, lv := range []int{2, 3} {
		r, err := s.Synthesize(ctx, lv, false, minNew)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func similarSynth(text string, cs []synthItem) *synthItem {
	for i := range cs {
		if jaccard(text, cs[i].Text) >= 0.75 {
			return &cs[i]
		}
	}
	return nil
}

// homeBank picks where a derived item lives: principles in the user's bank, syntheses in the bank most of their
// evidence comes from.
func (s *Service) homeBank(ctx context.Context, level int, ev []int64, bankOf map[int64]int64) int64 {
	if level == 3 {
		var id int64
		if s.db.QueryRow(ctx, `SELECT id FROM memory_banks WHERE kind='user' AND status='active' ORDER BY id LIMIT 1`).Scan(&id) == nil {
			return id
		}
	}
	count := map[int64]int{}
	best, n := int64(0), 0
	for _, e := range ev {
		b := bankOf[e]
		count[b]++
		if count[b] > n {
			best, n = b, count[b]
		}
	}
	if level == 2 && len(count) > 1 { // spans banks: the user's bank is the neutral home
		var id int64
		if s.db.QueryRow(ctx, `SELECT id FROM memory_banks WHERE kind='user' AND status='active' ORDER BY id LIMIT 1`).Scan(&id) == nil {
			return id
		}
	}
	return best
}

// discountConfidence keeps a derived item below what its supports have: at most 90% of their mean.
func (s *Service) discountConfidence(ctx context.Context, id int64, ev []int64) {
	_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET confidence=GREATEST(0.5, LEAST(confidence, $2*(SELECT COALESCE(avg(confidence),1) FROM memory_facts WHERE id=ANY($3)))) WHERE id=$1`,
		id, float32(evidenceDiscount), ev)
}

func (s *Service) addSynthEvidence(ctx context.Context, id int64, ev []int64, conf float64, level int) bool {
	if !s.addEvidence(ctx, id, ev, conf) {
		return false
	}
	capConf := maxSynthConf
	if level == 3 {
		capConf = maxPrincipleConf
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET confidence=LEAST(confidence,$2) WHERE id=$1`, id, float32(capConf))
	return true
}
