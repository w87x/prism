// Package memory implements PRISM's banked long-term memory: user, profile,
// project and domain banks of ranked facts, plus a raw bank that is distilled
// into facts. Facts are never edited in place when the world changes; a newer
// fact supersedes the old one (kept, with validity interval) so history stays.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/db"
	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/textmatch"
)

const (
	KindUser    = "user"
	KindProfile = "profile"
	KindProject = "project"
	KindDomain  = "domain"
)

type Bank struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	Owner       string    `json:"owner"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Facts       int       `json:"facts"`
	CreatedAt   time.Time `json:"created_at"`
}

// Label is the addressable form: "user", "profile:Scout", "project:Price check".
func (b Bank) Label() string {
	if b.Kind == KindUser {
		return "user"
	}
	return b.Kind + ":" + b.Name
}

type Fact struct {
	ID           int64      `json:"id"`
	BankID       int64      `json:"bank_id"`
	Bank         string     `json:"bank"`
	Text         string     `json:"text"`
	Tags         []string   `json:"tags"`
	Rank         float64    `json:"rank"`
	Hits         int        `json:"hits"`
	Confidence   float64    `json:"confidence"`
	Source       string     `json:"source"`
	Supersedes   *int64     `json:"supersedes,omitempty"`
	SupersededBy *int64     `json:"superseded_by,omitempty"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidTo      *time.Time `json:"valid_to,omitempty"`
	LastUsed     *time.Time `json:"last_used,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	Embedded     bool       `json:"embedded"`
	Links        int        `json:"links"`
	// Kind is "fact" or "conclusion" (a belief derived from several facts; see Reflect). Proof counts the facts
	// a conclusion rests on; Stale marks one whose evidence has since been retired.
	Kind  string  `json:"kind"`
	Proof int     `json:"proof,omitempty"`
	Stale bool    `json:"stale,omitempty"`
	Score float64 `json:"score,omitempty"`
	// Origins are the web sites (registrable domains) a fact was learned from. The same fact found on several
	// independent sites is trusted more (see corroborated).
	Origins []string `json:"origins,omitempty"`
	// Via is set on facts that were not matched by the query themselves but reached through a link from this fact.
	Via *int64 `json:"via,omitempty"`
	// TaskID is the task/conversation this fact was stored during or extracted from, when known — "why do
	// you remember this" (see task_transcript for the fuller record, and internal/db/migrations/021).
	TaskID int64 `json:"task_id,omitempty"`
	// Pinned exempts a fact from Prune's auto-archival and from rank's time-decay in retrieval scoring.
	Pinned bool `json:"pinned"`
}

type Service struct {
	db       *pgxpool.Pool
	llm      *llm.Router
	settings *settings.Store
	OnChange func() // notify the UI
	// OnNew fires when something that background maintenance might act on arrived (a fact changed, a raw
	// message was queued); the app uses it to run its pass soon instead of waiting for the next timer tick.
	OnNew func()
	// ChatProject names the project bank a web chat is focused on ("" when none); set by the app. Distillation uses
	// it to file facts about the work in that project by default.
	ChatProject func(ctx context.Context, channel, topic string) string
	// VectorOn makes similarity search run inside Postgres (pgvector) instead of in-process.
	VectorOn bool
}

func New(db *pgxpool.Pool, r *llm.Router, st *settings.Store) *Service {
	return &Service{db: db, llm: r, settings: st}
}

// ── banks ───────────────────────────────────────────────────────────────────

// ParseSpec resolves "user", "profile", "profile:Name", "project:X", "domain:Y".
// agent is used for a bare "profile".
func ParseSpec(spec, agent string) (kind, name, owner string, err error) {
	spec = strings.TrimSpace(spec)
	k, n, has := strings.Cut(spec, ":")
	k = strings.ToLower(strings.TrimSpace(k))
	n = strings.TrimSpace(n)
	switch k {
	case KindUser:
		return KindUser, "user", "", nil
	case KindProfile:
		if !has || n == "" {
			n = agent
		}
		if n == "" {
			return "", "", "", errors.New("profile bank needs an agent name")
		}
		return KindProfile, n, n, nil
	case KindProject, KindDomain:
		if n == "" {
			return "", "", "", fmt.Errorf("%s bank needs a name (%s:<name>)", k, k)
		}
		return k, n, "", nil
	}
	return "", "", "", fmt.Errorf("unknown bank %q: use user, profile, project:<name> or domain:<name>", spec)
}

func (s *Service) EnsureBank(ctx context.Context, kind, name, owner, desc string) (*Bank, error) {
	var b Bank
	err := s.db.QueryRow(ctx, `INSERT INTO memory_banks(kind,name,owner,description) VALUES($1,$2,$3,$4)
		ON CONFLICT (kind,name,owner) DO UPDATE SET status = CASE WHEN memory_banks.status='archived' THEN 'active' ELSE memory_banks.status END
		RETURNING id,kind,name,owner,description,status,created_at`, kind, name, owner, desc).
		Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt)
	return &b, err
}

func (s *Service) BankBySpec(ctx context.Context, spec, agent string, create bool) (*Bank, error) {
	kind, name, owner, err := ParseSpec(spec, agent)
	if err != nil {
		return nil, err
	}
	if create {
		return s.EnsureBank(ctx, kind, name, owner, "")
	}
	var b Bank
	err = s.db.QueryRow(ctx, `SELECT id,kind,name,owner,description,status,created_at FROM memory_banks WHERE kind=$1 AND name=$2 AND owner=$3`, kind, name, owner).
		Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("memory bank %q does not exist", spec)
	}
	return &b, err
}

func (s *Service) Banks(ctx context.Context) ([]Bank, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id,b.kind,b.name,b.owner,b.description,b.status,b.created_at,
		(SELECT count(*) FROM memory_facts f WHERE f.bank_id=b.id AND f.valid_to IS NULL)
		FROM memory_banks b ORDER BY b.kind, b.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bank
	for rows.Next() {
		var b Bank
		if err := rows.Scan(&b.ID, &b.Kind, &b.Name, &b.Owner, &b.Description, &b.Status, &b.CreatedAt, &b.Facts); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) SaveBank(ctx context.Context, b Bank) (int64, error) {
	if b.ID == 0 {
		nb, err := s.EnsureBank(ctx, b.Kind, b.Name, b.Owner, b.Description)
		if err != nil {
			return 0, err
		}
		return nb.ID, nil
	}
	_, err := s.db.Exec(ctx, `UPDATE memory_banks SET name=$2, description=$3, status=$4 WHERE id=$1`, b.ID, b.Name, b.Description, b.Status)
	return b.ID, err
}

func (s *Service) DeleteBank(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM memory_banks WHERE id=$1 AND kind<>'user'`, id)
	return err
}

