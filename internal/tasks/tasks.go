// Package tasks is the persistent message queue between users and agents and
// between agents. A task is a unit of work with a sender, a recipient agent and
// a status; multi-turn tasks keep their agent session between turns.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"prism/internal/textutil"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Queued       = "queued"
	Running      = "running"
	WaitingInput = "waiting_input"
	Done         = "done"
	// Partial: the run ended without a real answer (iteration budget exhausted, stopped by the loop guard) —
	// distinct from Done so it is never mistaken for a genuine, complete result. Resumable, like WaitingInput.
	Partial   = "partial"
	Failed    = "failed"
	Cancelled = "cancelled"
)

type Task struct {
	ID         int64      `json:"id"`
	ParentID   *int64     `json:"parent_id,omitempty"`
	RootID     int64      `json:"root_id"`
	FromKind   string     `json:"from_kind"` // user | agent | cron | intent | telegram | system
	FromName   string     `json:"from_name"`
	ToAgent    string     `json:"to_agent"`
	Title      string     `json:"title"`
	Input      string     `json:"input"`
	Status     string     `json:"status"`
	Result     string     `json:"result"`
	Error      string     `json:"error"`
	Question   string     `json:"question"`
	Depth      int        `json:"depth"`
	Priority   int        `json:"priority"`
	SessionID  *int64     `json:"session_id,omitempty"`
	TokensIn   int64      `json:"tokens_in"`
	TokensOut  int64      `json:"tokens_out"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Restarts counts how many times this task was requeued because PRISM stopped while it was running (see
	// RequeueRunning). A resumed run with restarts > 0 gets a reconciliation notice instead of blindly
	// repeating whatever it was doing when the process stopped.
	Restarts int `json:"restarts"`
	// AcknowledgedAt is set once the user has dealt with (or dismissed) a partial/failed task, which removes it
	// from Today's "needs your attention".
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	// NotifyTopic: for a cron- or intent-fired task, the Telegram topic its "reports back to the user" notice
	// (internal/agent/orchestrator.go) should use — set by the firing cron/intent (see scheduler/topics.go).
	NotifyTopic string `json:"notify_topic,omitempty"`
}

func (t Task) Terminal() bool { return t.Status == Done || t.Status == Failed || t.Status == Cancelled }

// Rerunnable reports whether a cancelled task may be started again from scratch. Restricted to the user's
// own direct requests to Atlas (FromKind "user", straight to the fixed entry-agent name — see
// internal/agent/seed.go, "Atlas has a fixed name") — never a delegated sub-task (someone else's to
// restart) and never a task another agent or the scheduler started on the user's behalf.
func (t Task) Rerunnable() bool {
	return t.Status == Cancelled && t.FromKind == "user" && t.ToAgent == "Atlas"
}

type Store struct {
	db       *pgxpool.Pool
	mu       sync.Mutex
	waiters  map[int64][]chan struct{}
	OnChange func(Task)
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db, waiters: map[int64][]chan struct{}{}}
}

const cols = `id,parent_id,root_id,from_kind,from_name,to_agent,title,input,status,result,error,question,depth,priority,session_id,tokens_in,tokens_out,created_at,started_at,finished_at,restarts,acknowledged_at,notify_topic`

func scan(r pgx.Row) (Task, error) {
	var t Task
	err := r.Scan(&t.ID, &t.ParentID, &t.RootID, &t.FromKind, &t.FromName, &t.ToAgent, &t.Title, &t.Input, &t.Status, &t.Result, &t.Error,
		&t.Question, &t.Depth, &t.Priority, &t.SessionID, &t.TokensIn, &t.TokensOut, &t.CreatedAt, &t.StartedAt, &t.FinishedAt, &t.Restarts, &t.AcknowledgedAt, &t.NotifyTopic)
	return t, err
}

func (s *Store) notify(t Task) {
	if s.OnChange != nil {
		s.OnChange(t)
	}
}

// Create inserts a task. When running is true the task starts in the running
// state (claimed by the caller); otherwise it is queued for the dispatcher.
func (s *Store) Create(ctx context.Context, t Task, running bool) (Task, error) {
	status := Queued
	if running {
		status = Running
	}
	t.Title, t.Input = textutil.Clean(t.Title), textutil.Clean(t.Input)
	if t.Title == "" {
		t.Title = firstLine(t.Input, 80)
	}
	row := s.db.QueryRow(ctx, `INSERT INTO tasks(parent_id,root_id,from_kind,from_name,to_agent,title,input,status,depth,priority,session_id,started_at,notify_topic)
		VALUES($1,COALESCE($2,0),$3,$4,$5,$6,$7,$8,$9,$10,$11, CASE WHEN $8='running' THEN now() END, $12) RETURNING `+cols,
		t.ParentID, nilIfZero(t.RootID), t.FromKind, t.FromName, t.ToAgent, t.Title, t.Input, status, t.Depth, t.Priority, t.SessionID, t.NotifyTopic)
	out, err := scan(row)
	if err != nil {
		return out, err
	}
	if out.RootID == 0 { // root of its own tree
		out.RootID = out.ID
		_, _ = s.db.Exec(ctx, `UPDATE tasks SET root_id=id WHERE id=$1`, out.ID)
	}
	s.notify(out)
	return out, nil
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

func (s *Store) Get(ctx context.Context, id int64) (Task, error) {
	t, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM tasks WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) { // a bare "no rows in result set" tells an agent nothing about what to fix
		return t, fmt.Errorf("task #%d does not exist: %w", id, err)
	}
	return t, err
}

type Filter struct {
	Status string
	Agent  string
	RootID int64
	Limit  int
}

func (s *Store) List(ctx context.Context, f Filter) ([]Task, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 2000 {
		f.Limit = 2000
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM tasks
		WHERE ($1='' OR status=$1) AND ($2='' OR to_agent=$2) AND ($3=0 OR root_id=$3)
		ORDER BY id DESC LIMIT $4`, f.Status, f.Agent, f.RootID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClaimNext atomically moves the highest-priority queued task to running.
func (s *Store) ClaimNext(ctx context.Context) (*Task, error) {
	row := s.db.QueryRow(ctx, `UPDATE tasks SET status='running', started_at=COALESCE(started_at,now())
		WHERE id=(SELECT id FROM tasks WHERE status='queued' ORDER BY priority DESC, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING `+cols)
	t, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.notify(t)
	return &t, nil
}

// Resume moves a waiting_input/partial/done task back to running (multi-turn continuation, or picking a
// partial run back up).
func (s *Store) Resume(ctx context.Context, id int64) (Task, error) {
	t, err := scan(s.db.QueryRow(ctx, `UPDATE tasks SET status='running', question='', finished_at=NULL WHERE id=$1 AND status IN ('waiting_input','partial','done') RETURNING `+cols, id))
	if err == nil {
		s.notify(t)
	}
	return t, err
}

// Acknowledge marks a finished task as seen/dealt with. Only terminal or partial tasks can be acknowledged.
func (s *Store) Acknowledge(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE tasks SET acknowledged_at=now() WHERE id=$1 AND status IN ('partial','failed','done','cancelled')`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("only a finished task can be acknowledged")
	}
	if err == nil {
		if t, gerr := s.Get(ctx, id); gerr == nil {
			s.notify(t)
		}
	}
	return err
}

func (s *Store) SetSession(ctx context.Context, id, session int64) error {
	_, err := s.db.Exec(ctx, `UPDATE tasks SET session_id=$2 WHERE id=$1`, id, session)
	return err
}

func (s *Store) AddTokens(ctx context.Context, id int64, in, out int) {
	_, _ = s.db.Exec(ctx, `UPDATE tasks SET tokens_in=tokens_in+$2, tokens_out=tokens_out+$3 WHERE id=$1`, id, in, out)
}

// SetBreakdown stores the latest prompt-token breakdown of the run working on the task.
func (s *Store) SetBreakdown(ctx context.Context, id int64, b map[string]int) {
	if raw, err := json.Marshal(b); err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE tasks SET ctx_breakdown=$2 WHERE id=$1`, id, raw)
	}
}

