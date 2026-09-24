package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Merges and splits are recorded so they can be undone: the log keeps what moved from where (and the duplicates a
// merge collapsed), which is all that is needed to put the banks back as they were.
type opBank struct {
	Bank
	ReflectedAt *time.Time `json:"reflected_at,omitempty"`
}

type droppedFact struct {
	Bank int64           `json:"bank"`
	Row  json.RawMessage `json:"row"`
}

type mergeOp struct {
	IntoID          int64           `json:"into_id"`
	IntoCreated     bool            `json:"into_created"`
	IntoDescription string          `json:"into_description"`
	Sources         []opBank        `json:"sources"`
	Origin          map[int64]int64 `json:"origin"` // fact id → the bank it came from
	Dropped         []droppedFact   `json:"dropped,omitempty"`
}

type splitOp struct {
	From      int64   `json:"from"`
	To        int64   `json:"to"`
	ToCreated bool    `json:"to_created"`
	Moved     []int64 `json:"moved"`
}

// Op is an entry of the undo log.
type Op struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	Summary   string     `json:"summary"`
	CreatedAt time.Time  `json:"created_at"`
	UndoneAt  *time.Time `json:"undone_at,omitempty"`
}

func (s *Service) logOp(ctx context.Context, kind, summary string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO memory_ops(kind,summary,payload) VALUES($1,$2,$3)`, kind, summary, b)
}

// Ops lists the recent operations that can still be undone, newest first.
func (s *Service) Ops(ctx context.Context) ([]Op, error) {
	rows, err := s.db.Query(ctx, `SELECT id,kind,summary,created_at,undone_at FROM memory_ops
		WHERE undone_at IS NULL AND created_at > now()-interval '30 days' ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Op
	for rows.Next() {
		var o Op
		if rows.Scan(&o.ID, &o.Kind, &o.Summary, &o.CreatedAt, &o.UndoneAt) == nil {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

// Undo reverses a merge or split. Facts that were deleted, or moved elsewhere since, are left alone.
func (s *Service) Undo(ctx context.Context, id int64) (string, error) {
	var kind, summary string
	var payload []byte
	var undone *time.Time
	if err := s.db.QueryRow(ctx, `SELECT kind,summary,payload,undone_at FROM memory_ops WHERE id=$1`, id).Scan(&kind, &summary, &payload, &undone); err != nil {
		return "", errors.New("that operation is not in the log")
	}
	if undone != nil {
		return "", errors.New("already undone")
	}
	var msg string
	var err error
	switch kind {
	case "merge":
		var op mergeOp
		if err = json.Unmarshal(payload, &op); err == nil {
			msg, err = s.undoMerge(ctx, op)
		}
	case "split":
		var op splitOp
		if err = json.Unmarshal(payload, &op); err == nil {
			msg, err = s.undoSplit(ctx, op)
		}
	default:
		err = fmt.Errorf("unknown operation %q", kind)
	}
	if err != nil {
		return "", err
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_ops SET undone_at=now() WHERE id=$1`, id)
	s.changed()
	return msg, nil
}

func (s *Service) undoSplit(ctx context.Context, op splitOp) (string, error) {
	if _, err := s.bankByID(ctx, op.From); err != nil {
		return "", errors.New("the original bank no longer exists")
	}
	tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET bank_id=$1 WHERE bank_id=$2 AND id=ANY($3)`, op.From, op.To, op.Moved)
	if err != nil {
		return "", err
	}
	if op.ToCreated {
		_, _ = s.db.Exec(ctx, `DELETE FROM memory_banks WHERE id=$1 AND kind IN ('project','domain')
			AND NOT EXISTS (SELECT 1 FROM memory_facts WHERE bank_id=$1)`, op.To)
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_banks SET reflected_at=NULL WHERE id=$1`, op.From)
	return fmt.Sprintf("%d facts moved back", tag.RowsAffected()), nil
}

func (s *Service) undoMerge(ctx context.Context, op mergeOp) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	for _, b := range op.Sources { // the emptied banks come back with their old ids
		var clash bool
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_banks WHERE id=$1 OR (kind=$2 AND name=$3 AND owner=$4))`, b.ID, b.Kind, b.Name, b.Owner).Scan(&clash)
		if clash {
			return "", fmt.Errorf("cannot undo: a bank %s:%s exists again", b.Kind, b.Name)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memory_banks(id,kind,name,owner,description,status,created_at,reflected_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			b.ID, b.Kind, b.Name, b.Owner, b.Description, b.Status, b.CreatedAt, b.ReflectedAt); err != nil {
			return "", err
		}
	}
	moved := 0
	for fid, bid := range op.Origin {
		tag, err := tx.Exec(ctx, `UPDATE memory_facts SET bank_id=$1 WHERE id=$2 AND bank_id=$3`, bid, fid, op.IntoID)
		if err != nil {
			return "", err
		}
		moved += int(tag.RowsAffected())
	}
	restored := 0
	for _, d := range op.Dropped { // duplicates the merge collapsed come back (without their links)
		tag, err := tx.Exec(ctx, `INSERT INTO memory_facts(id,bank_id,text,tags,rank,hits,confidence,source,kind,origins,valid_from,valid_to,created_at,last_used)
			SELECT id,$2,text,tags,rank,hits,confidence,source,kind,origins,valid_from,valid_to,created_at,last_used
			FROM jsonb_populate_record(NULL::memory_facts, $1::jsonb) ON CONFLICT (id) DO NOTHING`, []byte(d.Row), d.Bank)
		if err != nil {
			return "", err
		}
		restored += int(tag.RowsAffected())
	}
	if op.IntoCreated {
		_, _ = tx.Exec(ctx, `DELETE FROM memory_banks WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM memory_facts WHERE bank_id=$1)`, op.IntoID)
	} else {
		_, _ = tx.Exec(ctx, `UPDATE memory_banks SET description=$2, reflected_at=NULL WHERE id=$1`, op.IntoID, op.IntoDescription)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if restored > 0 {
		_, _ = s.EmbedMissing(ctx)
	}
	return fmt.Sprintf("%d facts moved back into %d banks (%d duplicates restored)", moved, len(op.Sources), restored), nil
}