// ── facts ───────────────────────────────────────────────────────────────────

const factCols = `f.id,f.bank_id,b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END,f.text,f.tags,f.rank,f.hits,f.confidence,f.source,
	f.supersedes,f.superseded_by,f.valid_from,f.valid_to,f.last_used,f.created_at,f.embedding IS NOT NULL,
	(SELECT count(*) FROM memory_links lk WHERE lk.a=f.id OR lk.b=f.id),
	f.kind,
	CASE WHEN f.kind='conclusion' THEN (SELECT count(*) FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=f.id THEN e.b ELSE e.a END
		WHERE (e.a=f.id OR e.b=f.id) AND e.kind='evidence' AND x.kind='fact') ELSE 0 END,
	CASE WHEN f.kind='conclusion' THEN EXISTS(SELECT 1 FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=f.id THEN e.b ELSE e.a END
		WHERE (e.a=f.id OR e.b=f.id) AND e.kind='evidence' AND x.kind='fact' AND x.valid_to IS NOT NULL) ELSE false END,
	f.origins,f.task_id,f.pinned`

func scanFact(r pgx.Row) (Fact, error) {
	var f Fact
	var rk, cf float32
	var taskID *int64
	err := r.Scan(&f.ID, &f.BankID, &f.Bank, &f.Text, &f.Tags, &rk, &f.Hits, &cf, &f.Source,
		&f.Supersedes, &f.SupersededBy, &f.ValidFrom, &f.ValidTo, &f.LastUsed, &f.CreatedAt, &f.Embedded, &f.Links, &f.Kind, &f.Proof, &f.Stale, &f.Origins,
		&taskID, &f.Pinned)
	f.Rank, f.Confidence = float64(rk), float64(cf)
	if taskID != nil {
		f.TaskID = *taskID
	}
	return f, err
}

func (s *Service) Facts(ctx context.Context, bankID int64, q string, history bool, limit, offset int) ([]Fact, error) {
	return s.FactsKind(ctx, bankID, "", q, history, limit, offset)
}

