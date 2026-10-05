package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ── required evidence and coverage (memo: "return coverage and gaps as well as ranked claims") ──────────────
//
// memory_find answers "what do you know about X?". A task usually knows what it MUST have: the price, the RAM, the
// release date. Coverage takes that list and answers per item whether memory supports it, disputes it, only has a
// stale or weak version, or has nothing within the searched banks — and says what was searched, so a gap is a
// finding ("not checked / not found here"), not a silent omission. "Not found" is never "does not exist": it only
// describes the searched scope.

const (
	CoverSupported = "supported"
	CoverDisputed  = "disputed"
	CoverStale     = "stale"
	CoverWeak      = "weak"
	CoverNotFound  = "not_found"
)

// Need is one thing the task requires.
type Need struct {
	Item string `json:"item"`
	// MaxAgeDays: evidence learned longer ago than this counts as stale (0 = no freshness constraint).
	MaxAgeDays int `json:"max_age_days,omitempty"`
}

type Coverage struct {
	Need   string `json:"need"`
	Status string `json:"status"`
	Facts  []Fact `json:"facts"`
	Note   string `json:"note"`
}

type CoverageReport struct {
	Searched []string   `json:"searched"`
	MinRel   float64    `json:"min_relevance"`
	Needs    []Coverage `json:"needs"`
	// Gaps counts the needs that are not plainly supported.
	Gaps int `json:"gaps"`
}

// CoverageOf checks each need against the readable banks. It reads only: no fact is reinforced by being checked.
func (s *Service) CoverageOf(ctx context.Context, banks []string, agent string, needs []Need) (*CoverageReport, error) {
	if len(needs) == 0 {
		return nil, fmt.Errorf("list at least one need")
	}
	if len(needs) > 12 {
		needs = needs[:12]
	}
	rep := &CoverageReport{Searched: banks, MinRel: 0.35}
	for _, n := range needs {
		n.Item = strings.TrimSpace(n.Item)
		if n.Item == "" {
			continue
		}
		facts, err := s.Find(ctx, FindReq{Query: n.Item, Banks: banks, Agent: agent, K: 4, MinRel: rep.MinRel, NoLinks: true, NoReinforce: true})
		if err != nil {
			return nil, err
		}
		c := judgeCoverage(n, facts, time.Now())
		if c.Status != CoverSupported {
			rep.Gaps++
		}
		rep.Needs = append(rep.Needs, c)
	}
	return rep, nil
}

// judgeCoverage turns the best matches into a verdict. It is deliberately conservative: a need counts as
// supported only by a current, non-disputed, adequately trusted fact that satisfies any freshness limit.
func judgeCoverage(n Need, facts []Fact, now time.Time) Coverage {
	c := Coverage{Need: n.Item, Facts: facts}
	if len(facts) == 0 {
		c.Status = CoverNotFound
		c.Note = "nothing relevant in the searched banks (that does not mean it is false or unknown elsewhere)"
		return c
	}
	var good, weak, stale, disputed []Fact
	for _, f := range facts {
		old := n.MaxAgeDays > 0 && now.Sub(f.CreatedAt) > time.Duration(n.MaxAgeDays)*24*time.Hour
		switch {
		case f.Status == StatusContested:
			disputed = append(disputed, f)
		case f.Stale || old || (f.ExpiresAt != nil && !f.ExpiresAt.After(now)):
			stale = append(stale, f)
		case f.Confidence < 0.5 && f.Confirmation != ConfirmUser && f.Confirmation != ConfirmMulti:
			weak = append(weak, f)
		default:
			good = append(good, f)
		}
	}
	switch {
	case len(good) > 0 && len(disputed) == 0:
		c.Status = CoverSupported
		c.Note = fmt.Sprintf("#%d%s", good[0].ID, trustNote(good[0]))
	case len(good) > 0:
		c.Status = CoverDisputed
		c.Note = fmt.Sprintf("a supporting fact (#%d) exists, but a contradicting one (#%d) is live — resolve before relying on it", good[0].ID, disputed[0].ID)
	case len(disputed) > 0:
		c.Status = CoverDisputed
		c.Note = fmt.Sprintf("#%d is disputed: another live fact contradicts it", disputed[0].ID)
	case len(stale) > 0:
		c.Status = CoverStale
		why := "its evidence changed"
		if n.MaxAgeDays > 0 && now.Sub(stale[0].CreatedAt) > time.Duration(n.MaxAgeDays)*24*time.Hour {
			why = fmt.Sprintf("learned %s, older than the %d days you allow", stale[0].CreatedAt.Format("2006-01-02"), n.MaxAgeDays)
		} else if stale[0].ExpiresAt != nil {
			why = "it was meant to expire after " + stale[0].ExpiresAt.Format("2006-01-02")
		}
		c.Note = fmt.Sprintf("only #%d, which is out of date: %s — re-check it", stale[0].ID, why)
	default:
		c.Status = CoverWeak
		c.Note = fmt.Sprintf("only #%d, unverified (confidence %.0f%%) — confirm it before relying on it", weak[0].ID, weak[0].Confidence*100)
	}
	return c
}

func trustNote(f Fact) string {
	switch {
	case f.Confirmation == ConfirmUser:
		return ", confirmed by the user"
	case f.Confirmation == ConfirmMulti || len(f.Origins) >= 2:
		return ", confirmed by independent sources"
	}
	return fmt.Sprintf(", confidence %.0f%%", f.Confidence*100)
}

// Render formats the report for an agent.
func (r *CoverageReport) Render() string {
	var sb strings.Builder
	banks := append([]string(nil), r.Searched...)
	sort.Strings(banks)
	fmt.Fprintf(&sb, "Coverage — searched banks: %s (relevance ≥ %.2f, best 4 per item)\n", strings.Join(banks, ", "), r.MinRel)
	for i, c := range r.Needs {
		fmt.Fprintf(&sb, "%d. %q → %s: %s\n", i+1, c.Need, strings.ToUpper(strings.ReplaceAll(c.Status, "_", " ")), c.Note)
		for _, f := range c.Facts {
			if len(c.Facts) > 0 && c.Status != CoverNotFound {
				fmt.Fprintf(&sb, "   #%d (%s) %s%s\n", f.ID, f.Bank, f.Text, FactFlags(f))
			}
		}
	}
	if r.Gaps == 0 {
		sb.WriteString("No gaps: every need is supported by memory.\n")
	} else {
		fmt.Fprintf(&sb, "Gaps: %d of %d needs are not plainly supported — fetch, verify or ask for those; do not assume them.\n", r.Gaps, len(r.Needs))
	}
	return sb.String()
}
