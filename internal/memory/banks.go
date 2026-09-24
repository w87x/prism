package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"prism/internal/llm"
)

// Project and domain banks come and go with the user's work, so they can be combined and divided:
// merge two banks that turned out to be one topic, split one that grew into several. Facts keep their
// ids, links, history and ranks when they move. The user bank and agent profile banks are fixed.

func mergeable(kind string) bool { return kind == KindProject || kind == KindDomain }

func (s *Service) bankByID(ctx context.Context, id int64) (*Bank, error) {
	var b Bank
	err := s.db.QueryRow(ctx, `SELECT id,kind,name,owner,description,status,created_at FROM memory_banks WHERE id=$1`, id).
		Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("memory bank #%d does not exist", id)
	}
	return &b, nil
}

type MergeResult struct {
	Bank    Bank `json:"bank"`
	Moved   int  `json:"moved"`
	Dropped int  `json:"dropped"` // exact duplicates collapsed into the fact already present
}

// MergeBanks moves every fact of the source banks into one target and removes the emptied sources.
// into is an existing bank (of the same kind as the sources); when it is 0 a new bank called name is made.
func (s *Service) MergeBanks(ctx context.Context, sources []int64, into int64, name string) (*MergeResult, error) {
	if len(sources) == 0 {
		return nil, errors.New("pick at least one bank to merge")
	}
	seen := map[int64]bool{}
	var src []*Bank
	kind := ""
	for _, id := range sources {
		if seen[id] || id == into {
			continue
		}
		seen[id] = true
		b, err := s.bankByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !mergeable(b.Kind) {
			return nil, fmt.Errorf("%s cannot be merged: only project and domain banks can", b.Label())
		}
		if kind != "" && b.Kind != kind {
			return nil, errors.New("banks of different kinds cannot be merged (project with project, domain with domain)")
		}
		kind = b.Kind
		src = append(src, b)
	}
	if len(src) == 0 {
		return nil, errors.New("nothing to merge")
	}
	var target *Bank
	var err error
	if into != 0 {
		if target, err = s.bankByID(ctx, into); err != nil {
			return nil, err
		}
		if target.Kind != kind {
			return nil, fmt.Errorf("%s is a %s bank; the banks to merge are %s banks", target.Label(), target.Kind, kind)
		}
	} else {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("give the merged bank a name")
		}
		if target, err = s.EnsureBank(ctx, kind, name, "", ""); err != nil {
			return nil, err
		}
	}
	ids := make([]int64, len(src))
	var names []string
	for i, b := range src {
		ids[i] = b.ID
		names = append(names, b.Label())
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	op := mergeOp{IntoID: target.ID, IntoCreated: into == 0, IntoDescription: target.Description, Origin: map[int64]int64{}}
	for _, b := range src {
		var ra *time.Time
		_ = tx.QueryRow(ctx, `SELECT reflected_at FROM memory_banks WHERE id=$1`, b.ID).Scan(&ra)
		op.Sources = append(op.Sources, opBank{*b, ra})
	}
	orows, err := tx.Query(ctx, `SELECT id, bank_id FROM memory_facts WHERE bank_id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var fid, bid int64
		if orows.Scan(&fid, &bid) == nil {
			op.Origin[fid] = bid
		}
	}
	orows.Close()
	tag, err := tx.Exec(ctx, `UPDATE memory_facts SET bank_id=$1 WHERE bank_id=ANY($2)`, target.ID, ids)
	if err != nil {
		return nil, err
	}
	// two banks often learned the same thing: keep the best-ranked of identical active facts (kept in the undo log)
	drows, err := tx.Query(ctx, `DELETE FROM memory_facts d USING memory_facts k
		WHERE d.bank_id=$1 AND k.bank_id=$1 AND d.id<>k.id AND d.valid_to IS NULL AND k.valid_to IS NULL AND d.kind=k.kind
		  AND lower(btrim(d.text))=lower(btrim(k.text)) AND (k.rank>d.rank OR (k.rank=d.rank AND k.id<d.id))
		RETURNING d.id, to_jsonb(d) - 'embedding' - 'vec' - 'tsv'`, target.ID)
	if err != nil {
		return nil, err
	}
	dropped := 0
	for drows.Next() {
		var fid int64
		var row json.RawMessage
		if drows.Scan(&fid, &row) == nil {
			op.Dropped = append(op.Dropped, droppedFact{Bank: op.Origin[fid], Row: row})
			dropped++
		}
	}
	drows.Close()
	desc := strings.TrimSpace(target.Description)
	note := "merged from " + strings.Join(names, ", ")
	if desc == "" {
		desc = note
	} else if !strings.Contains(desc, note) {
		desc += " · " + note
	}
	if _, err := tx.Exec(ctx, `UPDATE memory_banks SET description=$2, reflected_at=NULL WHERE id=$1`, target.ID, desc); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM memory_banks WHERE id=ANY($1) AND kind IN ('project','domain')`, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.logOp(ctx, "merge", fmt.Sprintf("merged %s into %s", strings.Join(names, ", "), target.Label()), op)
	s.changed()
	nb, _ := s.bankByID(ctx, target.ID)
	return &MergeResult{Bank: *nb, Moved: int(tag.RowsAffected()), Dropped: dropped}, nil
}

type SplitResult struct {
	Bank  Bank `json:"bank"`
	Moved int  `json:"moved"`
}

// SplitBank moves the chosen facts of a bank into a new (or existing) bank of the same kind. A fact's
// supersede chain (its retired predecessors and successors) moves with it so histories are not torn apart.
func (s *Service) SplitBank(ctx context.Context, id int64, name, description string, factIDs []int64) (*SplitResult, error) {
	src, err := s.bankByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !mergeable(src.Kind) {
		return nil, fmt.Errorf("%s cannot be split: only project and domain banks can", src.Label())
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("give the new bank a name")
	}
	if name == src.Name {
		return nil, errors.New("the new bank needs a different name")
	}
	if len(factIDs) == 0 {
		return nil, errors.New("pick the facts to move")
	}
	move, err := s.chainClosure(ctx, id, factIDs)
	if err != nil {
		return nil, err
	}
	if len(move) == 0 {
		return nil, errors.New("none of those facts belong to this bank")
	}
	var existed bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_banks WHERE kind=$1 AND name=$2 AND owner='')`, src.Kind, name).Scan(&existed)
	dst, err := s.EnsureBank(ctx, src.Kind, name, "", strings.TrimSpace(description))
	if err != nil {
		return nil, err
	}
	var moved []int64
	mrows, err := s.db.Query(ctx, `UPDATE memory_facts SET bank_id=$1 WHERE bank_id=$2 AND id=ANY($3) RETURNING id`, dst.ID, id, move)
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var fid int64
		if mrows.Scan(&fid) == nil {
			moved = append(moved, fid)
		}
	}
	mrows.Close()
	s.logOp(ctx, "split", fmt.Sprintf("split %d facts of %s into %s", len(moved), src.Label(), dst.Label()), splitOp{From: id, To: dst.ID, ToCreated: !existed, Moved: moved})
	_, _ = s.db.Exec(ctx, `UPDATE memory_banks SET reflected_at=NULL WHERE id=ANY($1)`, []int64{id, dst.ID})
	s.changed()
	nb, _ := s.bankByID(ctx, dst.ID)
	return &SplitResult{Bank: *nb, Moved: len(moved)}, nil
}

// chainClosure extends ids with the facts they supersede or were superseded by, inside the bank.
func (s *Service) chainClosure(ctx context.Context, bankID int64, ids []int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT id, supersedes, superseded_by FROM memory_facts WHERE bank_id=$1`, bankID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	adj := map[int64][]int64{}
	in := map[int64]bool{}
	for rows.Next() {
		var id int64
		var a, b *int64
		if rows.Scan(&id, &a, &b) != nil {
			continue
		}
		in[id] = true
		for _, o := range []*int64{a, b} {
			if o != nil {
				adj[id] = append(adj[id], *o)
				adj[*o] = append(adj[*o], id)
			}
		}
	}
	got := map[int64]bool{}
	var queue []int64
	for _, id := range ids {
		if in[id] && !got[id] {
			got[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]
		for _, y := range adj[x] {
			if in[y] && !got[y] {
				got[y] = true
				queue = append(queue, y)
			}
		}
	}
	out := make([]int64, 0, len(got))
	for id := range got {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// SplitGroup is a proposed sub-topic of a bank.
type SplitGroup struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	FactIDs     []int64 `json:"fact_ids"`
}

const splitPrompt = `You organise a memory bank that has grown too broad. Below are its facts (id: text). Group them into 2-4 coherent sub-topics that would each make a sensible bank of their own, with a short name and one-line description. Facts that fit no group stay in the original bank: leave them out. Every group needs at least 3 facts, and a fact belongs to at most one group. If the bank is really one topic, return an empty list.
Answer JSON only: {"groups":[{"name":"...","description":"...","fact_ids":[1,2,3]}]}`

// SuggestSplit asks the fast model to propose sub-topics of a bank; the user or agent reviews them
// before anything moves.
func (s *Service) SuggestSplit(ctx context.Context, id int64) ([]SplitGroup, error) {
	b, err := s.bankByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !mergeable(b.Kind) {
		return nil, fmt.Errorf("%s cannot be split: only project and domain banks can", b.Label())
	}
	if s.llm.RoleRef(ctx, "fast") == "" {
		return nil, errors.New("no fast model configured")
	}
	fs, err := s.Facts(ctx, id, "", false, 150, 0)
	if err != nil {
		return nil, err
	}
	if len(fs) < 6 {
		return nil, nil
	}
	valid := map[int64]bool{}
	var sb strings.Builder
	for _, f := range fs {
		if f.Kind == ConclusionKind {
			continue // conclusions follow their evidence; grouping the facts is enough
		}
		valid[f.ID] = true
		t := f.Text
		if len(t) > 240 {
			t = t[:240] + "…"
		}
		fmt.Fprintf(&sb, "%d: %s\n", f.ID, t)
	}
	out, err := s.llm.Complete(ctx, "role:fast", splitPrompt, "Bank: "+b.Label()+"\n\n"+sb.String(), true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Groups []SplitGroup `json:"groups"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return nil, fmt.Errorf("split suggestion: unparsable model output: %w", err)
	}
	used := map[int64]bool{}
	var groups []SplitGroup
	for _, g := range parsed.Groups {
		g.Name = strings.TrimSpace(g.Name)
		var ids []int64
		for _, id := range g.FactIDs {
			if valid[id] && !used[id] {
				used[id] = true
				ids = append(ids, id)
			}
		}
		if g.Name == "" || g.Name == b.Name || len(ids) < 3 {
			for _, id := range ids {
				delete(used, id)
			}
			continue
		}
		g.FactIDs = ids
		groups = append(groups, g)
		if len(groups) == 4 {
			break
		}
	}
	return groups, nil
}
