package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Pooled analysis: deep analysis over the facts of SEVERAL banks at once. A bank with only a handful of facts can never be
// analysed on its own ("fewer than four trusted facts", "only 3 new facts"), and the most interesting patterns — a habit
// that shows in the user's bank and in two projects, a question that one bank's facts raise and another's answer — span
// banks anyway. The facts are read together (each line says which bank it is from), insights are filed in the bank that
// holds most of their evidence, and contradictions are linked across banks like any other.
const poolNote = `
These FACTS come from SEVERAL memory banks; the bank of each fact is shown in brackets. Look especially for what only shows when they are read together: a habit or preference visible in more than one bank, a hypothesis that combines facts from different banks, a question one bank raises that another bank half answers, contradictions across banks. Do not return a card (return ""). Existing INSIGHTS may belong to any of the banks.`

// PoolSelect picks the banks a pooled analysis reads by default: the user's bank and every project and domain bank (agents'
// private "profile" lessons stay out of it).
func (s *Service) PoolSelect(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM memory_banks WHERE status='active' AND kind IN ('user','project','domain') ORDER BY id`)
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

// AnalyzePooled runs one analysis over the given banks (nil = PoolSelect). Without force it waits for minNew facts that are
// new since their bank was last analysed. Every bank in the pool is marked analysed afterwards.
func (s *Service) AnalyzePooled(ctx context.Context, bankIDs []int64, force bool, minNew int) (AnalyzeResult, error) {
	res := AnalyzeResult{Bank: "several banks"}
	if s.llm.RoleRef(ctx, "chat") == "" && s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no model configured"
		return res, nil
	}
	if len(bankIDs) == 0 {
		ids, err := s.PoolSelect(ctx)
		if err != nil {
			return res, err
		}
		bankIDs = ids
	}
	if len(bankIDs) < 2 {
		res.Skipped = "pick at least two banks"
		return res, nil
	}
	if minNew <= 0 {
		minNew = DefaultAnalyzeMin
	}
	labels := map[int64]string{}
	analyzed := map[int64]*time.Time{}
	var names []string
	for _, id := range bankIDs {
		var b Bank
		var at *time.Time
		if err := s.db.QueryRow(ctx, `SELECT id,kind,name,owner,description,status,created_at,analyzed_at FROM memory_banks WHERE id=$1`, id).
			Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt, &at); err != nil {
			continue
		}
		labels[id], analyzed[id] = b.Label(), at
		names = append(names, b.Label())
	}
	if len(labels) < 2 {
		res.Skipped = "pick at least two banks"
		return res, nil
	}
	res.Bank = strings.Join(names, " + ")
	rows, err := s.db.Query(ctx, `SELECT id,bank_id,text,created_at FROM memory_facts
		WHERE bank_id=ANY($1) AND kind='fact' AND valid_to IS NULL AND status<>'proposed' AND confidence>=0.5 ORDER BY created_at DESC, id DESC LIMIT $2`, bankIDs, maxAnalyzeFacts)
	if err != nil {
		return res, err
	}
	type fr struct {
		id, bank int64
		text     string
		created  time.Time
	}
	var facts []fr
	byID := map[int64]bool{}
	bankOf := map[int64]int64{}
	fresh := 0
	for rows.Next() {
		var f fr
		if rows.Scan(&f.id, &f.bank, &f.text, &f.created) != nil {
			continue
		}
		facts = append(facts, f)
		byID[f.id], bankOf[f.id] = true, f.bank
		if at := analyzed[f.bank]; at == nil || f.created.After(*at) {
			fresh++
		}
	}
	rows.Close()
	res.Considered = len(facts)
	if len(facts) < 4 {
		res.Skipped = "fewer than four trusted facts across these banks"
		return res, nil
	}
	if !force && fresh < minNew {
		res.Skipped = fmt.Sprintf("only %d new facts across these banks", fresh)
		return res, nil
	}
	var concl, ins []conclusionRow
	for _, id := range bankIDs {
		if c, err := s.conclusions(ctx, id); err == nil {
			concl = append(concl, c...)
		}
		if i, err := s.analysisInsights(ctx, id); err == nil {
			ins = append(ins, i...)
		}
	}
	var sb strings.Builder
	sb.WriteString("FACTS (newest first):\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "%d [%s] (%s): %s\n", f.id, f.created.Format("2006-01-02"), labels[f.bank], f.text)
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
	sb.WriteString("\nCARD:\n(not used for several banks)\n")
	var parsed analysisParsed
	role := "role:chat"
	if s.llm.RoleRef(ctx, "chat") == "" {
		role = "role:fast"
	}
	if err := s.llm.CompleteJSON(ctx, role, analyzePrompt+poolNote, s.guidance(ctx)+"Banks: "+res.Bank+"\n\n"+sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("pooled analysis: %w", err)
	}
	if len(parsed.Insights) > maxAnalyzeChanges {
		parsed.Insights = parsed.Insights[:maxAnalyzeChanges]
	}
	// an insight lives in the bank that holds most of its evidence (the first one named wins a tie)
	home := func(ev []int64) int64 {
		count := map[int64]int{}
		best, bestN := bankOf[ev[0]], 0
		for _, e := range ev {
			count[bankOf[e]]++
		}
		for _, e := range ev {
			if c := count[bankOf[e]]; c > bestN {
				best, bestN = bankOf[e], c
			}
		}
		return best
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
				continue
			}
			bid := home(ev)
			if _, err := s.insertDerived(ctx, bid, text, ev, ch.Confidence, nil, "analysis", []string{"insight", typ}, capConf); err == nil {
				res.Insights++
				if typ == "question" && ch.Importance >= 4 && s.AskUser != nil {
					_ = s.AskUser(ctx, labels[bid], text, text, ch.Importance)
				}
			}
		case "strengthen":
			if ch.ID != nil && own[*ch.ID] != nil && len(ev) > 0 && s.addEvidence(ctx, *ch.ID, ev, ch.Confidence) {
				res.Strengthened++
			}
		case "revise":
			if ch.ID == nil || own[*ch.ID] == nil || text == "" {
				continue
			}
			old := own[*ch.ID]
			ev = validEvidence(append(append([]int64{}, old.evidence...), ev...), byID)
			t := insightType(old.Tags)
			if len(ev) < insightNeeds[t] {
				continue
			}
			if t == "hypothesis" || t == "question" {
				capConf = maxHypothesisConf
			}
			if _, err := s.insertDerived(ctx, old.BankID, text, ev, ch.Confidence, ch.ID, "analysis", old.Tags, capConf); err == nil {
				res.Revised++
			}
		case "retire":
			if ch.ID != nil && own[*ch.ID] != nil {
				if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1 AND kind='conclusion' AND valid_to IS NULL`, *ch.ID); err == nil {
					res.Retired++
				}
			}
		}
	}
	for i, c := range parsed.Contradictions {
		if i >= 8 || c.A == c.B || !byID[c.A] || !byID[c.B] {
			continue
		}
		var have bool
		x, y := order(c.A, c.B)
		_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_links WHERE a=$1 AND b=$2 AND kind=$3)`, x, y, LinkContradicts).Scan(&have)
		if !have && s.Link(ctx, c.A, c.B, LinkContradicts, strings.TrimSpace(c.Note), "auto", 0.7) == nil {
			res.Contradictions++
		}
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_banks SET analyzed_at=now() WHERE id=ANY($1)`, bankIDs)
	if res.Changes() > 0 {
		s.changed()
	}
	return res, nil
}

// poolSmallBanks finds the banks too small to be analysed on their own (fewer than DefaultAnalyzeMin usable facts) that
// together have enough new facts to be worth a pooled pass, and the pass is run over them plus the user's bank.
func (s *Service) poolSmallBanks(ctx context.Context) (*AnalyzeResult, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id, b.kind,
			(SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.kind='fact' AND f.valid_to IS NULL AND f.status<>'proposed' AND f.confidence>=0.5)
		FROM memory_banks b WHERE b.status='active' AND b.kind IN ('user','project','domain') ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	type bk struct {
		id     int64
		kind   string
		usable int
	}
	var all []bk
	for rows.Next() {
		var b bk
		if rows.Scan(&b.id, &b.kind, &b.usable) == nil {
			all = append(all, b)
		}
	}
	rows.Close()
	var ids []int64
	small := 0
	for _, b := range all {
		if b.usable < DefaultAnalyzeMin {
			ids = append(ids, b.id)
			if b.usable > 0 {
				small++
			}
		}
	}
	if small == 0 || len(ids) < 2 {
		return nil, nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	r, err := s.AnalyzePooled(ctx, ids, false, 0)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
