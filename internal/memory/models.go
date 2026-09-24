package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A mental model is a standing question about the user's world ("What does Danil want from his media
// setup?", "Which projects are active and what blocks them?") together with its current answer. The answer
// is written from the memory itself — facts, conclusions and insights found for the question — cites the
// facts it rests on, and is rewritten whenever enough new facts have arrived in its scope. Agents read
// models first (memory_models) before searching raw facts, which is Hindsight's retrieval order:
// models, then observations, then facts.
const (
	DefaultModelMin = 3
	maxModelContext = 30
)

const modelPrompt = `You maintain one "mental model" of an assistant's memory: a standing QUESTION and its current ANSWER. You get the question, the previous answer (if any) and MEMORIES (numbered facts, conclusions and insights) found for it.

Write the answer as a compact, well-organised brief (at most 900 characters, plain sentences or short bullet lines) that someone could read instead of searching memory. Use only what the memories support; say plainly what is unknown. Prefer newer memories where they conflict and note the change. Do not mention memory ids in the text.
Answer JSON only: {"answer":"...","sources":[<ids of the memories the answer rests on>]}`

type Model struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Query       string     `json:"query"`
	BankID      *int64     `json:"bank_id"`
	Body        string     `json:"body"`
	Sources     []int64    `json:"sources"`
	RefreshedAt *time.Time `json:"refreshed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	Fresh       int        `json:"fresh"` // trusted facts added in its scope since the last refresh
}

const modelCols = `m.id,m.name,m.query,m.bank_id,m.body,m.sources,m.refreshed_at,m.created_at,
	(SELECT count(*) FROM memory_facts f WHERE f.kind='fact' AND f.valid_to IS NULL AND f.confidence>=0.5
		AND (m.bank_id IS NULL OR f.bank_id=m.bank_id) AND (m.refreshed_at IS NULL OR f.created_at>m.refreshed_at))`

func (s *Service) Models(ctx context.Context) ([]Model, error) {
	rows, err := s.db.Query(ctx, `SELECT `+modelCols+` FROM memory_models m ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Name, &m.Query, &m.BankID, &m.Body, &m.Sources, &m.RefreshedAt, &m.CreatedAt, &m.Fresh); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveModel creates a model (id 0) or edits its name, question or scope; a changed question empties the
// answer so the next refresh writes it afresh.
func (s *Service) SaveModel(ctx context.Context, id int64, name, query string, bankID *int64) (int64, error) {
	name, query = strings.TrimSpace(name), strings.TrimSpace(query)
	if name == "" || query == "" {
		return 0, errors.New("a model needs a name and a question")
	}
	if id == 0 {
		err := s.db.QueryRow(ctx, `INSERT INTO memory_models(name,query,bank_id) VALUES($1,$2,$3) RETURNING id`, name, query, bankID).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("a model called %q already exists", name)
		}
		return id, nil
	}
	_, err := s.db.Exec(ctx, `UPDATE memory_models SET name=$2, bank_id=$4,
		body=CASE WHEN query<>$3 THEN '' ELSE body END, refreshed_at=CASE WHEN query<>$3 THEN NULL ELSE refreshed_at END, query=$3 WHERE id=$1`,
		id, name, query, bankID)
	return id, err
}

func (s *Service) DeleteModel(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM memory_models WHERE id=$1`, id)
	return err
}

// RefreshModel rewrites one model's answer from the memories found for its question.
func (s *Service) RefreshModel(ctx context.Context, id int64) (*Model, error) {
	var m Model
	if err := s.db.QueryRow(ctx, `SELECT `+modelCols+` FROM memory_models m WHERE m.id=$1`, id).
		Scan(&m.ID, &m.Name, &m.Query, &m.BankID, &m.Body, &m.Sources, &m.RefreshedAt, &m.CreatedAt, &m.Fresh); err != nil {
		return nil, errors.New("model not found")
	}
	role := "role:chat"
	if s.llm.RoleRef(ctx, "chat") == "" {
		role = "role:fast"
	}
	if s.llm.RoleRef(ctx, strings.TrimPrefix(role, "role:")) == "" {
		return nil, errors.New("no model configured")
	}
	var specs []string
	if m.BankID != nil {
		b, err := s.bankByID(ctx, *m.BankID)
		if err != nil {
			return nil, err
		}
		specs = []string{b.Label()}
	} else {
		bs, err := s.Banks(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if b.Status == "active" && b.Facts > 0 {
				specs = append(specs, b.Label())
			}
		}
	}
	found, err := s.Find(ctx, FindReq{Query: m.Query, Banks: specs, K: maxModelContext, MinRel: 0.01, NoLinks: true})
	if err != nil {
		return nil, err
	}
	trusted := found[:0]
	for _, f := range found {
		if f.Confidence >= 0.5 {
			trusted = append(trusted, f)
		}
	}
	if len(trusted) == 0 {
		return nil, errors.New("memory holds nothing trusted about this question yet")
	}
	valid := map[int64]bool{}
	var sb strings.Builder
	fmt.Fprintf(&sb, "QUESTION: %s\n\nPREVIOUS ANSWER:\n%s\n\nMEMORIES:\n", m.Query, strings.TrimSpace(m.Body))
	for _, f := range trusted {
		valid[f.ID] = true
		kind := ""
		if f.Kind == "conclusion" {
			kind = " (conclusion)"
		}
		fmt.Fprintf(&sb, "%d [%s]%s: %s\n", f.ID, f.CreatedAt.Format("2006-01-02"), kind, f.Text)
	}
	var parsed struct {
		Answer  string  `json:"answer"`
		Sources []int64 `json:"sources"`
	}
	if err := s.llm.CompleteJSON(ctx, role, modelPrompt, s.guidance(ctx)+sb.String(), &parsed); err != nil {
		return nil, fmt.Errorf("model refresh: %w", err)
	}
	answer := strings.TrimSpace(parsed.Answer)
	if answer == "" {
		return nil, errors.New("the model returned an empty answer")
	}
	if len(answer) > 1500 {
		answer = answer[:1500]
	}
	src := validEvidence(parsed.Sources, valid)
	if src == nil {
		src = []int64{}
	}
	if _, err := s.db.Exec(ctx, `UPDATE memory_models SET body=$2, sources=$3, refreshed_at=now() WHERE id=$1`, id, answer, src); err != nil {
		return nil, err
	}
	s.changed()
	m.Body, m.Sources, m.Fresh = answer, src, 0
	now := time.Now()
	m.RefreshedAt = &now
	return &m, nil
}

// ModelsDue refreshes models that were never written or have gathered minNew new facts in their scope.
func (s *Service) ModelsDue(ctx context.Context, minNew, max int) ([]Model, error) {
	if minNew <= 0 {
		minNew = DefaultModelMin
	}
	ms, err := s.Models(ctx)
	if err != nil {
		return nil, err
	}
	var out []Model
	for _, m := range ms {
		if len(out) >= max {
			break
		}
		if m.RefreshedAt != nil && m.Fresh < minNew {
			continue
		}
		r, err := s.RefreshModel(ctx, m.ID)
		if err != nil {
			if m.RefreshedAt == nil && strings.Contains(err.Error(), "nothing trusted") {
				continue // nothing to say yet; try again when facts arrive
			}
			return out, err
		}
		out = append(out, *r)
	}
	return out, nil
}
