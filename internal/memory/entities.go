package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Entities turns facts into a second, coarser graph: named things (people, organisations, products,
// places, events, concepts) and typed relations between them, so "what do I know about X" or "how are X
// and Y related" can be answered without re-reading every fact that happens to mention them. This mirrors
// Reflect's shape closely (an LLM pass over a bank's new facts, watermarked so it only looks at what
// changed) but produces a different kind of node: an entity is a thing, not a belief.
const (
	maxEntityFacts = 60
	maxEntityItems = 12 // entities or relations per pass, so one bad reply cannot flood the graph
)

var entityKinds = map[string]bool{
	"entity": true, "person": true, "organization": true, "product": true, "place": true, "event": true, "concept": true,
}

var entityLinkKinds = map[string]bool{"related": true, "temporal": true, "semantic": true}

const entityPrompt = `You extract entities and relations from an assistant's memory facts, for a knowledge graph.

An entity is a specific, named, concrete thing: a person, an organisation, a product, a place, an event or a durable concept — not a generic noun, not the user themself (the user is implicit, do not create an entity for "the user" or "I"). Reuse an EXISTING ENTITY's exact name when a fact refers to the same thing; do not create a near-duplicate with slightly different wording.

For every entity give: "name" (canonical, short), "kind" (person|organization|product|place|event|concept|entity if unsure), "facts" (ids of facts that mention it).
For every relation give: "a" and "b" (entity names, must be among the entities you listed or already existing), "kind" (temporal: happens before/after/during; semantic: is-a/part-of/owns/works-at/located-in; related: anything else), "label" (a short human-readable phrase, e.g. "works at", "released on", "located in"), "facts" (ids that evidence it).

Rules: never invent facts not grounded in the text; skip trivial or vague entities; at most 12 entities and 12 relations; if nothing qualifies return empty lists.
Answer JSON only: {"entities":[{"name":"...","kind":"...","facts":[1]}],"relations":[{"a":"...","b":"...","kind":"related","label":"...","facts":[1]}]}`

type EntityExtractResult struct {
	Bank       string `json:"bank"`
	Considered int    `json:"considered"`
	Entities   int    `json:"entities"`
	Relations  int    `json:"relations"`
	Skipped    string `json:"skipped,omitempty"`
}

func (r EntityExtractResult) String() string {
	if r.Skipped != "" {
		return fmt.Sprintf("%s: skipped (%s)", r.Bank, r.Skipped)
	}
	return fmt.Sprintf("%s: %d entities, %d relations (from %d facts)", r.Bank, r.Entities, r.Relations, r.Considered)
}

// ExtractEntities looks at a bank's facts created since the last pass (or all of them, with force) and
// extracts entities and relations. Idempotent: re-running finds the same entities by name and simply
// adds new mentions/evidence rather than duplicating them.
func (s *Service) ExtractEntities(ctx context.Context, bankID int64, force bool) (EntityExtractResult, error) {
	var res EntityExtractResult
	var b Bank
	var since *time.Time
	if err := s.db.QueryRow(ctx, `SELECT id,kind,name,owner,description,status,created_at,entities_at FROM memory_banks WHERE id=$1`, bankID).
		Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt, &since); err != nil {
		return res, errors.New("memory bank not found")
	}
	res.Bank = b.Label()
	if s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no fast model configured"
		return res, nil
	}

	q := `SELECT id,text FROM memory_facts WHERE bank_id=$1 AND kind='fact' AND valid_to IS NULL`
	args := []any{bankID}
	if !force && since != nil {
		q += ` AND created_at > $2`
		args = append(args, *since)
	}
	q += ` ORDER BY id DESC LIMIT ` + fmt.Sprint(maxEntityFacts)
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return res, err
	}
	type fr struct {
		id   int64
		text string
	}
	var facts []fr
	byID := map[int64]bool{}
	for rows.Next() {
		var f fr
		if rows.Scan(&f.id, &f.text) == nil {
			facts = append(facts, f)
			byID[f.id] = true
		}
	}
	rows.Close()
	res.Considered = len(facts)
	if len(facts) == 0 {
		res.Skipped = "no new facts"
		return res, nil
	}

	existing, err := s.entityNames(ctx, bankID)
	if err != nil {
		return res, err
	}

	var sb strings.Builder
	sb.WriteString("FACTS:\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "%d: %s\n", f.id, f.text)
	}
	sb.WriteString("\nEXISTING ENTITIES:\n")
	if len(existing) == 0 {
		sb.WriteString("(none yet)\n")
	} else {
		sb.WriteString(strings.Join(existing, ", ") + "\n")
	}
	var parsed struct {
		Entities []struct {
			Name  string  `json:"name"`
			Kind  string  `json:"kind"`
			Facts []int64 `json:"facts"`
		} `json:"entities"`
		Relations []struct {
			A     string  `json:"a"`
			B     string  `json:"b"`
			Kind  string  `json:"kind"`
			Label string  `json:"label"`
			Facts []int64 `json:"facts"`
		} `json:"relations"`
	}
	if err := s.llm.CompleteJSON(ctx, "role:fast", entityPrompt, s.guidance(ctx)+"Bank: "+b.Label()+"\n\n"+sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("entity extraction: %w", err)
	}
	if len(parsed.Entities) > maxEntityItems {
		parsed.Entities = parsed.Entities[:maxEntityItems]
	}
	if len(parsed.Relations) > maxEntityItems {
		parsed.Relations = parsed.Relations[:maxEntityItems]
	}

	byName := map[string]int64{}
	for _, e := range parsed.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" || strings.EqualFold(name, "user") || strings.EqualFold(name, "the user") {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		if !entityKinds[kind] {
			kind = "entity"
		}
		ev := validEvidence(e.Facts, byID)
		id, isNew, err := s.upsertEntity(ctx, bankID, name, kind, len(ev))
		if err != nil {
			continue
		}
		byName[strings.ToLower(name)] = id
		if isNew {
			res.Entities++
		}
		for _, fid := range ev {
			_, _ = s.db.Exec(ctx, `INSERT INTO memory_entity_mentions(entity_id,fact_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, fid)
		}
	}
	// a relation may reference an entity that already existed before this pass (not just one just created)
	resolve := func(name string) (int64, bool) {
		if id, ok := byName[strings.ToLower(strings.TrimSpace(name))]; ok {
			return id, true
		}
		var id int64
		if err := s.db.QueryRow(ctx, `SELECT id FROM memory_entities WHERE bank_id=$1 AND lower(name)=lower($2)`, bankID, strings.TrimSpace(name)).Scan(&id); err == nil {
			return id, true
		}
		return 0, false
	}
	for _, rl := range parsed.Relations {
		aID, aok := resolve(rl.A)
		bID, bok := resolve(rl.B)
		if !aok || !bok || aID == bID {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(rl.Kind))
		if !entityLinkKinds[kind] {
			kind = "related"
		}
		if s.linkEntities(ctx, aID, bID, kind, strings.TrimSpace(rl.Label), "auto", 0.6) == nil {
			res.Relations++
		}
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_banks SET entities_at=now() WHERE id=$1`, bankID)
	if res.Entities+res.Relations > 0 {
		s.changed()
	}
	return res, nil
}

