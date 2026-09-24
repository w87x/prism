package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Verification: facts learned from a single web page stay "unverified" (confidence < 0.5) and out of reflection
// until a second, independent site confirms them; analysis hypotheses are only guesses until checked. Instead of
// waiting for a human to click, an agent can be sent to check a handful of them against sources it actually
// reads (see VerifyBrief and the memory_verify tool). Whatever the outcome, an item is marked as tried so it
// is not sent out again for two weeks.
const verifyRetry = 14 * 24 * time.Hour

type VerifyItem struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	Kind string `json:"kind"` // fact | hypothesis
	Bank string `json:"bank"`
}

func triedTag(t time.Time) string { return "vt:" + t.Format("2006-01-02") }

func recentlyTried(tags []string, now time.Time) bool {
	for _, t := range tags {
		if d, ok := strings.CutPrefix(t, "vt:"); ok {
			if at, err := time.Parse("2006-01-02", d); err == nil && now.Sub(at) < verifyRetry {
				return true
			}
		}
	}
	return false
}

// VerifyQueue lists what is worth checking: unverified facts, then open hypotheses.
func (s *Service) VerifyQueue(ctx context.Context, max int) ([]VerifyItem, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id, f.text, f.kind, f.tags, b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END
		FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.valid_to IS NULL AND ((f.kind='fact' AND f.confidence<0.5) OR (f.kind='conclusion' AND f.source='analysis' AND f.tags && ARRAY['hypothesis']))
		ORDER BY (f.kind='fact') DESC, f.rank DESC, f.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifyItem
	now := time.Now()
	for rows.Next() {
		var it VerifyItem
		var tags []string
		var kind string
		if err := rows.Scan(&it.ID, &it.Text, &kind, &tags, &it.Bank); err != nil {
			return nil, err
		}
		if recentlyTried(tags, now) || len(out) >= max {
			continue
		}
		it.Kind = "fact"
		if kind == "conclusion" {
			it.Kind = "hypothesis"
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// VerifyItems loads specific facts as verify items (for a "verify this" button).
func (s *Service) VerifyItems(ctx context.Context, ids []int64) ([]VerifyItem, error) {
	var out []VerifyItem
	for _, id := range ids {
		f, err := s.GetFact(ctx, id)
		if err != nil || f.ValidTo != nil {
			continue
		}
		it := VerifyItem{ID: f.ID, Text: f.Text, Kind: "fact", Bank: f.Bank}
		if f.Kind == "conclusion" {
			it.Kind = "hypothesis"
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to verify")
	}
	return out, nil
}

// MarkVerifyTried stamps items so they are not queued again for a while.
func (s *Service) MarkVerifyTried(ctx context.Context, ids []int64) {
	for _, id := range ids {
		_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET tags = array_append(ARRAY(SELECT t FROM unnest(tags) t WHERE t NOT LIKE 'vt:%'), $2) WHERE id=$1`, id, triedTag(time.Now()))
	}
}

const verifyInstructions = `Verify these claims from the assistant's memory against the web. Each is either a fact learned from one source or a guess memory drew from other facts.

For each one:
1. web_search for it, then web_fetch the most relevant pages. You need TWO independent websites (different domains) that you actually read.
2. Call memory_verify with the id, a verdict and the URLs of the pages you read:
   - "confirmed": two or more independent sites you read say the same.
   - "contradicted": reliable sources say it is wrong or outdated (give them).
   - "unclear": you could not settle it.
3. Do not guess, do not use pages you did not read, and do not spend more than about 3 tool calls per claim.
Finish with one line per claim: its id, the verdict, and a few words why.

CLAIMS:
%s`

func (s *Service) VerifyBrief(items []VerifyItem) (title, input string) {
	var sb strings.Builder
	for _, it := range items {
		kind := "fact learned from the web"
		if it.Kind == "hypothesis" {
			kind = "hypothesis (a guess drawn from other facts)"
		}
		fmt.Fprintf(&sb, "#%d [%s, bank %s]: %s\n", it.ID, kind, it.Bank, it.Text)
	}
	return fmt.Sprintf("Verify %d claim(s) from memory", len(items)), fmt.Sprintf(verifyInstructions, sb.String())
}

// ApplyVerdict records the outcome of a check. origins are the registrable domains of the pages the checker
// read; a confirmation needs two different ones.
func (s *Service) ApplyVerdict(ctx context.Context, id int64, verdict, note string, origins []string) (string, error) {
	f, err := s.GetFact(ctx, id)
	if err != nil || f.ValidTo != nil {
		return "", fmt.Errorf("#%d is not an active fact", id)
	}
	hyp := f.Kind == "conclusion" && slices.Contains(f.Tags, "hypothesis")
	if f.Kind != "fact" && !hyp {
		return "", fmt.Errorf("#%d is neither a fact nor a hypothesis", id)
	}
	var distinct []string
	for _, o := range origins {
		if o != "" && !slices.Contains(distinct, o) {
			distinct = append(distinct, o)
		}
	}
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "confirmed":
		if len(distinct) < 2 {
			return "", errors.New("a confirmation needs pages from two different websites that you actually read")
		}
		if hyp {
			if _, err := s.ResolveInsight(ctx, id, "confirm", ""); err != nil {
				return "", err
			}
			return fmt.Sprintf("#%d confirmed by %s: stored as a trusted fact", id, strings.Join(distinct, ", ")), nil
		}
		all := append(append([]string{}, f.Origins...), distinct...)
		var uniq []string
		for _, o := range all {
			if !slices.Contains(uniq, o) {
				uniq = append(uniq, o)
			}
		}
		nc := corroborated(len(uniq))
		if f.Confidence >= 0.5 {
			nc = max(f.Confidence, 0.85)
		}
		if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET confidence=GREATEST(confidence,$2::real), origins=$3, tags=array_remove(tags,'unverified') WHERE id=$1`, id, float32(nc), uniq); err != nil {
			return "", err
		}
		s.changed()
		return fmt.Sprintf("#%d confirmed by %s: now trusted", id, strings.Join(distinct, ", ")), nil
	case "contradicted":
		if hyp {
			_, err = s.ResolveInsight(ctx, id, "reject", "")
		} else {
			err = s.MarkOutdated(ctx, id)
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("#%d contradicted (%s): retired", id, strings.TrimSpace(note)), nil
	case "unclear":
		s.MarkVerifyTried(ctx, []int64{id})
		return fmt.Sprintf("#%d left as it is (unclear)", id), nil
	}
	return "", fmt.Errorf("unknown verdict %q (confirmed, contradicted or unclear)", verdict)
}
