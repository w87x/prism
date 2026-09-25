package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Chats are the user's web conversations. They share one memory (user, domain and agent banks); a chat may be
// focused on a project bank, which is searched first and receives the facts about the work by default. The main
// chat has topic "".
type Chat struct {
	ID               int64     `json:"id"`
	Topic            string    `json:"topic"`
	Title            string    `json:"title"`
	ProjectBankID    *int64    `json:"project_bank_id"`
	Project          string    `json:"project"` // bank label ("project:Trip"), "" when unbound
	SuggestedProject string    `json:"suggested_project"`
	SuggestedNew     bool      `json:"suggested_new"`
	Remember         bool      `json:"remember"`
	Archived         bool      `json:"archived"`
	CreatedAt        time.Time `json:"created_at"`
	LastAt           time.Time `json:"last_at"`
	LastMsgID        int64     `json:"last_msg_id"`
	LastText         string    `json:"last_text"`
	Busy             bool      `json:"busy"`
	MergedInto       string    `json:"merged_into"` // topic of the chat this one was merged into (archived, can be undone)
	MergedTitle      string    `json:"merged_title"`
}

// chatCols are the columns scanChat reads.
const chatCols = `c.id,c.topic,c.title,c.project_bank_id,COALESCE(b.kind||CASE WHEN b.kind='user' THEN '' ELSE ':'||b.name END,''),c.suggested_project,c.suggested_new,c.remember,c.archived,c.created_at,c.last_at,c.merged_into`

func scanChat(r interface{ Scan(...any) error }) (Chat, error) {
	var c Chat
	err := r.Scan(&c.ID, &c.Topic, &c.Title, &c.ProjectBankID, &c.Project, &c.SuggestedProject, &c.SuggestedNew, &c.Remember, &c.Archived, &c.CreatedAt, &c.LastAt, &c.MergedInto)
	return c, err
}

// Chats lists the web chats, main first, then by recent activity.
func (e *Engine) Chats(ctx context.Context, archived bool) ([]Chat, error) {
	rows, err := e.DB.Query(ctx, `SELECT `+chatCols+`,
			COALESCE((SELECT max(id) FROM chat_messages m WHERE m.channel='web' AND m.topic=c.topic),0),
			COALESCE((SELECT left(text,120) FROM chat_messages m WHERE m.channel='web' AND m.topic=c.topic AND m.role IN ('user','agent') ORDER BY id DESC LIMIT 1),''),
			COALESCE((SELECT title FROM chats t WHERE t.topic=c.merged_into AND c.merged_into<>''),'')
		FROM chats c LEFT JOIN memory_banks b ON b.id=c.project_bank_id
		WHERE c.archived=$1 OR c.topic='' ORDER BY (c.topic='') DESC, c.last_at DESC`, archived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Topic, &c.Title, &c.ProjectBankID, &c.Project, &c.SuggestedProject, &c.SuggestedNew, &c.Remember, &c.Archived, &c.CreatedAt, &c.LastAt, &c.MergedInto, &c.LastMsgID, &c.LastText, &c.MergedTitle); err != nil {
			return nil, err
		}
		c.Busy = e.ChatBusy(ChatKey("web", c.Topic))
		out = append(out, c)
	}
	return out, rows.Err()
}

func (e *Engine) GetChat(ctx context.Context, topic string) (Chat, error) {
	return scanChat(e.DB.QueryRow(ctx, `SELECT `+chatCols+` FROM chats c LEFT JOIN memory_banks b ON b.id=c.project_bank_id WHERE c.topic=$1`, topic))
}