// FactsKind is Facts limited to one kind ("fact" or "conclusion"; empty = both).
func (s *Service) FactsKind(ctx context.Context, bankID int64, kind, q string, history bool, limit, offset int) ([]Fact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	sql := `SELECT ` + factCols + ` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE ($1=0 OR f.bank_id=$1)`
	if kind == "fact" || kind == ConclusionKind {
		sql += ` AND f.kind='` + kind + `'`
	}
	if !history {
		sql += ` AND f.valid_to IS NULL`
	}
	args := []any{bankID}
	if q = strings.TrimSpace(q); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		sql += fmt.Sprintf(` AND lower(f.text) LIKE $%d`, len(args))
	}
	args = append(args, limit, offset)
	sql += fmt.Sprintf(` ORDER BY f.rank DESC, f.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := s.db.Query(ctx, sql, args...)
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
	return out, rows.Err()
}

func (s *Service) GetFact(ctx context.Context, id int64) (Fact, error) {
	return scanFact(s.db.QueryRow(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE f.id=$1`, id))
}

// UpdateFact edits a fact. Changing only tags/rank updates the row in place — that's metadata, not a claim
// about the world. Changing the TEXT is a correction and must preserve history rather than silently
// overwrite it: the old row is superseded (valid_to/superseded_by set, exactly like Store()'s own supersede
// path) and a new row takes its place. This also means any conclusion resting on the old text is
// automatically flagged for review the next time it's read — factCols' "stale" column is a live computation
// off valid_to, so superseding is enough; nothing extra needs to run reflection eagerly.
func (s *Service) UpdateFact(ctx context.Context, id int64, text *string, tags []string, rank *float64) (*Fact, error) {
	cur, err := s.GetFact(ctx, id)
	if err != nil {
		return nil, err
	}
	if rank != nil && math.Abs(*rank-cur.Rank) > 0.001 { // a hand-set rank is remembered so the UI can tell it from a learned one
		base := cur.Tags
		if tags != nil {
			base = tags
		}
		if !slices.Contains(base, "user-rank") {
			tags = append(append([]string{}, base...), "user-rank")
		}
	}
	newText := strings.TrimSpace(firstNonEmptyStr(text, cur.Text))
	if text != nil && newText != "" && normFactText(newText) != normFactText(cur.Text) {
		newTags := cur.Tags
		if tags != nil {
			newTags = tags
		}
		newRank := cur.Rank
		if rank != nil {
			newRank = *rank
		}
		emb, model := s.embedOne(ctx, newText)
		var taskID any
		if cur.TaskID != 0 {
			taskID = cur.TaskID
		}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		var newID int64
		if s.VectorOn {
			err = tx.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,tags,rank,embedding,vec,confidence,source,supersedes,pinned,task_id,embed_model)
				VALUES($1,$2,$3,$4,$5,$6::vector,$7,$8,$9,$10,$11,$12) RETURNING id`,
				cur.BankID, newText, newTags, float32(newRank), emb, s.vecArg(emb), float32(cur.Confidence), cur.Source, id, cur.Pinned, taskID, model).Scan(&newID)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,tags,rank,embedding,confidence,source,supersedes,pinned,task_id,embed_model)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
				cur.BankID, newText, newTags, float32(newRank), emb, float32(cur.Confidence), cur.Source, id, cur.Pinned, taskID, model).Scan(&newID)
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1`, id, newID); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		s.changed()
		f, err := s.GetFact(ctx, newID)
		return &f, err
	}
	if tags != nil {
		if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET tags=$2 WHERE id=$1`, id, tags); err != nil {
			return nil, err
		}
	}
	if rank != nil {
		if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET rank=$2 WHERE id=$1`, id, float32(*rank)); err != nil {
			return nil, err
		}
	}
	s.changed()
	f, err := s.GetFact(ctx, id)
	return &f, err
}

func firstNonEmptyStr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// SetPinned marks a fact as the user's own standing say that it matters — exempt from Prune's auto-archival
// and from rank's time-decay in retrieval scoring (see effRank), whatever usage does or doesn't happen to it.
func (s *Service) SetPinned(ctx context.Context, id int64, pinned bool) error {
	_, err := s.db.Exec(ctx, `UPDATE memory_facts SET pinned=$2 WHERE id=$1`, id, pinned)
	s.changed()
	return err
}

// MarkOutdated retires a fact with no replacement — "this is no longer true/relevant" without correcting it
// to something else (that's UpdateFact's job). It stays inspectable with history, just excluded from normal
// (non-history) retrieval, same as any other superseded fact.
func (s *Service) MarkOutdated(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1 AND valid_to IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is already retired (or does not exist)", id)
	}
	s.changed()
	return nil
}

