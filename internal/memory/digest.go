package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// A digest sums up what memory learned, revised and doubts over a period, in one readable piece: the place to
// see what analysis, ingestion, harvesting and reflection actually did, and what is waiting for a human decision.
type DigestLine struct {
	Text string `json:"text"`
	Kind string `json:"kind,omitempty"`
	Bank string `json:"bank,omitempty"`
}

type Digest struct {
	Since          time.Time    `json:"since"`
	NewFacts       int          `json:"new_facts"`
	ByBank         []DigestLine `json:"by_bank"` // text = "N facts", bank = label
	Highlights     []DigestLine `json:"highlights"`
	Insights       []DigestLine `json:"insights"`
	Conclusions    []DigestLine `json:"conclusions"`
	Retired        int          `json:"retired"`
	Documents      []DigestLine `json:"documents"`
	Bookmarks      int          `json:"bookmarks"`
	Models         []string     `json:"models"`
	Contradictions int          `json:"contradictions"`
	Unverified     int          `json:"unverified"`
	Stale          int          `json:"stale"`
	Questions      []DigestLine `json:"questions"`
}

// Empty reports whether nothing worth a briefing happened.
func (d *Digest) Empty() bool {
	return d.NewFacts == 0 && len(d.Insights) == 0 && len(d.Conclusions) == 0 && len(d.Documents) == 0 && d.Bookmarks == 0 && len(d.Models) == 0 && d.Retired == 0
}

func (s *Service) Digest(ctx context.Context, since time.Time) (*Digest, error) {
	d := &Digest{Since: since}
	q := func(sql string, args ...any) ([]DigestLine, error) {
		rows, err := s.db.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []DigestLine
		for rows.Next() {
			var l DigestLine
			if err := rows.Scan(&l.Text, &l.Kind, &l.Bank); err != nil {
				return nil, err
			}
			out = append(out, l)
		}
		return out, rows.Err()
	}
	label := `b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END`
	var err error
	if d.ByBank, err = q(`SELECT count(*)::text||' facts', '', `+label+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.kind='fact' AND f.created_at>$1 AND f.source NOT LIKE 'digest%' GROUP BY b.id ORDER BY count(*) DESC LIMIT 8`, since); err != nil {
		return nil, err
	}
	for _, l := range d.ByBank {
		var n int
		fmt.Sscan(l.Text, &n)
		d.NewFacts += n
	}
	if d.Highlights, err = q(`SELECT f.text, '', `+label+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.kind='fact' AND f.valid_to IS NULL AND f.confidence>=0.5 AND f.created_at>$1 ORDER BY f.rank DESC, f.id DESC LIMIT 5`, since); err != nil {
		return nil, err
	}
	if d.Insights, err = q(`SELECT f.text, COALESCE((SELECT t FROM unnest(f.tags) t WHERE t IN ('pattern','deduction','hypothesis','trend','preference','risk','question') LIMIT 1),''), `+label+`
		FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.kind='conclusion' AND f.source='analysis' AND f.valid_to IS NULL AND f.created_at>$1 ORDER BY f.confidence DESC LIMIT 6`, since); err != nil {
		return nil, err
	}
	if d.Conclusions, err = q(`SELECT f.text, '', `+label+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.kind='conclusion' AND f.source<>'analysis' AND f.valid_to IS NULL AND f.created_at>$1 ORDER BY f.confidence DESC LIMIT 4`, since); err != nil {
		return nil, err
	}
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_facts WHERE kind='fact' AND valid_to>$1`, since).Scan(&d.Retired)
	if d.Documents, err = q(`SELECT name||' — '||facts||' facts', kind, title FROM memory_ingests WHERE status='done' AND finished_at>$1 ORDER BY id DESC LIMIT 5`, since); err != nil {
		return nil, err
	}
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM bookmarks WHERE created_at>$1`, since).Scan(&d.Bookmarks)
	if ms, err := q(`SELECT name, '', '' FROM memory_models WHERE refreshed_at>$1 ORDER BY name`, since); err == nil {
		for _, m := range ms {
			d.Models = append(d.Models, m.Text)
		}
	}
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_links l JOIN memory_facts a ON a.id=l.a JOIN memory_facts b ON b.id=l.b
		WHERE l.kind=$1 AND a.valid_to IS NULL AND b.valid_to IS NULL`, LinkContradicts).Scan(&d.Contradictions)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_facts WHERE kind='fact' AND valid_to IS NULL AND confidence<0.5`).Scan(&d.Unverified)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_facts f WHERE f.kind='conclusion' AND f.valid_to IS NULL AND EXISTS(
		SELECT 1 FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=f.id THEN e.b ELSE e.a END
		WHERE (e.a=f.id OR e.b=f.id) AND e.kind='evidence' AND x.kind='fact' AND x.valid_to IS NOT NULL)`).Scan(&d.Stale)
	if d.Questions, err = q(`SELECT f.text, 'question', ` + label + ` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.kind='conclusion' AND f.source='analysis' AND f.valid_to IS NULL AND f.tags && ARRAY['question'] ORDER BY f.id DESC LIMIT 4`); err != nil {
		return nil, err
	}
	return d, nil
}