// CreateChat opens a new web chat, optionally focused on a project bank.
func (e *Engine) CreateChat(ctx context.Context, title string, projectBankID int64) (Chat, error) {
	title = strings.TrimSpace(title)
	var pb any
	if projectBankID != 0 {
		pb = projectBankID
	}
	var id int64
	if err := e.DB.QueryRow(ctx, `INSERT INTO chats(topic,title,auto_title,project_bank_id) VALUES('tmp-'||md5(random()::text), $1, $2, $3) RETURNING id`, title, title == "", pb).Scan(&id); err != nil {
		return Chat{}, err
	}
	topic := fmt.Sprintf("c%d", id)
	if _, err := e.DB.Exec(ctx, `UPDATE chats SET topic=$2 WHERE id=$1`, id, topic); err != nil {
		return Chat{}, err
	}
	if projectBankID != 0 {
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET suggestion_done=true WHERE id=$1`, id)
	}
	e.Emit("chats.update", map[string]any{"topic": topic})
	return e.GetChat(ctx, topic)
}

// ChatPatch changes a chat; nil fields stay as they are. ProjectBankID 0 unbinds.
type ChatPatch struct {
	Title          *string `json:"title"`
	ProjectBankID  *int64  `json:"project_bank_id"`
	Remember       *bool   `json:"remember"`
	Archived       *bool   `json:"archived"`
	NewProject     *string `json:"new_project"`       // create a project bank with this name and bind the chat to it
	AcceptSuggest  bool    `json:"accept_suggestion"` // bind to the suggested project (creating it when new)
	DismissSuggest bool    `json:"dismiss_suggestion"`
}

func (e *Engine) UpdateChat(ctx context.Context, id int64, p ChatPatch) (Chat, error) {
	var topic string
	if err := e.DB.QueryRow(ctx, `SELECT topic FROM chats WHERE id=$1`, id).Scan(&topic); err != nil {
		return Chat{}, errors.New("chat not found")
	}
	if p.Title != nil {
		if _, err := e.DB.Exec(ctx, `UPDATE chats SET title=$2, auto_title=false WHERE id=$1`, id, strings.TrimSpace(*p.Title)); err != nil {
			return Chat{}, err
		}
	}
	if p.ProjectBankID != nil {
		var pb any
		if *p.ProjectBankID != 0 {
			pb = *p.ProjectBankID
		}
		if _, err := e.DB.Exec(ctx, `UPDATE chats SET project_bank_id=$2, suggested_project='', suggestion_done=true WHERE id=$1`, id, pb); err != nil {
			return Chat{}, err
		}
	}
	if p.Remember != nil {
		if _, err := e.DB.Exec(ctx, `UPDATE chats SET remember=$2 WHERE id=$1`, id, *p.Remember); err != nil {
			return Chat{}, err
		}
	}
	if p.Archived != nil && topic != "" {
		if _, err := e.DB.Exec(ctx, `UPDATE chats SET archived=$2 WHERE id=$1`, id, *p.Archived); err != nil {
			return Chat{}, err
		}
	}
	if p.NewProject != nil && strings.TrimSpace(*p.NewProject) != "" && e.Memory != nil {
		b, err := e.Memory.EnsureBank(ctx, "project", strings.TrimSpace(*p.NewProject), "", "")
		if err != nil {
			return Chat{}, err
		}
		if _, err := e.DB.Exec(ctx, `UPDATE chats SET project_bank_id=$2, suggested_project='', suggestion_done=true WHERE id=$1`, id, b.ID); err != nil {
			return Chat{}, err
		}
	}
	if p.DismissSuggest {
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET suggested_project='', suggestion_done=true WHERE id=$1`, id)
	}
	if p.AcceptSuggest {
		var name string
		var isNew bool
		_ = e.DB.QueryRow(ctx, `SELECT suggested_project, suggested_new FROM chats WHERE id=$1`, id).Scan(&name, &isNew)
		if name != "" && e.Memory != nil {
			spec := name
			if isNew && !strings.Contains(name, ":") {
				spec = "project:" + name
			}
			if b, err := e.Memory.EnsureBank(ctx, "project", strings.TrimPrefix(spec, "project:"), "", ""); err == nil {
				_, _ = e.DB.Exec(ctx, `UPDATE chats SET project_bank_id=$2, suggested_project='', suggestion_done=true WHERE id=$1`, id, b.ID)
			}
		}
	}
	e.Emit("chats.update", map[string]any{"topic": topic})
	return e.GetChat(ctx, topic)
}

// DeleteChat removes a chat and its messages (the main chat cannot be deleted).
func (e *Engine) DeleteChat(ctx context.Context, id int64) error {
	var topic string
	if err := e.DB.QueryRow(ctx, `SELECT topic FROM chats WHERE id=$1`, id).Scan(&topic); err != nil {
		return errors.New("chat not found")
	}
	if topic == "" {
		return errors.New("the main chat cannot be deleted (clear it instead)")
	}
	e.StopChat(ChatKey("web", topic))
	if _, err := e.DB.Exec(ctx, `DELETE FROM chat_messages WHERE channel='web' AND topic=$1`, topic); err != nil {
		return err
	}
	_, _ = e.DB.Exec(ctx, `DELETE FROM sessions WHERE kind='chat' AND key=$1`, ChatKey("web", topic))
	_, err := e.DB.Exec(ctx, `DELETE FROM chats WHERE id=$1`, id)
	e.Emit("chats.update", map[string]any{"topic": topic, "deleted": true})
	return err
}

