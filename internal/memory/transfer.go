package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Export and import move memory between installs or keep a readable backup: banks, facts (with history, ranks and
// provenance) and links, as one JSON document. Embeddings are not included; they are rebuilt on import.

const DumpVersion = 1

type DumpBank struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type DumpFact struct {
	ID           int64      `json:"id"`
	Bank         int64      `json:"bank"`
	Text         string     `json:"text"`
	Tags         []string   `json:"tags"`
	Kind         string     `json:"kind"`
	Rank         float64    `json:"rank"`
	Hits         int        `json:"hits"`
	Confidence   float64    `json:"confidence"`
	Source       string     `json:"source"`
	Origins      []string   `json:"origins,omitempty"`
	Supersedes   *int64     `json:"supersedes,omitempty"`
	SupersededBy *int64     `json:"superseded_by,omitempty"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidTo      *time.Time `json:"valid_to,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type DumpLink struct {
	A      int64   `json:"a"`
	B      int64   `json:"b"`
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
	Note   string  `json:"note,omitempty"`
	Source string  `json:"source,omitempty"`
}

type Dump struct {
	Version    int        `json:"version"`
	App        string     `json:"app"`
	ExportedAt time.Time  `json:"exported_at"`
	Banks      []DumpBank `json:"banks"`
	Facts      []DumpFact `json:"facts"`
	Links      []DumpLink `json:"links"`
}

// Export dumps the given banks (all of them when none are named), history included.
func (s *Service) Export(ctx context.Context, bankIDs []int64) (*Dump, error) {
	d := &Dump{Version: DumpVersion, App: "prism", ExportedAt: time.Now().UTC(), Banks: []DumpBank{}, Facts: []DumpFact{}, Links: []DumpLink{}}
	brows, err := s.db.Query(ctx, `SELECT id,kind,name,owner,description,status FROM memory_banks WHERE $1::bigint[] IS NULL OR cardinality($1::bigint[])=0 OR id=ANY($1) ORDER BY id`, bankIDs)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for brows.Next() {
		var b DumpBank
		if brows.Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status) == nil {
			d.Banks = append(d.Banks, b)
			ids = append(ids, b.ID)
		}
	}
	brows.Close()
	frows, err := s.db.Query(ctx, `SELECT id,bank_id,text,tags,kind,rank,hits,confidence,source,origins,supersedes,superseded_by,valid_from,valid_to,created_at
		FROM memory_facts WHERE bank_id=ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	have := map[int64]bool{}
	for frows.Next() {
		var f DumpFact
		var rk, cf float32
		if frows.Scan(&f.ID, &f.Bank, &f.Text, &f.Tags, &f.Kind, &rk, &f.Hits, &cf, &f.Source, &f.Origins, &f.Supersedes, &f.SupersededBy, &f.ValidFrom, &f.ValidTo, &f.CreatedAt) == nil {
			f.Rank, f.Confidence = float64(rk), float64(cf)
			d.Facts = append(d.Facts, f)
			have[f.ID] = true
		}
	}
	frows.Close()
	lrows, err := s.db.Query(ctx, `SELECT a,b,kind,weight,note,source FROM memory_links WHERE a IN (SELECT id FROM memory_facts WHERE bank_id=ANY($1)) AND b IN (SELECT id FROM memory_facts WHERE bank_id=ANY($1))`, ids)
	if err != nil {
		return nil, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var l DumpLink
		var w float32
		if lrows.Scan(&l.A, &l.B, &l.Kind, &w, &l.Note, &l.Source) == nil && have[l.A] && have[l.B] {
			l.Weight = float64(w)
			d.Links = append(d.Links, l)
		}
	}
	return d, lrows.Err()
}

type ImportResult struct {
	Banks   int `json:"banks"`
	Facts   int `json:"facts"`
	Skipped int `json:"skipped"` // already present
	Links   int `json:"links"`
}

const maxImportFacts = 50000

// Import merges a dump into this memory: banks are matched by kind and name, facts already present (same bank, same
// text, both active) are skipped, ids are remapped, and supersede chains and links are carried over.
func (s *Service) Import(ctx context.Context, d *Dump) (*ImportResult, error) {
	if d == nil || d.Version != DumpVersion {
		return nil, fmt.Errorf("not a PRISM memory export (version %d expected)", DumpVersion)
	}
	if len(d.Facts) > maxImportFacts {
		return nil, fmt.Errorf("too many facts (limit %d)", maxImportFacts)
	}
	res := &ImportResult{}
	bankMap := map[int64]int64{}
	for _, b := range d.Banks {
		switch b.Kind {
		case KindUser, KindProfile, KindProject, KindDomain:
		default:
			return nil, fmt.Errorf("unknown bank kind %q", b.Kind)
		}
		name, owner := strings.TrimSpace(b.Name), b.Owner
		if b.Kind == KindUser {
			name, owner = "user", ""
		}
		if name == "" {
			return nil, errors.New("a bank without a name")
		}
		nb, err := s.EnsureBank(ctx, b.Kind, name, owner, b.Description)
		if err != nil {
			return nil, err
		}
		bankMap[b.ID] = nb.ID
		res.Banks++
	}
	factMap := map[int64]int64{}
	for _, f := range d.Facts {
		text := strings.TrimSpace(f.Text)
		bid, ok := bankMap[f.Bank]
		if text == "" || len(text) > 4000 || !ok {
			res.Skipped++
			continue
		}
		kind := f.Kind
		if kind != ConclusionKind {
			kind = "fact"
		}
		var existing int64
		if f.ValidTo == nil {
			_ = s.db.QueryRow(ctx, `SELECT id FROM memory_facts WHERE bank_id=$1 AND valid_to IS NULL AND kind=$2 AND lower(btrim(text))=lower($3) LIMIT 1`, bid, kind, text).Scan(&existing)
		} else { // a retired fact is the same one when it was valid from the same moment
			_ = s.db.QueryRow(ctx, `SELECT id FROM memory_facts WHERE bank_id=$1 AND valid_to IS NOT NULL AND kind=$2 AND lower(btrim(text))=lower($3)
				AND date_trunc('milliseconds', valid_from)=date_trunc('milliseconds', $4::timestamptz) LIMIT 1`, bid, kind, text, f.ValidFrom).Scan(&existing)
		}
		if existing != 0 {
			factMap[f.ID] = existing
			res.Skipped++
			continue
		}
		tags := f.Tags
		if tags == nil {
			tags = []string{}
		}
		origins := f.Origins
		if origins == nil {
			origins = []string{}
		}
		conf := f.Confidence
		if conf <= 0 || conf > 1 {
			conf = 0.7
		}
		rank := f.Rank
		if rank <= 0 || rank > 5 {
			rank = 1
		}
		if f.ValidFrom.IsZero() {
			f.ValidFrom = time.Now()
		}
		if f.CreatedAt.IsZero() {
			f.CreatedAt = f.ValidFrom
		}
		var id int64
		err := s.db.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,tags,kind,rank,hits,confidence,source,origins,valid_from,valid_to,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
			bid, text, tags, kind, float32(rank), max(f.Hits, 0), float32(conf), "import", origins, f.ValidFrom, f.ValidTo, f.CreatedAt).Scan(&id)
		if err != nil {
			return res, err
		}
		factMap[f.ID] = id
		res.Facts++
	}
	for _, f := range d.Facts { // supersede chains
		nid, ok := factMap[f.ID]
		if !ok {
			continue
		}
		if f.Supersedes != nil {
			if o, ok := factMap[*f.Supersedes]; ok && o != nid {
				_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET supersedes=$2 WHERE id=$1 AND supersedes IS NULL`, nid, o)
			}
		}
		if f.SupersededBy != nil {
			if o, ok := factMap[*f.SupersededBy]; ok && o != nid {
				_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET superseded_by=$2 WHERE id=$1 AND superseded_by IS NULL`, nid, o)
			}
		}
	}
	for _, l := range d.Links {
		a, ok1 := factMap[l.A]
		b, ok2 := factMap[l.B]
		if !ok1 || !ok2 || a == b || !linkKinds[l.Kind] {
			continue
		}
		if s.Link(ctx, a, b, l.Kind, l.Note, "import", l.Weight) == nil {
			res.Links++
		}
	}
	if res.Facts > 0 {
		_, _ = s.EmbedMissing(ctx)
	}
	s.changed()
	return res, nil
}

// EmbedMissing embeds facts that have no vector yet (after an import or an undo), up to a few hundred at a time.
func (s *Service) EmbedMissing(ctx context.Context) (int, error) {
	if !s.llm.HasEmbedding(ctx) {
		return 0, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id,text FROM memory_facts WHERE embedding IS NULL ORDER BY id LIMIT 600`)
	if err != nil {
		return 0, err
	}
	type it struct {
		id   int64
		text string
	}
	var items []it
	for rows.Next() {
		var x it
		if rows.Scan(&x.id, &x.text) == nil {
			items = append(items, x)
		}
	}
	rows.Close()
	n := 0
	for i := 0; i < len(items); i += 32 {
		end := min(i+32, len(items))
		texts := make([]string, 0, end-i)
		for _, x := range items[i:end] {
			texts = append(texts, x.text)
		}
		model := s.embedModel(ctx)
		vs, err := s.llm.Embed(ctx, model, texts)
		if err != nil {
			return n, err
		}
		for j, v := range vs {
			if err := s.setEmbedding(ctx, items[i+j].id, items[i+j].text, encodeVec(normalize(v)), model); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
