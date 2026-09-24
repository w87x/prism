package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// EnrichReq says what to look into: one fact, an entity of the graph, a whole bank, or a free-text topic.
type EnrichReq struct {
	FactID   int64  `json:"fact_id"`
	EntityID int64  `json:"entity_id"`
	BankID   int64  `json:"bank_id"`
	Topic    string `json:"topic"`
	Note     string `json:"note"` // extra guidance from the user
}

const enrichInstructions = `Research request from the user: check and enrich what memory holds about the subject below.

Do this:
1. memory_find for what is already known, so you do not repeat it.
2. Research the subject on the web (web_search, then web_fetch on 2-4 good sources; prefer official and primary pages). Verify the facts above where you can, and find what is missing or has changed.
3. Store each new, verified detail with memory_store as a short self-contained fact (bank: %s). Store facts from different sites separately and give each its source_url: the same fact confirmed by an independent site raises its trust automatically.
4. If memory is wrong or out of date, store the corrected fact (it replaces the old one) and say so in your report.
5. Never store guesses, opinions of a single forum post, or anything you could not read yourself. Keep it to at most 12 tool calls.
Finish with a short report: what you added, what you corrected, what you could not verify.`

// EnrichBrief turns a request into a title and a complete instruction for a research task, including what
// memory already knows about the subject.
func (s *Service) EnrichBrief(ctx context.Context, r EnrichReq) (title, input string, err error) {
	var subject, known, bank string
	lines := func(fs []Fact) string {
		var sb strings.Builder
		for i, f := range fs {
			if i >= 15 {
				break
			}
			fmt.Fprintf(&sb, "- %s\n", f.Text)
		}
		return sb.String()
	}
	switch {
	case r.FactID != 0:
		f, err := s.GetFact(ctx, r.FactID)
		if err != nil {
			return "", "", err
		}
		subject, bank = f.Text, f.Bank
		if ls, lerr := s.Links(ctx, r.FactID); lerr == nil {
			var rel []Fact
			for _, l := range ls {
				rel = append(rel, l.Fact)
			}
			known = lines(rel)
		}
	case r.EntityID != 0:
		fs, ferr := s.EntityFacts(ctx, r.EntityID)
		if ferr != nil {
			return "", "", ferr
		}
		var name, kind string
		if err := s.db.QueryRow(ctx, `SELECT name, kind FROM memory_entities WHERE id=$1`, r.EntityID).Scan(&name, &kind); err != nil {
			return "", "", errors.New("entity not found")
		}
		subject, known = fmt.Sprintf("%s (%s)", name, kind), lines(fs)
		if len(fs) > 0 {
			bank = fs[0].Bank
		}
	case r.BankID != 0:
		b, berr := s.bankByID(ctx, r.BankID)
		if berr != nil {
			return "", "", berr
		}
		fs, ferr := s.Facts(ctx, r.BankID, "", false, 15, 0)
		if ferr != nil {
			return "", "", ferr
		}
		subject, bank, known = "everything memory holds in the bank "+b.Label(), b.Label(), lines(fs)
	case strings.TrimSpace(r.Topic) != "":
		subject = strings.TrimSpace(r.Topic)
		if fs, ferr := s.Find(ctx, FindReq{Query: subject, Banks: s.allActive(ctx), K: 10, NoLinks: true}); ferr == nil {
			known = lines(fs)
		}
	default:
		return "", "", errors.New("say what to look into: a fact, an entity, a bank or a topic")
	}
	if bank == "" {
		bank = "the one that fits best (memory_banks lists them)"
	}
	if known == "" {
		known = "(nothing yet)\n"
	}
	input = fmt.Sprintf(enrichInstructions, bank) + "\n\nSUBJECT: " + subject + "\n\nWHAT MEMORY ALREADY HOLDS ABOUT IT:\n" + known
	if n := strings.TrimSpace(r.Note); n != "" {
		input += "\nGuidance from the user: " + n + "\n"
	}
	t := subject
	if rs := []rune(t); len(rs) > 50 {
		t = string(rs[:50]) + "…"
	}
	return "Research: " + t, input, nil
}

func (s *Service) allActive(ctx context.Context) []string {
	bs, err := s.Banks(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range bs {
		if b.Status == "active" && b.Facts > 0 {
			out = append(out, b.Label())
		}
	}
	return out
}

var (
	usedFactRe  = regexp.MustCompile(`(?m)^#(\d+) \(`)
	usedModelRe = regexp.MustCompile(`\(from facts \[([\d ]*)\]`)
)

// UsedFactIDs pulls the memory ids out of what memory_find / memory_models returned to an agent, so a finished
// task can show which memories its answer was built on.
func UsedFactIDs(toolOutput string) []int64 {
	var ids []int64
	add := func(s string) {
		var id int64
		if _, err := fmt.Sscan(s, &id); err == nil && id > 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, m := range usedFactRe.FindAllStringSubmatch(toolOutput, -1) {
		add(m[1])
	}
	for _, m := range usedModelRe.FindAllStringSubmatch(toolOutput, -1) {
		for _, f := range strings.Fields(m[1]) {
			add(f)
		}
	}
	return ids
}