// touchChat records activity so the list sorts by recency.
func (e *Engine) touchChat(ctx context.Context, channel, topic string) {
	if channel == "web" {
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET last_at=now() WHERE topic=$1`, topic)
	}
}

// ChatProject is the project bank label a web chat is focused on ("" when unbound or not a web chat).
func (e *Engine) ChatProject(ctx context.Context, channel, topic string) string {
	if channel != "web" {
		return ""
	}
	var label string
	_ = e.DB.QueryRow(ctx, `SELECT COALESCE(b.kind||':'||b.name,'') FROM chats c JOIN memory_banks b ON b.id=c.project_bank_id WHERE c.topic=$1`, topic).Scan(&label)
	return label
}

// ChatRemembers is false for a chat the user marked "do not remember".
func (e *Engine) ChatRemembers(ctx context.Context, channel, topic string) bool {
	if channel != "web" {
		return true
	}
	remember := true
	_ = e.DB.QueryRow(ctx, `SELECT remember FROM chats WHERE topic=$1`, topic).Scan(&remember)
	return remember
}

// ── the chat a run belongs to, carried in the context so memory recall can favour its project ──

type chatCtxKey struct{}

type chatRef struct{ Channel, Topic string }

func withChat(ctx context.Context, channel, topic string) context.Context {
	return context.WithValue(ctx, chatCtxKey{}, chatRef{channel, topic})
}

// ChatFrom returns the chat a context belongs to.
func ChatFrom(ctx context.Context) (channel, topic string, ok bool) {
	r, ok := ctx.Value(chatCtxKey{}).(chatRef)
	return r.Channel, r.Topic, ok
}

// ── naming and project suggestion ───────────────────────────────────────────

const nameChatPrompt = `You name a chat between a user and their assistant and say whether it belongs to one of the user's projects.
You get the first messages of the chat and the existing project banks (label — description).
Answer JSON only: {"title":"2-5 word title in the user's language, no quotes","project":"the exact label of an existing project bank this chat is clearly about, or empty","new_project":"a short name for a NEW project if the chat is clearly about one ongoing piece of work that has no bank yet, or empty"}
Rules: bind to a project only when the chat is clearly about it (not for small talk, quick questions or general topics); prefer an existing bank; leave both project fields empty when unsure.`

// Naming: the first message gives a provisional title at once (no model); a small model writes a real title once the
// chat has some substance (3 user messages) and once more when it has grown (8), unless the user renamed it. The
// first model pass may also suggest a project.
const (
	nameAfter   = 3
	renameAfter = 8
)

func (e *Engine) nameChat(ctx context.Context, topic string) {
	c, err := e.GetChat(ctx, topic)
	if err != nil || c.Topic == "" {
		return
	}
	var autoTitle, done bool
	var namedAt int
	_ = e.DB.QueryRow(ctx, `SELECT auto_title, suggestion_done, named_at FROM chats WHERE topic=$1`, topic).Scan(&autoTitle, &done, &namedAt)
	var users int
	var first string
	_ = e.DB.QueryRow(ctx, `SELECT count(*), COALESCE((array_agg(text ORDER BY id))[1],'') FROM chat_messages WHERE channel='web' AND topic=$1 AND role='user'`, topic).Scan(&users, &first)
	if autoTitle && strings.TrimSpace(c.Title) == "" && first != "" {
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET title=$2 WHERE topic=$1 AND auto_title AND title=''`, topic, briefText(first, 40))
		e.Emit("chats.update", map[string]any{"topic": topic})
	}
	due := (users >= nameAfter && namedAt < nameAfter) || (users >= renameAfter && namedAt < renameAfter)
	if !due || (!autoTitle && done) || e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		return
	}
	msgs, err := e.ChatHistory(ctx, "web", topic, 14)
	if err != nil {
		return
	}
	var sb strings.Builder
	if c.Title != "" && namedAt > 0 {
		fmt.Fprintf(&sb, "Current title: %s (keep it if it still fits the chat)\n\n", c.Title)
	}
	sb.WriteString("Existing project banks:\n")
	if e.Memory != nil {
		if bs, err := e.Memory.Banks(ctx); err == nil {
			n := 0
			for _, b := range bs {
				if b.Kind == "project" && b.Status == "active" && n < 40 {
					fmt.Fprintf(&sb, "- %s", b.Label())
					if b.Description != "" {
						fmt.Fprintf(&sb, " — %s", b.Description)
					}
					sb.WriteByte('\n')
					n++
				}
			}
		}
	}
	sb.WriteString("\nChat:\n")
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "agent" {
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, briefText(m.Text, 400))
		}
	}
	var out struct {
		Title      string `json:"title"`
		Project    string `json:"project"`
		NewProject string `json:"new_project"`
	}
	if err := e.LLM.CompleteJSON(ctx, "role:fast", nameChatPrompt, sb.String(), &out); err != nil {
		return
	}
	if t := strings.TrimSpace(out.Title); t != "" && autoTitle {
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET title=$2 WHERE topic=$1 AND auto_title`, topic, briefText(t, 60))
	}
	_, _ = e.DB.Exec(ctx, `UPDATE chats SET named_at=$2 WHERE topic=$1`, topic, users)
	if !done && c.ProjectBankID == nil {
		sugg, isNew := strings.TrimSpace(out.Project), false
		if sugg == "" && strings.TrimSpace(out.NewProject) != "" {
			sugg, isNew = strings.TrimSpace(out.NewProject), true
		}
		valid := sugg == ""
		if sugg != "" && !isNew && e.Memory != nil {
			if b, err := e.Memory.BankBySpec(ctx, sugg, "", false); err == nil && b.Kind == "project" {
				sugg, valid = b.Label(), true
			}
		} else if isNew {
			valid = true
		}
		if valid {
			_, _ = e.DB.Exec(ctx, `UPDATE chats SET suggested_project=$2, suggested_new=$3 WHERE topic=$1`, topic, sugg, isNew)
		}
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET suggestion_done=true WHERE topic=$1`, topic) // asked once
	}
	e.Emit("chats.update", map[string]any{"topic": topic})
}