func (s *Service) entityNames(ctx context.Context, bankID int64) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name FROM memory_entities WHERE bank_id=$1 ORDER BY mentions DESC LIMIT 200`, bankID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// upsertEntity finds or creates an entity by (bank, name) and bumps its mention stats; isNew reports
// whether this call created it.
func (s *Service) upsertEntity(ctx context.Context, bankID int64, name, kind string, mentionDelta int) (id int64, isNew bool, err error) {
	err = s.db.QueryRow(ctx, `INSERT INTO memory_entities(bank_id,kind,name,mentions,last_seen) VALUES($1,$2,$3,$4,now())
		ON CONFLICT (bank_id,name) DO UPDATE SET
			kind=CASE WHEN memory_entities.kind='entity' THEN EXCLUDED.kind ELSE memory_entities.kind END,
			mentions=memory_entities.mentions+EXCLUDED.mentions, last_seen=now()
		RETURNING id, (xmax=0)`, bankID, kind, name, mentionDelta).Scan(&id, &isNew)
	return id, isNew, err
}

// linkEntities creates or strengthens the relation between two entities (undirected, a<b, one per pair —
// mirrors Link for facts).
func (s *Service) linkEntities(ctx context.Context, a, b int64, kind, label, by string, weight float64) error {
	if a == b {
		return errors.New("an entity cannot be linked to itself")
	}
	a, b = order(a, b)
	_, err := s.db.Exec(ctx, `INSERT INTO memory_entity_links(a,b,kind,label,weight,source) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (a,b) DO UPDATE SET kind=EXCLUDED.kind, label=CASE WHEN EXCLUDED.label<>'' THEN EXCLUDED.label ELSE memory_entity_links.label END,
			source=EXCLUDED.source, weight=LEAST(1,GREATEST(memory_entity_links.weight, EXCLUDED.weight)+0.05)`,
		a, b, kind, label, float32(weight), by)
	return err
}

// EntityGraph describes the entity graph for the Memory page, alongside the fact graph.
type EntityNode struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Mentions int       `json:"mentions"`
	LastSeen time.Time `json:"last_seen"`
}

type EntityEdge struct {
	A      int64   `json:"a"`
	B      int64   `json:"b"`
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
}

type EntityGraphResult struct {
	Nodes []EntityNode `json:"nodes"`
	Edges []EntityEdge `json:"edges"`
	More  int          `json:"more"`
}

func (s *Service) EntityGraph(ctx context.Context, bankID int64, limit int) (*EntityGraphResult, error) {
	if limit <= 0 || limit > 300 {
		limit = 150
	}
	g := &EntityGraphResult{Nodes: []EntityNode{}, Edges: []EntityEdge{}}
	rows, err := s.db.Query(ctx, `SELECT id,name,kind,mentions,last_seen, count(*) OVER () FROM memory_entities
		WHERE ($1=0 OR bank_id=$1) ORDER BY mentions DESC, id DESC LIMIT $2`, bankID, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	total := 0
	for rows.Next() {
		var n EntityNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Kind, &n.Mentions, &n.LastSeen, &total); err != nil {
			rows.Close()
			return nil, err
		}
		g.Nodes = append(g.Nodes, n)
		ids = append(ids, n.ID)
	}
	rows.Close()
	g.More = max(0, total-len(g.Nodes))
	if len(ids) == 0 {
		return g, nil
	}
	erows, err := s.db.Query(ctx, `SELECT a,b,kind,label,weight FROM memory_entity_links WHERE a=ANY($1) AND b=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var e EntityEdge
		var w float32
		if erows.Scan(&e.A, &e.B, &e.Kind, &e.Label, &w) == nil {
			e.Weight = float64(w)
			g.Edges = append(g.Edges, e)
		}
	}
	return g, erows.Err()
}

