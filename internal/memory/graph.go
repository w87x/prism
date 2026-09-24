package memory

import "context"

// GraphNode and GraphEdge describe the fact graph for the Memory page.
type GraphNode struct {
	ID       int64   `json:"id"`
	Text     string  `json:"text"`
	Bank     string  `json:"bank"`
	BankKind string  `json:"bank_kind"`
	Kind     string  `json:"kind"`
	Rank     float64 `json:"rank"`
	Conf     float64 `json:"confidence"`
	Retired  bool    `json:"retired,omitempty"`
	Links    int     `json:"links"`
}

type GraphEdge struct {
	A      int64   `json:"a"`
	B      int64   `json:"b"`
	Kind   string  `json:"kind"` // related | supports | contradicts | evidence | supersedes
	Weight float64 `json:"weight"`
}

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// More is how many facts of the selection are not shown.
	More int `json:"more"`
}

// Graph returns up to limit facts (linked ones first, then by rank) of a bank, or of all banks when bankID is 0,
// with the links between them and the supersede chains.
func (s *Service) Graph(ctx context.Context, bankID int64, history bool, limit int) (*Graph, error) {
	if limit <= 0 || limit > 300 {
		limit = 120
	}
	g := &Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	rows, err := s.db.Query(ctx, `SELECT f.id, left(f.text, 240), b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END, b.kind, f.kind, f.rank, f.confidence,
			f.valid_to IS NOT NULL, (SELECT count(*) FROM memory_links lk WHERE lk.a=f.id OR lk.b=f.id) AS nl,
			count(*) OVER () AS total
		FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE ($1=0 OR f.bank_id=$1) AND ($2 OR f.valid_to IS NULL)
		ORDER BY nl DESC, f.rank DESC, f.id DESC LIMIT $3`, bankID, history, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	in := map[int64]bool{}
	var ids []int64
	total := 0
	for rows.Next() {
		var n GraphNode
		var rk, cf float32
		if err := rows.Scan(&n.ID, &n.Text, &n.Bank, &n.BankKind, &n.Kind, &rk, &cf, &n.Retired, &n.Links, &total); err != nil {
			return nil, err
		}
		n.Rank, n.Conf = float64(rk), float64(cf)
		g.Nodes = append(g.Nodes, n)
		in[n.ID] = true
		ids = append(ids, n.ID)
	}
	rows.Close()
	g.More = max(0, total-len(g.Nodes))
	if len(ids) == 0 {
		return g, nil
	}
	lrows, err := s.db.Query(ctx, `SELECT a,b,kind,weight FROM memory_links WHERE a=ANY($1) AND b=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var e GraphEdge
		var w float32
		if lrows.Scan(&e.A, &e.B, &e.Kind, &w) == nil {
			e.Weight = float64(w)
			g.Edges = append(g.Edges, e)
		}
	}
	lrows.Close()
	srows, err := s.db.Query(ctx, `SELECT id, superseded_by FROM memory_facts WHERE id=ANY($1) AND superseded_by IS NOT NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var a, b int64
		if srows.Scan(&a, &b) == nil && in[b] {
			g.Edges = append(g.Edges, GraphEdge{A: a, B: b, Kind: "supersedes", Weight: 0.5})
		}
	}
	return g, srows.Err()
}