func briefText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ── merging chats ───────────────────────────────────────────────────────────

// MergeResult says what a merge did.
type MergeResult struct {
	Target Chat `json:"target"`
	Moved  int  `json:"moved"`  // messages moved into the target
	Merged int  `json:"merged"` // chats merged
}

const mergeSummaryPrompt = `Summarise a chat between a user and their assistant so that the assistant can continue in another chat without the transcript. 4-8 short lines: what the user wanted, what was decided or done, what is still open, any names / numbers / preferences that matter. Plain text, the user's language.`

// summariseChat writes the note that carries a merged chat's gist into the target's context.
func (e *Engine) summariseChat(ctx context.Context, title, topic string) string {
	msgs, _ := e.ChatHistory(ctx, "web", topic, 40)
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "agent" {
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, briefText(m.Text, 500))
		}
	}
	if strings.TrimSpace(sb.String()) == "" {
		return ""
	}
	gist := ""
	if e.LLM != nil && e.LLM.RoleRef(ctx, "fast") != "" {
		if out, err := e.LLM.Complete(ctx, "role:fast", mergeSummaryPrompt, sb.String(), false); err == nil {
			gist = strings.TrimSpace(out)
		}
	}
	if gist == "" { // no model: the tail of the conversation itself
		tail := msgs
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		var tb strings.Builder
		for _, m := range tail {
			if m.Role == "user" || m.Role == "agent" {
				fmt.Fprintf(&tb, "%s: %s\n", m.Role, briefText(m.Text, 300))
			}
		}
		gist = strings.TrimSpace(tb.String())
	}
	return fmt.Sprintf("[merged chat %q — its earlier conversation, summarised] %s", title, gist)
}

