package memory

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// FactsAt is what memory believed at a moment: the facts and conclusions that existed and had not yet been retired
// or replaced. A fact retired since then has ValidTo set, so a caller can show what changed.
func (s *Service) FactsAt(ctx context.Context, at time.Time, bankID int64, q string, limit, offset int) ([]Fact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	sql := `SELECT ` + factCols + ` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE ($1=0 OR f.bank_id=$1) AND f.valid_from<=$2 AND (f.valid_to IS NULL OR f.valid_to>$2)`
	args := []any{bankID, at}
	if q = strings.TrimSpace(q); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		sql += ` AND lower(f.text) LIKE $3`
	}
	args = append(args, limit, offset)
	sql += ` ORDER BY f.rank DESC, f.id DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fact{}
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

type TimelineItem struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"` // added | corrected | retired
	Text string `json:"text"`
	Bank string `json:"bank"`
	Type string `json:"type"` // fact | conclusion
}

type TimelineDay struct {
	Day       string         `json:"day"`
	Added     int            `json:"added"`
	Corrected int            `json:"corrected"` // wording replaced by a newer one
	Retired   int            `json:"retired"`   // dropped with no replacement
	Samples   []TimelineItem `json:"samples"`
}

// Timeline lists, day by day (newest first), what was added, corrected and retired since a date.
func (s *Service) Timeline(ctx context.Context, since time.Time, bankID int64) ([]TimelineDay, error) {
	rows, err := s.db.Query(ctx, `SELECT day, kind, id, text, bank, type FROM (
			SELECT to_char(f.created_at,'YYYY-MM-DD') AS day, 'added' AS kind, f.id, f.text, f.kind AS type, f.created_at AS at,
				b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END AS bank
			FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE f.created_at>=$1 AND ($2=0 OR f.bank_id=$2)
			UNION ALL
			SELECT to_char(f.valid_to,'YYYY-MM-DD'), CASE WHEN f.superseded_by IS NOT NULL THEN 'corrected' ELSE 'retired' END, f.id, f.text, f.kind, f.valid_to,
				b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END
			FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE f.valid_to>=$1 AND ($2=0 OR f.bank_id=$2)
		) e ORDER BY day DESC, at DESC LIMIT 4000`, since, bankID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineDay
	idx := map[string]int{}
	for rows.Next() {
		var day, kind, text, bank, typ string
		var id int64
		if err := rows.Scan(&day, &kind, &id, &text, &bank, &typ); err != nil {
			return nil, err
		}
		i, ok := idx[day]
		if !ok {
			out = append(out, TimelineDay{Day: day, Samples: []TimelineItem{}})
			i = len(out) - 1
			idx[day] = i
		}
		switch kind {
		case "added":
			out[i].Added++
		case "corrected":
			out[i].Corrected++
		default:
			out[i].Retired++
		}
		if len(out[i].Samples) < 8 {
			out[i].Samples = append(out[i].Samples, TimelineItem{ID: id, Kind: kind, Text: text, Bank: bank, Type: typ})
		}
	}
	if out == nil {
		out = []TimelineDay{}
	}
	return out, rows.Err()
}
