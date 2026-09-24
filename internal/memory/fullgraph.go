package memory

import (
	"context"
	"fmt"
)

// FullGraph merges the fact graph and the entity graph into one view: facts/conclusions and entities as
// nodes on the same canvas, with fact links, entity relations, and "mentions" edges (an entity to the
// facts it was extracted from) tying the two together. Built on top of Graph and EntityGraph rather than
// duplicating their queries — fact ids and entity ids come from different sequences and would collide, so
// every id here is prefixed ("f12", "e12") to stay unambiguous in one combined node list.
type FullNode struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"` // fact | entity
	Text     string  `json:"text"` // fact text, or the entity's name
	Kind     string  `json:"kind"` // fact: fact|conclusion — entity: person|organization|product|place|event|concept|entity
	Bank     string  `json:"bank,omitempty"`
	BankKind string  `json:"bank_kind,omitempty"`
	Rank     float64 `json:"rank,omitempty"`
	Conf     float64 `json:"confidence,omitempty"`
	Mentions int     `json:"mentions,omitempty"`
	Retired  bool    `json:"retired,omitempty"`
	Links    int     `json:"links,omitempty"`
}

type FullEdge struct {
	A      string  `json:"a"`
	B      string  `json:"b"`
	Kind   string  `json:"kind"` // related|supports|contradicts|evidence|supersedes (facts) | temporal|semantic (entities) | mentions
	Label  string  `json:"label,omitempty"`
	Weight float64 `json:"weight"`
}

type FullGraphResult struct {
	Nodes []FullNode `json:"nodes"`
	Edges []FullEdge `json:"edges"`
	More  int        `json:"more"`
}

func factNodeID(id int64) string   { return fmt.Sprintf("f%d", id) }
func entityNodeID(id int64) string { return fmt.Sprintf("e%d", id) }

func (s *Service) FullGraph(ctx context.Context, bankID int64, history bool, limit int) (*FullGraphResult, error) {
	g, err := s.Graph(ctx, bankID, history, limit)
	if err != nil {
		return nil, err
	}
	eg, err := s.EntityGraph(ctx, bankID, limit)
	if err != nil {
		return nil, err
	}
	out := &FullGraphResult{Nodes: []FullNode{}, Edges: []FullEdge{}, More: g.More + eg.More}

	factIDs := make([]int64, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		factIDs = append(factIDs, n.ID)
		out.Nodes = append(out.Nodes, FullNode{ID: factNodeID(n.ID), Type: "fact", Text: n.Text, Kind: n.Kind,
			Bank: n.Bank, BankKind: n.BankKind, Rank: n.Rank, Conf: n.Conf, Retired: n.Retired, Links: n.Links})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, FullEdge{A: factNodeID(e.A), B: factNodeID(e.B), Kind: e.Kind, Weight: e.Weight})
	}

	entIDs := make([]int64, 0, len(eg.Nodes))
	for _, n := range eg.Nodes {
		entIDs = append(entIDs, n.ID)
		out.Nodes = append(out.Nodes, FullNode{ID: entityNodeID(n.ID), Type: "entity", Text: n.Name, Kind: n.Kind, Mentions: n.Mentions})
	}
	for _, e := range eg.Edges {
		out.Edges = append(out.Edges, FullEdge{A: entityNodeID(e.A), B: entityNodeID(e.B), Kind: e.Kind, Label: e.Label, Weight: e.Weight})
	}

	if len(entIDs) > 0 && len(factIDs) > 0 {
		// restricted to facts/entities actually shown, so a mention never draws an edge to a node that got
		// trimmed out by the limit — a dangling edge to nothing the layout can place.
		rows, err := s.db.Query(ctx, `SELECT entity_id, fact_id FROM memory_entity_mentions WHERE entity_id=ANY($1) AND fact_id=ANY($2)`, entIDs, factIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var eid, fid int64
			if rows.Scan(&eid, &fid) == nil {
				out.Edges = append(out.Edges, FullEdge{A: entityNodeID(eid), B: factNodeID(fid), Kind: "mentions", Weight: 0.35})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
