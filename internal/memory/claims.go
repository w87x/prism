package memory

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// A fact is a claim with a lifecycle, adopted from the memo design (docs/architecture.md there):
//
//	proposed → active ⇄ contested → superseded | retracted | expired
//
// valid_to IS NULL still means "current". Retiring a fact sets valid_to and records why in status; "contested" is
// never stored — a fact is contested exactly while a live fact contradicts it (a contradicts-link), so it clears
// by itself the moment the conflict is resolved.
const (
	StatusProposed   = "proposed"
	StatusActive     = "active"
	StatusContested  = "contested"
	StatusSuperseded = "superseded"
	StatusRetracted  = "retracted"
	StatusExpired    = "expired"

	ConfirmNone  = "unconfirmed"
	ConfirmUser  = "user_confirmed"
	ConfirmMulti = "multi_source_confirmed"
)

// effectiveStatus is the status a reader should see: a retired row reports why it was retired (rows written
// before the status column existed, or by code that only sets valid_to, count as retracted), a current row
// reports contested while it has a live contradiction.
func effectiveStatus(stored string, validTo interface{}, supersededBy *int64, contested bool) string {
	retired := false
	switch v := validTo.(type) {
	case *time.Time:
		retired = v != nil
	case time.Time:
		retired = !v.IsZero()
	}
	if retired {
		switch {
		case supersededBy != nil:
			return StatusSuperseded
		case stored == StatusExpired:
			return StatusExpired
		}
		return StatusRetracted
	}
	if stored == StatusProposed {
		return StatusProposed
	}
	if contested {
		return StatusContested
	}
	return StatusActive
}

// ── audit trail ─────────────────────────────────────────────────────────────────────────────────────────────

// AuditEntry is one thing that happened to a fact.
type AuditEntry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	FactID int64     `json:"fact_id"`
	Detail string    `json:"detail,omitempty"`
}

func (s *Service) audit(ctx context.Context, actor, action string, factID int64, detail string) {
	if actor == "" {
		actor = "system"
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO memory_audit(actor,action,fact_id,detail) VALUES($1,$2,$3,$4)`, actor, action, factID, brief(detail, 400))
}

func brief(t string, n int) string {
	t = strings.Join(strings.Fields(t), " ")
	if r := []rune(t); len(r) > n {
		return string(r[:n]) + "…"
	}
	return t
}

// Audit lists what happened to a fact, newest first.
func (s *Service) Audit(ctx context.Context, factID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT id,at,actor,action,fact_id,detail FROM memory_audit WHERE fact_id=$1 ORDER BY at DESC, id DESC LIMIT $2`, factID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.FactID, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── lifecycle operations ────────────────────────────────────────────────────────────────────────────────────

// Retract withdraws a fact that turned out not to be true or no longer matters, with a reason. The row stays as
// history (valid_to set, status retracted) — it is never silently overwritten or deleted.
func (s *Service) Retract(ctx context.Context, actor string, id int64, reason string) error {
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), status='retracted' WHERE id=$1 AND valid_to IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is already retired (or does not exist)", id)
	}
	s.audit(ctx, actor, "retract", id, reason)
	s.changed()
	return nil
}

// ConfirmByUser records that the user vouches for a fact: it is trusted (confidence ≥ 0.95) and forgets four
// times slower. It is not an irreversible override — Unconfirm takes it back, and a correction still supersedes.
func (s *Service) ConfirmByUser(ctx context.Context, actor string, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET confirmation='user_confirmed', confidence=GREATEST(confidence,0.95), last_used=now()
		WHERE id=$1 AND valid_to IS NULL AND status<>'proposed'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is not a current fact", id)
	}
	s.audit(ctx, actor, "confirm", id, "")
	s.changed()
	return nil
}

// Unconfirm withdraws a user confirmation (the fact keeps its confidence; it just no longer forgets slowly).
func (s *Service) Unconfirm(ctx context.Context, actor string, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET confirmation='unconfirmed' WHERE id=$1 AND confirmation='user_confirmed'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is not user-confirmed", id)
	}
	s.audit(ctx, actor, "unconfirm", id, "")
	s.changed()
	return nil
}

// effRankConfirmed is effRank with the slower forgetting of a user-confirmed fact (half-life ×4).
func effRankConfirmed(rank float64, last *time.Time, created time.Time, pinned, userConfirmed bool) float64 {
	if !userConfirmed || pinned {
		return effRank(rank, last, created, pinned)
	}
	ref := created
	if last != nil {
		ref = *last
	}
	days := time.Since(ref).Hours() / 24
	return math.Max(0.3, rank*math.Pow(0.5, days/(120*4)))
}

// contestedWeight discounts a fact a live fact contradicts: still worth showing, much less worth believing.
const contestedWeight = 0.45

// ── proposals: what a probationary agent wants in a shared bank ─────────────────────────────────────────────

// Proposals lists facts waiting for a curator's decision, oldest first.
func (s *Service) Proposals(ctx context.Context, limit int) ([]Fact, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.status='proposed' AND f.valid_to IS NULL ORDER BY f.created_at, f.id LIMIT $1`, limit)
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

// Promote accepts a proposal: the fact becomes active and is retrieved like any other.
func (s *Service) Promote(ctx context.Context, actor string, id int64, note string) error {
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET status='active', last_used=now() WHERE id=$1 AND status='proposed' AND valid_to IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is not a pending proposal", id)
	}
	s.audit(ctx, actor, "promote", id, note)
	if sup, con, err := s.applyCardinality(ctx, s.db, id); err == nil { // a promoted fact is now current: single-valued rules apply
		s.settleCardinality(ctx, s.db, id, sup, con)
	}
	s.changed()
	return nil
}

// RejectProposal declines a proposal; the fact is kept as retracted history, with the reason in the audit trail.
func (s *Service) RejectProposal(ctx context.Context, actor string, id int64, note string) error {
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), status='retracted' WHERE id=$1 AND status='proposed' AND valid_to IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is not a pending proposal", id)
	}
	s.audit(ctx, actor, "reject", id, note)
	s.changed()
	return nil
}

// ── membership: one fact, several banks ─────────────────────────────────────────────────────────────────────

// ShareFact makes a fact visible in another bank as well, without copying it: the claim, its evidence and its
// lifecycle stay single. The bank is created if it does not exist yet, like memory_store does.
func (s *Service) ShareFact(ctx context.Context, actor string, id int64, bankSpec string) error {
	f, err := s.GetFact(ctx, id)
	if err != nil {
		return fmt.Errorf("fact %d not found", id)
	}
	b, err := s.BankBySpec(ctx, bankSpec, actor, true)
	if err != nil {
		return err
	}
	if b.ID == f.BankID {
		return fmt.Errorf("fact #%d already lives in %s", id, b.Label())
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO memory_fact_banks(fact_id,bank_id,added_by) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, b.ID, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is already visible in %s", id, b.Label())
	}
	s.audit(ctx, actor, "share", id, "also in "+b.Label())
	s.changed()
	return nil
}

// UnshareFact removes the fact from one of its extra banks (its home bank cannot be detached — move it instead).
func (s *Service) UnshareFact(ctx context.Context, actor string, id int64, bankSpec string) error {
	b, err := s.BankBySpec(ctx, bankSpec, actor, false)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM memory_fact_banks WHERE fact_id=$1 AND bank_id=$2`, id, b.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is not shared into %s (its home bank cannot be detached; move it instead)", id, b.Label())
	}
	s.audit(ctx, actor, "unshare", id, "no longer in "+b.Label())
	s.changed()
	return nil
}