// Markdown renders the digest as a briefing body.
func (d *Digest) Markdown() string {
	var sb strings.Builder
	days := int(time.Since(d.Since).Hours()/24 + 0.5)
	if days < 1 {
		days = 1
	}
	fmt.Fprintf(&sb, "What memory did in the last %d day(s).\n", days)
	if d.NewFacts > 0 {
		var parts []string
		for _, l := range d.ByBank {
			parts = append(parts, l.Bank+" "+l.Text)
		}
		fmt.Fprintf(&sb, "\n**Learned:** %d new facts (%s).\n", d.NewFacts, strings.Join(parts, ", "))
		for _, l := range d.Highlights {
			fmt.Fprintf(&sb, "- %s\n", l.Text)
		}
	}
	if len(d.Insights) > 0 {
		sb.WriteString("\n**Worked out from what it knows:**\n")
		for _, l := range d.Insights {
			tag := ""
			if l.Kind != "" {
				tag = "[" + l.Kind + "] "
			}
			fmt.Fprintf(&sb, "- %s%s\n", tag, l.Text)
		}
	}
	if len(d.Conclusions) > 0 {
		sb.WriteString("\n**Conclusions drawn:**\n")
		for _, l := range d.Conclusions {
			fmt.Fprintf(&sb, "- %s\n", l.Text)
		}
	}
	if len(d.Documents) > 0 {
		sb.WriteString("\n**Learned from documents:**\n")
		for _, l := range d.Documents {
			fmt.Fprintf(&sb, "- %s (→ %s)\n", l.Text, l.Bank)
		}
	}
	if d.Bookmarks > 0 {
		fmt.Fprintf(&sb, "\n**Bookmarked:** %d pages the agents visited.\n", d.Bookmarks)
	}
	if len(d.Models) > 0 {
		fmt.Fprintf(&sb, "\n**Mental models refreshed:** %s.\n", strings.Join(d.Models, ", "))
	}
	if d.Retired > 0 {
		fmt.Fprintf(&sb, "\n%d facts were retired or corrected.\n", d.Retired)
	}
	var wait []string
	if d.Contradictions > 0 {
		wait = append(wait, fmt.Sprintf("%d contradicting facts to decide", d.Contradictions))
	}
	if d.Unverified > 0 {
		wait = append(wait, fmt.Sprintf("%d unverified web facts", d.Unverified))
	}
	if d.Stale > 0 {
		wait = append(wait, fmt.Sprintf("%d conclusions whose evidence changed", d.Stale))
	}
	if len(wait) > 0 {
		fmt.Fprintf(&sb, "\n**Waiting for you** (Memory → Review): %s.\n", strings.Join(wait, "; "))
	}
	if len(d.Questions) > 0 {
		sb.WriteString("\n**Memory would like to know:**\n")
		for _, l := range d.Questions {
			fmt.Fprintf(&sb, "- %s\n", l.Text)
		}
		sb.WriteString("Answer them in Memory → Review, or reply to this briefing.\n")
	}
	return strings.TrimSpace(sb.String())
}