func (s *Service) DeleteFact(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM memory_facts WHERE id=$1`, id)
	s.changed()
	return err
}

func (s *Service) MoveFact(ctx context.Context, id, bankID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE memory_facts SET bank_id=$2 WHERE id=$1`, id, bankID)
	s.changed()
	return err
}

func (s *Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
	s.nudge()
}

func (s *Service) nudge() {
	if s.OnNew != nil {
		s.OnNew()
	}
}

// vecArg converts a stored embedding to the pgvector literal argument (nil when unavailable).
func (s *Service) vecArg(emb []byte) any {
	if !s.VectorOn || emb == nil {
		return nil
	}
	lit := db.VectorLiteral(decodeVec(emb))
	if lit == "" {
		return nil
	}
	return lit
}

// embedModel is the current embedding model's reference (e.g. "embed"), used as an identity tag on every
// vector so a later switch of embedding model can't have its vectors silently compared against the old
// model's as if they lived in the same space — matching dimensions alone does not make that true.
func (s *Service) embedModel(ctx context.Context) string {
	return s.llm.RoleRef(ctx, "embedding")
}

func (s *Service) embedOne(ctx context.Context, text string) (emb []byte, model string) {
	model = s.embedModel(ctx)
	if model == "" {
		return nil, ""
	}
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v, err := s.llm.Embed(ectx, model, []string{text})
	if err != nil || len(v) == 0 {
		return nil, ""
	}
	return encodeVec(normalize(v[0])), model
}

