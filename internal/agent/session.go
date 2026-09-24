package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/llm"
)

// Msg is a session message with provenance metadata.
type Msg struct {
	llm.Message
	ID         int64
	Provenance string // user | agent | tool | web | mcp | memory | system
	Tainted    bool
	ImageIDs   []int64 // pictures attached to a user message (artifact ids); loaded when the message is sent to the model
}

type Session struct {
	ID         int64
	Agent      string
	Kind       string
	Key        string
	TaskID     int64
	Scratchpad string
	TokensIn   int64
	TokensOut  int64
}

type SessionStore struct{ db *pgxpool.Pool }

// imgs keeps a nil slice from being stored as NULL in the NOT NULL images column.
func imgs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func NewSessionStore(db *pgxpool.Pool) *SessionStore { return &SessionStore{db: db} }

func (s *SessionStore) Create(ctx context.Context, agent, kind, key string, taskID int64) (*Session, error) {
	var tid *int64
	if taskID != 0 {
		tid = &taskID
	}
	sess := &Session{Agent: agent, Kind: kind, Key: key, TaskID: taskID}
	err := s.db.QueryRow(ctx, `INSERT INTO sessions(agent,kind,key,task_id,status) VALUES($1,$2,$3,$4,'idle') RETURNING id`, agent, kind, key, tid).Scan(&sess.ID)
	return sess, err
}

// Chat returns the persistent chat session for (agent,key), creating it on first use.
func (s *SessionStore) Chat(ctx context.Context, agent, key string) (*Session, error) {
	sess, err := s.Get(ctx, 0, agent, "chat", key)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.Create(ctx, agent, "chat", key, 0)
	}
	return sess, err
}

func (s *SessionStore) Get(ctx context.Context, id int64, agent, kind, key string) (*Session, error) {
	var sess Session
	var tid *int64
	var q string
	var args []any
	if id != 0 {
		q, args = `SELECT id,agent,kind,key,task_id,scratchpad,tokens_in,tokens_out FROM sessions WHERE id=$1`, []any{id}
	} else {
		q, args = `SELECT id,agent,kind,key,task_id,scratchpad,tokens_in,tokens_out FROM sessions WHERE agent=$1 AND kind=$2 AND key=$3 ORDER BY id DESC LIMIT 1`, []any{agent, kind, key}
	}
	err := s.db.QueryRow(ctx, q, args...).Scan(&sess.ID, &sess.Agent, &sess.Kind, &sess.Key, &tid, &sess.Scratchpad, &sess.TokensIn, &sess.TokensOut)
	if tid != nil {
		sess.TaskID = *tid
	}
	return &sess, err
}

func (s *SessionStore) Messages(ctx context.Context, sessionID int64) ([]Msg, error) {
	rows, err := s.db.Query(ctx, `SELECT id,role,content,tool_calls,tool_call_id,name,provenance,tainted,images FROM session_messages WHERE session_id=$1 ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Msg
	for rows.Next() {
		var m Msg
		var tc []byte
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &tc, &m.ToolCallID, &m.Name, &m.Provenance, &m.Tainted, &m.ImageIDs); err != nil {
			return nil, err
		}
		if len(tc) > 0 {
			_ = json.Unmarshal(tc, &m.ToolCalls)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Append persists a message and returns its id.
func (s *SessionStore) Append(ctx context.Context, sessionID int64, m Msg) (int64, error) {
	var tc []byte
	if len(m.ToolCalls) > 0 {
		tc, _ = json.Marshal(m.ToolCalls)
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO session_messages(session_id,role,content,tool_calls,tool_call_id,name,provenance,tainted,images)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, sessionID, m.Role, m.Content, tc, m.ToolCallID, m.Name, m.Provenance, m.Tainted, imgs(m.ImageIDs)).Scan(&id)
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE sessions SET updated_at=now() WHERE id=$1`, sessionID)
	}
	return id, err
}

// Replace atomically rewrites a session's messages (used by compaction).
func (s *SessionStore) Replace(ctx context.Context, sessionID int64, ms []Msg) ([]Msg, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM session_messages WHERE session_id=$1`, sessionID); err != nil {
		return nil, err
	}
	out := make([]Msg, len(ms))
	for i, m := range ms {
		var tc []byte
		if len(m.ToolCalls) > 0 {
			tc, _ = json.Marshal(m.ToolCalls)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO session_messages(session_id,role,content,tool_calls,tool_call_id,name,provenance,tainted,images)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, sessionID, m.Role, m.Content, tc, m.ToolCallID, m.Name, m.Provenance, m.Tainted, imgs(m.ImageIDs)).Scan(&m.ID); err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, tx.Commit(ctx)
}

func (s *SessionStore) Clear(ctx context.Context, sessionID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM session_messages WHERE session_id=$1`, sessionID)
	return err
}

func (s *SessionStore) SetScratch(ctx context.Context, sessionID int64, text string) error {
	_, err := s.db.Exec(ctx, `UPDATE sessions SET scratchpad=$2 WHERE id=$1`, sessionID, text)
	return err
}

func (s *SessionStore) Scratch(ctx context.Context, sessionID int64) string {
	var t string
	_ = s.db.QueryRow(ctx, `SELECT scratchpad FROM sessions WHERE id=$1`, sessionID).Scan(&t)
	return t
}

func (s *SessionStore) AddTokens(ctx context.Context, sessionID int64, in, out int) {
	_, _ = s.db.Exec(ctx, `UPDATE sessions SET tokens_in=tokens_in+$2, tokens_out=tokens_out+$3 WHERE id=$1`, sessionID, in, out)
}