// EntityFacts lists the facts an entity was mentioned in — "why do you know this".
func (s *Service) EntityFacts(ctx context.Context, entityID int64) ([]Fact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		JOIN memory_entity_mentions m ON m.fact_id=f.id WHERE m.entity_id=$1 ORDER BY f.id DESC`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, rows.Err()
}

// DefaultEntitiesMin is how many new facts in a bank make an entity-extraction pass worthwhile.
const DefaultEntitiesMin = 8

// EntitiesDue extracts entities in the banks that gathered at least minNew new facts since their last pass
// (at most maxBanks per call), so the entity graph keeps up without anyone pressing a button.
func (s *Service) EntitiesDue(ctx context.Context, minNew, maxBanks int) ([]EntityExtractResult, error) {
	if minNew <= 0 {
		minNew = DefaultEntitiesMin
	}
	rows, err := s.db.Query(ctx, `SELECT b.id FROM memory_banks b WHERE b.status='active' AND
		(SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.kind='fact' AND f.valid_to IS NULL AND f.confidence>=0.5
			AND (b.entities_at IS NULL OR f.created_at>b.entities_at)) >= $1
		ORDER BY b.entities_at NULLS FIRST LIMIT $2`, minNew, maxBanks)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []EntityExtractResult
	for _, id := range ids {
		r, err := s.ExtractEntities(ctx, id, false)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}