func (s *Service) setEmbedding(ctx context.Context, id int64, text string, emb []byte, model string) error {
	if s.VectorOn {
		_, err := s.db.Exec(ctx, `UPDATE memory_facts SET text=$2, embedding=$3, vec=$4::vector, embed_model=$5 WHERE id=$1`, id, text, emb, s.vecArg(emb), model)
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE memory_facts SET text=$2, embedding=$3, embed_model=$4 WHERE id=$1`, id, text, emb, model)
	return err
}

// ── storing ─────────────────────────────────────────────────────────────────

type StoreReq struct {
	Bank       string // spec
	Agent      string // resolves bare "profile"
	Text       string
	Tags       []string
	Source     string
	Confidence float64
	// Origin is the site (registrable domain) the fact was learned from, when it came from the web.
	Origin string
	// TaskID is the task/conversation this fact is being stored during, when known (0 = none) — see
	// Fact.TaskID.
	TaskID int64
}

type StoreResult struct {
	Fact       Fact    `json:"fact"`
	Duplicate  bool    `json:"duplicate"`
	Superseded []int64 `json:"superseded,omitempty"`
	// Corroborated: this was a known web-learned fact and the new report raised its confidence.
	Corroborated bool `json:"corroborated,omitempty"`
}

var (
	dupSim     = 0.96
	relatedSim = 0.6
)

// Store adds a fact, deduplicating and superseding contradicted facts.
func (s *Service) Store(ctx context.Context, r StoreReq) (*StoreResult, error) {
	r.Text = strings.TrimSpace(r.Text)
	if r.Text == "" {
		return nil, errors.New("empty fact")
	}
	if r.Confidence == 0 {
		r.Confidence = 0.7
	}
	bank, err := s.BankBySpec(ctx, r.Bank, r.Agent, true)
	if err != nil {
		return nil, err
	}
	emb, embModel := s.embedOne(ctx, r.Text)
	vec := decodeVec(emb)

	// neighbours in the same bank: vector-nearest ∪ text-nearest ∪ the top-ranked facts, not a flat
	// popularity pre-filter (see gatherCandidates) — a rarely-used but near-identical fact must still be
	// found, whether the question is "is this a duplicate?" or "is this a correction of something older?".
	var pgSims map[int64]float64
	if s.VectorOn && vec != nil {
		pgSims = s.vecSims(ctx, []int64{bank.ID}, false, vec, 20)
	}
	cands, err := s.gatherCandidates(ctx, []int64{bank.ID}, false, r.Text, pgSims)
	if err != nil {
		return nil, err
	}
	type near struct {
		c   cand
		sim float64
	}
	var ns []near
	for _, c := range cands {
		var sim float64
		if pgSims != nil {
			if v, ok := pgSims[c.id]; ok {
				sim = v
			} else {
				sim = jaccard(r.Text, c.text) * 0.8
			}
		} else if vec != nil && c.vec != nil {
			sim = Dot(vec, c.vec)
		} else {
			sim = jaccard(r.Text, c.text)
			if sim < 0.5 { // lexical overlap is a weak proxy: scale it down
				sim *= 0.8
			}
		}
		if sim >= relatedSim || (vec == nil && sim >= 0.35) {
			ns = append(ns, near{c, sim})
		}
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].sim > ns[j].sim })
	if len(ns) > 4 {
		ns = ns[:4]
	}
	// Only an EXACT match (case/whitespace-insensitive) is merged automatically. A fact can be highly similar
	// to an existing one — the same sentence with a changed price, date or preference — and similarity alone
	// cannot tell a duplicate from a correction; that decision goes to the classifier below, which sees both
	// texts. Skipping it here would risk silently reinforcing a now-wrong fact instead of superseding it.
	if len(ns) > 0 && normFactText(ns[0].c.text) == normFactText(r.Text) {
		cor := s.reinforce(ctx, ns[0].c.id, r)
		f, err := s.GetFact(ctx, ns[0].c.id)
		return &StoreResult{Fact: f, Duplicate: true, Corroborated: cor}, err
	}
	var supersede []int64
	if len(ns) > 0 && s.llm.RoleRef(ctx, "fast") != "" {
		valid := make(map[int64]bool, len(ns))
		var lines []string
		for _, n := range ns {
			valid[n.c.id] = true
			lines = append(lines, fmt.Sprintf(`%d: %s`, n.c.id, n.c.text))
		}
		out, err := s.llm.Complete(ctx, "role:fast", `You maintain a memory of facts. Given NEW and existing OLD facts, decide for each OLD fact its relation to NEW:
"same" (NEW adds nothing), "update" (NEW replaces or corrects OLD, e.g. a changed preference), "contradiction" (both cannot be true, NEW is more recent) or "unrelated".
Answer JSON only: {"relations":[{"id":<old id>,"relation":"same|update|contradiction|unrelated"}]}`,
			"NEW: "+r.Text+"\nOLD:\n"+strings.Join(lines, "\n"), true)
		if err == nil {
			var parsed struct {
				Relations []struct {
					ID       int64  `json:"id"`
					Relation string `json:"relation"`
				} `json:"relations"`
			}
			if json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed) == nil {
				for _, rel := range parsed.Relations {
					if !valid[rel.ID] { // the model must only judge the candidates it was actually shown
						continue
					}
					switch rel.Relation {
					case "same":
						cor := s.reinforce(ctx, rel.ID, r)
						f, gerr := s.GetFact(ctx, rel.ID)
						return &StoreResult{Fact: f, Duplicate: true, Corroborated: cor}, gerr
					case "update", "contradiction":
						supersede = append(supersede, rel.ID)
					}
				}
			}
		}
	}
	// no classifier available and nothing was an exact match: store as a new fact rather than guessing whether
	// a merely-similar candidate is a duplicate or a correction (relatedSim alone is not proof of either).

	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	origins := []string{}
	if r.Origin != "" {
		origins = append(origins, r.Origin)
	}
	// insert + supersede updates happen in one transaction: a crash or error partway must never leave a new
	// fact recorded while its predecessors are still (or only partly) marked live.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var sup *int64
	if len(supersede) > 0 {
		sup = &supersede[0]
	}
	var taskID any
	if r.TaskID != 0 {
		taskID = r.TaskID
	}
	if s.VectorOn {
		err = tx.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,tags,embedding,vec,confidence,source,supersedes,origins,embed_model,task_id)
			VALUES($1,$2,$3,$4,$5::vector,$6,$7,$8,$9,$10,$11) RETURNING id`,
			bank.ID, r.Text, tags, emb, s.vecArg(emb), float32(r.Confidence), r.Source, sup, origins, embModel, taskID).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text,tags,embedding,confidence,source,supersedes,origins,embed_model,task_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			bank.ID, r.Text, tags, emb, float32(r.Confidence), r.Source, sup, origins, embModel, taskID).Scan(&id)
	}
	if err != nil {
		return nil, err
	}
	for _, old := range supersede {
		if _, err := tx.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND valid_to IS NULL`, old, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	retired := map[int64]bool{}
	for _, old := range supersede {
		retired[old] = true
	}
	var close []neighbour
	for _, n := range ns {
		if !retired[n.c.id] {
			close = append(close, neighbour{n.c.id, n.sim})
		}
	}
	s.autoLink(ctx, id, bank.ID, r.Text, vec, close)
	f, err := s.GetFact(ctx, id)
	s.changed()
	return &StoreResult{Fact: f, Superseded: supersede}, err
}

type cand struct {
	id       int64
	bankID   int64
	text     string
	vec      []float32
	model    string // the embedding model that produced vec ("" = unlabeled legacy row)
	rank     float64
	lastUsed *time.Time
	created  time.Time
	hits     int
	conf     float64
	pinned   bool
}

// vecSims asks pgvector for the nearest facts to qv: id → cosine similarity.
// vecSims asks pgvector for the nearest facts to qv, restricted to vectors from the current embedding model
// (or with no recorded model — legacy rows from before this tracking existed). Matching dimensions alone
// does not make two models' vectors comparable, so a bare dimension check is not enough once a fact could
// have been embedded by a different model than the one now configured.
func (s *Service) vecSims(ctx context.Context, bankIDs []int64, history bool, qv []float32, limit int) map[int64]float64 {
	out := map[int64]float64{}
	lit := db.VectorLiteral(qv)
	if lit == "" {
		return out
	}
	model := s.embedModel(ctx)
	rows, err := s.db.Query(ctx, `SELECT id, 1 - (vec <=> $1::vector) FROM memory_facts
		WHERE bank_id=ANY($2) AND ($3 OR valid_to IS NULL) AND vec IS NOT NULL AND vector_dims(vec)=$4 AND (embed_model='' OR embed_model=$6)
		ORDER BY vec <=> $1::vector LIMIT $5`, lit, bankIDs, history, len(qv), limit, model)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sim float64
		if rows.Scan(&id, &sim) == nil {
			out[id] = sim
		}
	}
	return out
}

// candidates is the "floor": the highest-ranked/most-recently-used facts, capped at limit. On its own this
// is a popularity pre-filter (see gatherCandidates for why that alone is not enough); it exists so a vague
// query, or a bank with no strong text/vector match at all, still behaves the way it always has.
func (s *Service) candidates(ctx context.Context, bankIDs []int64, history bool, limit int) ([]cand, error) {
	emb := "embedding"
	if s.VectorOn {
		emb = "NULL::bytea" // similarity is computed by Postgres; don't ship vectors
	}
	sql := `SELECT id,bank_id,text,` + emb + `,rank,last_used,created_at,hits,confidence,embed_model,pinned FROM memory_facts WHERE bank_id=ANY($1)`
	if !history {
		sql += ` AND valid_to IS NULL`
	}
	sql += ` ORDER BY rank DESC, id DESC LIMIT $2`
	rows, err := s.db.Query(ctx, sql, bankIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCands(rows)
}

// candidatesByIDs fetches full candidate rows for an exact id list (the union gatherCandidates assembles).
func (s *Service) candidatesByIDs(ctx context.Context, ids []int64) ([]cand, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	emb := "embedding"
	if s.VectorOn {
		emb = "NULL::bytea"
	}
	rows, err := s.db.Query(ctx, `SELECT id,bank_id,text,`+emb+`,rank,last_used,created_at,hits,confidence,embed_model,pinned FROM memory_facts WHERE id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCands(rows)
}