// Breakdown returns what SetBreakdown stored (nil if none).
func (s *Store) Breakdown(ctx context.Context, id int64) map[string]int {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT ctx_breakdown FROM tasks WHERE id=$1`, id).Scan(&raw); err != nil || len(raw) == 0 {
		return nil
	}
	var b map[string]int
	_ = json.Unmarshal(raw, &b)
	return b
}

// Finish records the outcome and wakes waiters.
func (s *Store) Finish(ctx context.Context, id int64, status, result, errMsg, question string) error {
	result, errMsg, question = textutil.Clean(result), textutil.Clean(errMsg), textutil.Clean(question)
	var fin any
	if status == Done || status == Failed || status == Cancelled {
		fin = time.Now()
	}
	t, err := scan(s.db.QueryRow(ctx, `UPDATE tasks SET status=$2,result=$3,error=$4,question=$5,finished_at=$6 WHERE id=$1 RETURNING `+cols,
		id, status, result, errMsg, question, fin))
	if err != nil {
		return err
	}
	s.notify(t)
	s.mu.Lock()
	ws := s.waiters[id]
	delete(s.waiters, id)
	s.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
	if fin != nil {
		s.closeWaitingChildren(ctx, id) // a requester that is done will never answer its colleagues' questions
	}
	return nil
}

// closeWaitingChildren cancels the sub-tasks of parent that are still waiting for input. Their question went to the parent
// as its tool result; once the parent has finished nobody is left to answer, and they would sit in "Waiting for your answer"
// for ever. parent < 0 means every task whose parent is already finished (a start-up sweep).
func (s *Store) closeWaitingChildren(ctx context.Context, parent int64) {
	q := `UPDATE tasks c SET status='cancelled', error='the requester finished without answering', finished_at=now()
	      WHERE c.status='waiting_input' AND c.parent_id IS NOT NULL AND `
	var args []any
	if parent >= 0 {
		q += `c.parent_id=$1`
		args = append(args, parent)
	} else {
		q += `EXISTS (SELECT 1 FROM tasks p WHERE p.id=c.parent_id AND p.status IN ('done','failed','cancelled'))`
	}
	rows, err := s.db.Query(ctx, q+` RETURNING c.id`, args...)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if t, err := s.Get(ctx, id); err == nil {
			s.notify(t)
		}
	}
}

// CloseOrphans runs at start-up: waiting_input sub-tasks whose requester already finished are cancelled.
func (s *Store) CloseOrphans(ctx context.Context) { s.closeWaitingChildren(ctx, -1) }

// Cancel marks a queued/running/waiting task cancelled (a running one also gets its context cancelled by the orchestrator).
func (s *Store) Cancel(ctx context.Context, id int64) error {
	return s.Finish(ctx, id, Cancelled, "", "cancelled by user", "")
}

// Wait blocks until the task leaves running/queued (done, failed, cancelled or waiting_input).
func (s *Store) Wait(ctx context.Context, id int64) (Task, error) {
	for {
		t, err := s.Get(ctx, id)
		if err != nil {
			return t, err
		}
		if t.Status != Running && t.Status != Queued {
			return t, nil
		}
		ch := make(chan struct{})
		s.mu.Lock()
		s.waiters[id] = append(s.waiters[id], ch)
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(5 * time.Second): // safety net against missed wakeups
		}
	}
}

// RequeueRunning puts tasks that were running when the process stopped back in the queue, so a restart
// never loses work, and bumps restarts so a resumed run can tell it may have already taken side-effecting
// actions and must reconcile rather than blindly repeat them (see Engine.reconcileNotice). Call
// ReconcilePendingAsks first: a task that was blocked on a persisted ask is handled there instead, as
// waiting_input with the question it was actually asking, not silently restarted from its original input.
func (s *Store) RequeueRunning(ctx context.Context) (int, error) {
	tag, err := s.db.Exec(ctx, `UPDATE tasks SET status='queued', restarts=restarts+1 WHERE status='running'`)
	return int(tag.RowsAffected()), err
}

// CancelOrphanedRunning runs at start-up, before RequeueRunning: a sub-task that was running when the process died, whose
// requester is not running either, has nobody waiting for its result (the requester's tool call died with the process), so
// re-running it would only burn model calls — and, for colleagues asking each other, restart their ping-pong.
func (s *Store) CancelOrphanedRunning(ctx context.Context) (int, error) {
	tag, err := s.db.Exec(ctx, `UPDATE tasks c SET status='cancelled', error='interrupted by a restart; its requester is no longer waiting', finished_at=now()
		WHERE c.status IN ('running','queued') AND c.parent_id IS NOT NULL AND c.from_kind='agent'
		AND NOT EXISTS (SELECT 1 FROM tasks p WHERE p.id=c.parent_id AND p.status IN ('running','queued'))`)
	return int(tag.RowsAffected()), err
}

// maxSummaryAttempts caps retries of the task-summary pipeline (see internal/tasksum and Engine.SummarizeTask)
// so a task whose transcript permanently fails to summarize (an unparsable model output, say) cannot block
// the queue forever — the same discipline memory's raw-fact pipeline applies (see maxRawAttempts).
const maxSummaryAttempts = 3

// NeedsSummary lists terminal tasks with a recorded session that have not yet been distilled into a
// task_summaries row (or given up on after maxSummaryAttempts), oldest first.
func (s *Store) NeedsSummary(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM tasks
		WHERE summarized_at IS NULL AND finished_at IS NOT NULL AND session_id IS NOT NULL
		AND status IN ('done','failed') AND summary_attempts < $2
		ORDER BY id LIMIT $1`, limit, maxSummaryAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkSummarized records that a task's summarization is resolved — stored, skipped as trivial (no tool
// calls worth distilling), or given up on — so NeedsSummary stops returning it.
func (s *Store) MarkSummarized(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE tasks SET summarized_at=now() WHERE id=$1`, id)
	return err
}

// BumpSummaryAttempts records a failed summarization attempt; after maxSummaryAttempts NeedsSummary stops
// offering the task again.
func (s *Store) BumpSummaryAttempts(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE tasks SET summary_attempts=summary_attempts+1 WHERE id=$1`, id)
	return err
}

// Prune deletes finished tasks older than d (root tasks cascade via parent link being SET NULL).
func (s *Store) Prune(ctx context.Context, d time.Duration) (int, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM tasks WHERE finished_at IS NOT NULL AND finished_at < $1`, time.Now().Add(-d))
	return int(tag.RowsAffected()), err
}

type Counts struct {
	Queued, Running, Waiting int
}

func (s *Store) Counts(ctx context.Context) (c Counts) {
	_ = s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='queued'), count(*) FILTER (WHERE status='running'), count(*) FILTER (WHERE status='waiting_input') FROM tasks`).
		Scan(&c.Queued, &c.Running, &c.Waiting)
	return
}