// MergeChats moves the messages of the source chats into the target and archives the sources (restorable with
// UnmergeChat until they are purged). The target's agent context receives a summary of each source. projectBankID
// decides the target's project when the sources disagree (nil: keep the target's, or the sources' shared one).
func (e *Engine) MergeChats(ctx context.Context, sourceIDs []int64, targetID int64, projectBankID *int64) (*MergeResult, error) {
	target, err := e.chatByID(ctx, targetID)
	if err != nil {
		return nil, errors.New("target chat not found")
	}
	var sources []Chat
	seen := map[int64]bool{targetID: true}
	for _, id := range sourceIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		c, err := e.chatByID(ctx, id)
		if err != nil {
			return nil, errors.New("a chat to merge was not found")
		}
		if c.Topic == "" {
			return nil, errors.New("the main chat can be the target of a merge, not a source")
		}
		sources = append(sources, c)
	}
	if len(sources) == 0 {
		return nil, errors.New("pick at least one other chat to merge into the target")
	}
	for _, c := range append([]Chat{target}, sources...) {
		if e.ChatBusy(ChatKey("web", c.Topic)) {
			return nil, fmt.Errorf("“%s” is working right now: wait for it to finish or stop it first", firstNonEmptyStr(c.Title, "New chat"))
		}
	}
	// project: an explicit choice wins; else the target's; else the sources' shared one; else ask
	var chosen *int64
	switch {
	case projectBankID != nil:
		chosen = projectBankID
	case target.ProjectBankID != nil:
	default:
		var shared *int64
		conflict := false
		for _, c := range sources {
			if c.ProjectBankID == nil {
				continue
			}
			if shared == nil {
				v := *c.ProjectBankID
				shared = &v
			} else if *shared != *c.ProjectBankID {
				conflict = true
			}
		}
		if conflict {
			return nil, errors.New("the chats are focused on different projects: choose which project the merged chat keeps")
		}
		chosen = shared
	}
	tsess, err := e.Sessions.Chat(ctx, "Atlas", ChatKey("web", target.Topic))
	if err != nil {
		return nil, err
	}
	res := &MergeResult{}
	for _, c := range sources {
		note := e.summariseChat(ctx, firstNonEmptyStr(c.Title, "New chat"), c.Topic)
		tag, err := e.DB.Exec(ctx, `UPDATE chat_messages SET topic=$1, moved_from=$2 WHERE channel='web' AND topic=$2`, target.Topic, c.Topic)
		if err != nil {
			return nil, err
		}
		res.Moved += int(tag.RowsAffected())
		if note != "" {
			_, _ = e.Sessions.Append(ctx, tsess.ID, Msg{Message: llmMsg("user", note), Provenance: "system"})
		}
		if _, err := e.DB.Exec(ctx, `UPDATE chats SET archived=true, merged_into=$2, merged_at=now() WHERE id=$1`, c.ID, target.Topic); err != nil {
			return nil, err
		}
		res.Merged++
	}
	if chosen != nil {
		var pb any
		if *chosen != 0 {
			pb = *chosen
		}
		_, _ = e.DB.Exec(ctx, `UPDATE chats SET project_bank_id=$2, suggested_project='', suggestion_done=true WHERE id=$1`, target.ID, pb)
	}
	_, _ = e.DB.Exec(ctx, `UPDATE chats SET last_at=now() WHERE id=$1`, target.ID)
	e.Emit("chats.update", map[string]any{"topic": target.Topic})
	e.Emit("chat.reload", map[string]any{"topic": target.Topic})
	res.Target, _ = e.GetChat(ctx, target.Topic)
	return res, nil
}

func (e *Engine) chatByID(ctx context.Context, id int64) (Chat, error) {
	return scanChat(e.DB.QueryRow(ctx, `SELECT `+chatCols+` FROM chats c LEFT JOIN memory_banks b ON b.id=c.project_bank_id WHERE c.id=$1`, id))
}

// UnmergeChat undoes a merge of one chat: its messages return to it and it is listed again. The summary that was
// added to the target's agent context stays there (it is harmless background).
func (e *Engine) UnmergeChat(ctx context.Context, id int64) error {
	c, err := e.chatByID(ctx, id)
	if err != nil || c.MergedInto == "" {
		return errors.New("that chat was not merged")
	}
	if e.ChatBusy(ChatKey("web", c.MergedInto)) {
		return errors.New("the chat it was merged into is working right now")
	}
	if _, err := e.DB.Exec(ctx, `UPDATE chat_messages SET topic=moved_from, moved_from='' WHERE channel='web' AND topic=$1 AND moved_from=$2`, c.MergedInto, c.Topic); err != nil {
		return err
	}
	if _, err := e.DB.Exec(ctx, `UPDATE chats SET archived=false, merged_into='', merged_at=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	e.Emit("chats.update", map[string]any{"topic": c.Topic})
	e.Emit("chat.reload", map[string]any{"topic": c.MergedInto})
	return nil
}

// PurgeMergedChats deletes merged-away chats (and their agent context) that have been archived for a while.
func (e *Engine) PurgeMergedChats(ctx context.Context, olderThan time.Duration) int {
	rows, err := e.DB.Query(ctx, `SELECT id, topic FROM chats WHERE merged_into<>'' AND merged_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		return 0
	}
	type row struct {
		id    int64
		topic string
	}
	var rs []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.topic) == nil {
			rs = append(rs, r)
		}
	}
	rows.Close()
	for _, r := range rs {
		_, _ = e.DB.Exec(ctx, `DELETE FROM sessions WHERE kind='chat' AND key=$1`, ChatKey("web", r.topic))
		_, _ = e.DB.Exec(ctx, `DELETE FROM chats WHERE id=$1`, r.id)
	}
	return len(rs)
}

func firstNonEmptyStr(v ...string) string {
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