func scanCands(rows pgx.Rows) ([]cand, error) {
	var out []cand
	for rows.Next() {
		var c cand
		var emb []byte
		var rk, cf float32
		if err := rows.Scan(&c.id, &c.bankID, &c.text, &emb, &rk, &c.lastUsed, &c.created, &c.hits, &cf, &c.model, &c.pinned); err != nil {
			return nil, err
		}
		c.vec, c.rank, c.conf = decodeVec(emb), float64(rk), float64(cf)
		out = append(out, c)
	}
	return out, rows.Err()
}

const (
	candTextLimit = 300 // best matches from Postgres's own text index (tsv)
	candRankFloor = 300 // the "floor" component: highest-ranked/most-used facts regardless of match
	candMaxTotal  = 1200
)

// gatherCandidates builds the candidate pool for near-neighbour search (Store's dedup/correction check) and
// retrieval (Find) from the union of three DB-side sources, instead of one popularity pre-filter capped at a
// fixed size: the nearest by vector (pgSims, already computed by the caller), the best by Postgres's own
// text index, and a smaller "floor" of the highest-ranked/most-recently-used facts. A fact outside the old
// flat top-N-by-rank window could never be found even when it was an excellent vector or text match for the
// query — that was the actual bug; this fixes it by letting relevance (not popularity) decide who is even in
// the running, while the floor keeps existing "browse by rank" behaviour for queries with no strong match.
func (s *Service) gatherCandidates(ctx context.Context, bankIDs []int64, history bool, query string, pgSims map[int64]float64) ([]cand, error) {
	seen := map[int64]bool{}
	order := make([]int64, 0, candMaxTotal)
	add := func(ids []int64) {
		for _, id := range ids {
			if !seen[id] && len(order) < candMaxTotal {
				seen[id] = true
				order = append(order, id)
			}
		}
	}
	if len(pgSims) > 0 {
		vids := make([]int64, 0, len(pgSims))
		for id := range pgSims {
			vids = append(vids, id)
		}
		add(vids)
	}
	add(s.textCandidateIDs(ctx, bankIDs, history, query, candTextLimit))
	floor, err := s.candidates(ctx, bankIDs, history, candRankFloor)
	if err != nil {
		return nil, err
	}
	floorIDs := make([]int64, len(floor))
	for i, c := range floor {
		floorIDs[i] = c.id
	}
	add(floorIDs)
	return s.candidatesByIDs(ctx, order)
}

