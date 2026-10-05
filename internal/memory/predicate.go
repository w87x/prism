package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ── predicate rules (memo: predicate schemas with cardinality) ──────────────────────────────────────────────
//
// A structured fact (subject · predicate · object) says one thing about its subject. For a single-valued
// predicate ("lives in") two different objects for the same subject cannot both be current, so the newer one
// replaces the older — unless the older is something the user vouched for or the newer is a weak report, in which
// case the two are linked as contradicting and show up as disputed instead of one silently winning.

type Predicate struct {
	Predicate   string   `json:"predicate"`
	Cardinality string   `json:"cardinality"` // one | many
	Aliases     []string `json:"aliases"`
	Description string   `json:"description,omitempty"`
}

func normPredicate(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Predicates lists the rules.
func (s *Service) Predicates(ctx context.Context) ([]Predicate, error) {
	rows, err := s.db.Query(ctx, `SELECT predicate,cardinality,aliases,description FROM memory_predicates ORDER BY predicate`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Predicate{}
	for rows.Next() {
		var p Predicate
		if err := rows.Scan(&p.Predicate, &p.Cardinality, &p.Aliases, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPredicate creates or replaces a rule. An alias may belong to one predicate only.
func (s *Service) SetPredicate(ctx context.Context, p Predicate) error {
	p.Predicate = normPredicate(p.Predicate)
	if p.Predicate == "" {
		return errors.New("a predicate needs a name")
	}
	if p.Cardinality != "one" && p.Cardinality != "many" {
		return errors.New("cardinality must be one or many")
	}
	seen := map[string]bool{}
	var al []string
	for _, a := range p.Aliases {
		if a = normPredicate(a); a != "" && a != p.Predicate && !seen[a] {
			seen[a] = true
			al = append(al, a)
		}
	}
	if al == nil {
		al = []string{}
	}
	var clash string
	if err := s.db.QueryRow(ctx, `SELECT predicate FROM memory_predicates WHERE predicate<>$1 AND (predicate=ANY($2) OR aliases && $2) LIMIT 1`, p.Predicate, al).Scan(&clash); err == nil {
		return fmt.Errorf("an alias is already used by %q", clash)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO memory_predicates(predicate,cardinality,aliases,description) VALUES($1,$2,$3,$4)
		ON CONFLICT (predicate) DO UPDATE SET cardinality=EXCLUDED.cardinality, aliases=EXCLUDED.aliases, description=EXCLUDED.description`,
		p.Predicate, p.Cardinality, al, strings.TrimSpace(p.Description))
	return err
}

func (s *Service) DeletePredicate(ctx context.Context, predicate string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM memory_predicates WHERE predicate=$1`, normPredicate(predicate))
	return err
}

// queryer is what both a pool and a transaction offer.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// canonicalPredicate maps any phrasing to its rule's canonical name and says whether it is single-valued.
func canonicalPredicate(ctx context.Context, q queryer, pred string) (canon string, one bool) {
	k := normPredicate(pred)
	var card string
	if err := q.QueryRow(ctx, `SELECT predicate,cardinality FROM memory_predicates WHERE predicate=$1 OR $1=ANY(aliases)`, k).Scan(&canon, &card); err != nil {
		return k, false
	}
	return canon, card == "one"
}

// applyCardinality enforces the rule for the structured fact `id` (a no-op for free text, many-valued
// predicates and proposals). It returns the facts it superseded and the ones it could only mark as contradicting.
func (s *Service) applyCardinality(ctx context.Context, q queryer, id int64) (superseded, contested []int64, err error) {
	var bank int64
	var sub, pred, obj, status, confirm string
	var conf float32
	var qual map[string]string
	err = q.QueryRow(ctx, `SELECT bank_id,subject,predicate,object,qualifiers,status,confirmation,confidence FROM memory_facts WHERE id=$1 AND valid_to IS NULL`, id).
		Scan(&bank, &sub, &pred, &obj, &qual, &status, &confirm, &conf)
	if err != nil || sub == "" || pred == "" || obj == "" || status == StatusProposed {
		return nil, nil, nil
	}
	canon, one := canonicalPredicate(ctx, q, pred)
	if !one {
		return nil, nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id,predicate,object,qualifiers,confirmation,confidence FROM memory_facts
		WHERE bank_id=$1 AND id<>$2 AND valid_to IS NULL AND status<>'proposed' AND lower(btrim(subject))=lower(btrim($3)) AND predicate<>'' AND object<>''`, bank, id, sub)
	if err != nil {
		return nil, nil, err
	}
	type other struct {
		id      int64
		pred    string
		obj     string
		qual    map[string]string
		confirm string
		conf    float32
	}
	var olds []other
	for rows.Next() {
		var o other
		if err := rows.Scan(&o.id, &o.pred, &o.obj, &o.qual, &o.confirm, &o.conf); err != nil {
			rows.Close()
			return nil, nil, err
		}
		olds = append(olds, o)
	}
	rows.Close()
	for _, o := range olds {
		if c, _ := canonicalPredicate(ctx, q, o.pred); c != canon {
			continue
		}
		if normPredicate(o.obj) == normPredicate(obj) || qualKey(o.qual) != qualKey(qual) {
			continue // the same value, or a different dimension (since=2020 vs since=2024): not in conflict
		}
		// a vouched-for fact is never replaced by the rule, and neither is a trusted one by a weak report
		if o.confirm == ConfirmUser || (conf < 0.5 && o.conf >= 0.5) {
			contested = append(contested, o.id)
			continue
		}
		if _, err := q.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2, status='superseded' WHERE id=$1 AND valid_to IS NULL`, o.id, id); err != nil {
			return superseded, contested, err
		}
		superseded = append(superseded, o.id)
	}
	return superseded, contested, nil
}

// settleCardinality is applyCardinality plus its bookkeeping: the audit trail and the contradicts links.
func (s *Service) settleCardinality(ctx context.Context, q queryer, id int64, superseded, contested []int64) {
	for _, old := range superseded {
		s.audit(ctx, "system", "supersede", old, fmt.Sprintf("replaced by #%d: single-valued predicate", id))
	}
	for _, old := range contested {
		_ = s.Link(ctx, id, old, LinkContradicts, "single-valued predicate: two values for one subject", "rule", 0.8)
	}
}