// textCandidateIDs finds facts via Postgres's own generated tsvector column and its GIN index (memory_facts_tsv_idx)
// — a real DB-side relevance search, rather than downloading every candidate row to rank in Go with the
// in-process textmatch package (which is still used afterwards to SCORE this now-focused set consistently).
func (s *Service) textCandidateIDs(ctx context.Context, bankIDs []int64, history bool, query string, limit int) []int64 {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	sql := `SELECT f.id FROM memory_facts f, plainto_tsquery('simple', $1) q WHERE f.bank_id=ANY($2) AND f.tsv @@ q`
	if !history {
		sql += ` AND f.valid_to IS NULL`
	}
	sql += ` ORDER BY ts_rank(f.tsv, q) DESC LIMIT $3`
	rows, err := s.db.Query(ctx, sql, q, bankIDs, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// normFactText normalizes a fact's text for exact-duplicate comparison (case and whitespace only — this is
// deliberately NOT fuzzy; anything more forgiving belongs to the same/update/contradiction classifier).
func normFactText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func jaccard(a, b string) float64 {
	ta, tb := textmatch.Tokens(a), textmatch.Tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, t := range ta {
		set[t] = true
	}
	inter := 0
	seen := map[string]bool{}
	for _, t := range tb {
		if set[t] && !seen[t] {
			inter++
			seen[t] = true
		}
	}
	union := len(set)
	for _, t := range tb {
		if !set[t] {
			union++
			set[t] = true
		}
	}
	return float64(inter) / float64(union)
}

// ── retrieval ───────────────────────────────────────────────────────────────

type FindReq struct {
	Query   string
	Banks   []string // specs; empty → caller-provided defaults are expected
	Agent   string
	K       int
	History bool // include superseded facts
	MinRel  float64
	NoLinks bool // do not follow links to related facts
	Deep    bool // rerank the best candidates with the fast model (slower, more precise)
}

// effRank applies time decay to a fact's rank: unused facts fade slowly (half-life 120 days). A pinned fact
// is exempt entirely — the user's own standing say that it matters, not something usage should erode.
func effRank(rank float64, last *time.Time, created time.Time, pinned bool) float64 {
	if pinned {
		return math.Max(0.3, rank)
	}
	ref := created
	if last != nil {
		ref = *last
	}
	days := time.Since(ref).Hours() / 24
	return math.Max(0.3, rank*math.Pow(0.5, days/120))
}

// Find retrieves the best facts across banks: cosine similarity when embeddings
// are available (BM25 otherwise), weighted by decayed usage rank.
// scoredIdx is a candidate (index into the candidate list) with its final score.
type scoredIdx struct {
	i     int
	score float64
}

func (s *Service) Find(ctx context.Context, r FindReq) ([]Fact, error) {
	if r.K <= 0 || r.K > 50 {
		r.K = 8
	}
	if r.MinRel == 0 {
		r.MinRel = 0.28
	}
	var ids []int64
	for _, spec := range r.Banks {
		b, err := s.BankBySpec(ctx, spec, r.Agent, false)
		if err != nil {
			continue // unknown banks are simply empty
		}
		ids = append(ids, b.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var qv []float32
	if s.llm.HasEmbedding(ctx) {
		ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if v, err := s.llm.Embed(ectx, "", []string{r.Query}); err == nil && len(v) > 0 {
			qv = normalize(v[0])
		}
		cancel()
	}
	var pgSims map[int64]float64
	if s.VectorOn && qv != nil {
		pgSims = s.vecSims(ctx, ids, r.History, qv, 300)
	}
	// candidates come from relevance (vector-nearest ∪ text-index-nearest), not a popularity pre-filter — a
	// fact outside the old top-N-by-rank window could never be found even as a strong match (see gatherCandidates).
	cands, err := s.gatherCandidates(ctx, ids, r.History, r.Query, pgSims)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	rel := make([]float64, len(cands))
	// lexical relevance for facts without vectors (or all, when no query vector) — scored in-process on this
	// now DB-narrowed candidate set, same as before; only which facts get here has changed.
	docs := make([]string, len(cands))
	for i, c := range cands {
		docs[i] = c.text
	}
	lex := map[int]float64{}
	if hits := textmatch.Rank(r.Query, docs, 0); len(hits) > 0 {
		top := hits[0].Score
		for _, h := range hits {
			lex[h.Index] = h.Score / top
		}
	}
	sem := make([]float64, len(cands)) // -1: no semantic signal for this fact
	for i, c := range cands {
		sem[i] = -1
		switch {
		case pgSims != nil:
			if v, ok := pgSims[c.id]; ok {
				sem[i] = v
				rel[i] = math.Max(v, 0.6*lex[i])
			} else {
				rel[i] = 0.75 * lex[i]
			}
		case qv != nil && c.vec != nil:
			sem[i] = Dot(qv, c.vec)
			rel[i] = math.Max(sem[i], 0.6*lex[i])
		default:
			rel[i] = 0.75 * lex[i]
		}
	}
	// Reciprocal rank fusion of the semantic and the keyword ranking among the facts that pass the relevance bar:
	// a fact that both signals like beats one that only a single signal loves, whatever their raw scales.
	fused := fuseRanks(rel, sem, lex, r.MinRel)
	var sc []scoredIdx
	for i, c := range cands {
		if rel[i] < r.MinRel {
			continue
		}
		w := 0.8 + 0.2*math.Min(effRank(c.rank, c.lastUsed, c.created, c.pinned), 3)
		w *= 0.85 + 0.15*c.conf/0.7
		sc = append(sc, scoredIdx{i, (0.55*rel[i] + 0.45*fused[i]) * w})
	}
	sort.Slice(sc, func(a, b int) bool { return sc[a].score > sc[b].score })
	if r.Deep && len(sc) > 1 { // let the model reorder the best few by real usefulness for the query
		sc = s.rerank(ctx, r.Query, cands, sc, r.K)
	}
	if len(sc) > r.K {
		sc = sc[:r.K]
	}
	if len(sc) == 0 {
		return nil, nil
	}
	pick := make([]int64, len(sc))
	scores := map[int64]float64{}
	for i, x := range sc {
		pick[i] = cands[x.i].id
		scores[pick[i]] = x.score
	}
	var linked []Fact
	if !r.History {
		_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET hits=hits+1, last_used=now(), rank=LEAST(rank+0.05,5) WHERE id=ANY($1)`, pick)
		s.strengthen(ctx, pick)
		if !r.NoLinks {
			linked = s.expand(ctx, scores, ids, min(maxExpansion, max(1, r.K/2)))
		}
	}
	rows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE f.id=ANY($1)`, pick)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Fact{}
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		byID[f.ID] = f
	}
	var out []Fact
	for _, x := range sc {
		f := byID[cands[x.i].id]
		f.Score = math.Round(x.score*1000) / 1000
		out = append(out, f)
	}
	return append(out, linked...), rows.Err()
}

// Feedback nudges a fact's rank: useful facts are surfaced more, wrong ones fade.
func (s *Service) Feedback(ctx context.Context, id int64, useful bool) error {
	d := 0.4
	if !useful {
		d = -0.6
	}
	_, err := s.db.Exec(ctx, `UPDATE memory_facts SET rank=GREATEST(0.05,LEAST(rank+$2,5)), last_used=now() WHERE id=$1`, id, float32(d))
	return err
}

// Reindex re-embeds every fact (after switching embedding models).
func (s *Service) Reindex(ctx context.Context) (int, error) {
	if !s.llm.HasEmbedding(ctx) {
		return 0, errors.New("no embedding model configured")
	}
	rows, err := s.db.Query(ctx, `SELECT id,text FROM memory_facts ORDER BY id`)
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
		if err := rows.Scan(&x.id, &x.text); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, x)
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
