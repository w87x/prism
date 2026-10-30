package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/textutil"
)

// Chat housekeeping, once a day. Chats pile up: empty ones from a click on "new chat", two chats about one subject, a chat that turned
// into two subjects, a chat nobody has opened for a month, a title that no longer says what the chat is about. Empty chats are simply
// removed; everything else is only SUGGESTED (Today → Needs your attention) and done when the user agrees, because every one of them
// moves or hides something the user wrote. Cheap rules and embeddings find the candidates; a model is asked one short question
// (SAME or DIFFERENT) before a candidate is shown, and a model that fails or is slow means nothing is suggested.
const (
	chatEmptyAfter    = 24 * time.Hour
	chatStaleAfter    = 30 * 24 * time.Hour
	chatMergeSim      = 0.62 // two chats' profiles this alike are candidates for a merge
	chatSplitMinMsgs  = 8    // a chat is checked for a second subject once it has this many messages of the user's
	chatSplitFar      = 0.30 // a group of messages whose centre is this far from the rest's centre is another subject
	chatSplitTogether = 0.45 // outliers must be this alike to be one other subject
	chatSplitMin      = 3
	chatDriftSim      = 0.25 // a title this far from what the chat now says is out of date
	chatSuggestPerRun = 3
)

type chatSuggestion struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"` // merge | split | archive | retitle
	Topics    []string        `json:"topics"`
	Payload   json.RawMessage `json:"payload"`
	Text      string          `json:"text"`
	CreatedAt time.Time       `json:"created_at"`
}

// ChatSuggestion is a suggestion as the UI shows it.
type ChatSuggestion = chatSuggestion

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}

func centroid(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	out := make([]float32, len(vs[0]))
	for _, v := range vs {
		if len(v) != len(out) {
			return nil
		}
		for i := range v {
			out[i] += v[i]
		}
	}
	return out
}

// chatNote is one chat as the housekeeping sees it.
type chatNote struct {
	Chat
	userMsgs []chatMsgRow
}

type chatMsgRow struct {
	ID   int64
	Text string
}

func (e *Engine) housekeepChats(ctx context.Context) {
	if e.DB == nil || e.LLMDown() {
		return
	}
	var last struct{ At time.Time }
	_, _ = e.Settings.Get(ctx, "chat_housekeeping", &last)
	if time.Since(last.At) < 24*time.Hour {
		return
	}
	last.At = time.Now()
	_ = e.Settings.Set(ctx, "chat_housekeeping", last)
	if settings.Load(ctx, e.Settings, settings.KeyGeneral, settings.General{}).ChatHousekeepingOff {
		return
	}
	e.removeEmptyChats(ctx)
	e.expireChatSuggestions(ctx)
	chats := e.chatNotes(ctx)
	made := 0
	for _, f := range []func(context.Context, []chatNote) int{e.suggestStale, e.suggestMerges, e.suggestSplits, e.suggestRetitles} {
		if made >= chatSuggestPerRun {
			break
		}
		made += f(ctx, chats)
	}
	if made > 0 {
		e.Emit("chats.suggestions", nil)
	}
}

// removeEmptyChats deletes the chats nothing was ever written in.
func (e *Engine) removeEmptyChats(ctx context.Context) {
	rows, err := e.DB.Query(ctx, `SELECT id FROM chats c WHERE c.topic<>'' AND NOT c.archived AND c.project_bank_id IS NULL AND c.created_at < now() - make_interval(secs=>$1)
		AND NOT EXISTS (SELECT 1 FROM chat_messages m WHERE m.channel='web' AND m.topic=c.topic)`, chatEmptyAfter.Seconds())
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
		_ = e.DeleteChat(ctx, id)
	}
}

// expireChatSuggestions closes the suggestions whose chats are gone or archived since.
func (e *Engine) expireChatSuggestions(ctx context.Context) {
	_, _ = e.DB.Exec(ctx, `UPDATE chat_suggestions s SET status='stale' WHERE status='open' AND (
		EXISTS (SELECT 1 FROM unnest(s.topics) t WHERE NOT EXISTS (SELECT 1 FROM chats c WHERE c.topic=t AND NOT c.archived))
		OR created_at < now() - interval '30 days')`)
}

func (e *Engine) chatNotes(ctx context.Context) []chatNote {
	cs, err := e.Chats(ctx, false)
	if err != nil {
		return nil
	}
	var out []chatNote
	for _, c := range cs {
		if c.Topic == "" {
			continue // the main chat is where anything may be said
		}
		n := chatNote{Chat: c}
		if rows, err := e.DB.Query(ctx, `SELECT id, text FROM chat_messages WHERE channel='web' AND topic=$1 AND role='user' ORDER BY id DESC LIMIT 60`, c.Topic); err == nil {
			for rows.Next() {
				var r chatMsgRow
				if rows.Scan(&r.ID, &r.Text) == nil {
					n.userMsgs = append(n.userMsgs, r)
				}
			}
			rows.Close()
		}
		sort.Slice(n.userMsgs, func(i, j int) bool { return n.userMsgs[i].ID < n.userMsgs[j].ID })
		if len(n.userMsgs) > 0 {
			out = append(out, n)
		}
	}
	return out
}

// addChatSuggestion records one; it reports false when the same thing was suggested before (open or dismissed).
func (e *Engine) addChatSuggestion(ctx context.Context, kind, key string, topics []string, payload any, text string) bool {
	pj, _ := json.Marshal(payload)
	tag, err := e.DB.Exec(ctx, `INSERT INTO chat_suggestions(kind,key,topics,payload,text) VALUES($1,$2,$3,$4,$5) ON CONFLICT (kind,key) DO NOTHING`, kind, key, topics, pj, text)
	return err == nil && tag.RowsAffected() > 0
}

// suggestStale offers to archive a chat nobody has touched for a month.
func (e *Engine) suggestStale(ctx context.Context, chats []chatNote) int {
	n := 0
	for _, c := range chats {
		if c.Busy || time.Since(c.LastAt) < chatStaleAfter || c.ProjectBankID != nil {
			continue
		}
		if e.addChatSuggestion(ctx, "archive", c.Topic, []string{c.Topic}, nil, fmt.Sprintf("“%s” has not been touched for %d days: archive it? (it stays searchable and can be restored)", c.Title, int(time.Since(c.LastAt).Hours()/24))) {
			n++
		}
	}
	return n
}

func (e *Engine) embedTexts(ctx context.Context, in []string) [][]float32 {
	if e.LLM == nil || !e.LLM.HasEmbedding(ctx) || len(in) == 0 {
		return nil
	}
	vs, err := e.LLM.Embed(ctx, e.LLM.RoleRef(ctx, "embedding"), in)
	if err != nil || len(vs) != len(in) {
		return nil
	}
	return vs
}

func (n chatNote) profile() string {
	var sb strings.Builder
	sb.WriteString(n.Title)
	from := max(0, len(n.userMsgs)-6)
	for _, m := range n.userMsgs[from:] {
		sb.WriteString("\n" + textutil.Brief(m.Text, 200))
	}
	return sb.String()
}

// oneWord asks the small model a question that has two possible answers and reports whether it said the first.
func (e *Engine) oneWord(ctx context.Context, system, user, yes, no string) bool {
	if e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := e.LLM.Complete(cctx, e.LLM.ActionRef(cctx, "chat_housekeeping", "fast"), system, user, false)
	if err != nil {
		return false
	}
	out = strings.ToUpper(strings.TrimSpace(out))
	return strings.HasPrefix(out, strings.ToUpper(yes)) && !strings.HasPrefix(out, strings.ToUpper(no))
}

// suggestMerges finds two chats about one subject.
func (e *Engine) suggestMerges(ctx context.Context, chats []chatNote) int {
	if len(chats) < 2 {
		return 0
	}
	in := make([]string, len(chats))
	for i, c := range chats {
		in[i] = c.profile()
	}
	vs := e.embedTexts(ctx, in)
	if vs == nil {
		return 0
	}
	type pair struct {
		a, b int
		sim  float64
	}
	var ps []pair
	for i := range chats {
		for j := i + 1; j < len(chats); j++ {
			if s := cosine(vs[i], vs[j]); s >= chatMergeSim && !chats[i].Busy && !chats[j].Busy {
				ps = append(ps, pair{i, j, s})
			}
		}
	}
	sort.Slice(ps, func(x, y int) bool { return ps[x].sim > ps[y].sim })
	made := 0
	for _, p := range ps {
		if made >= 2 {
			break
		}
		a, b := chats[p.a], chats[p.b]
		if b.LastAt.After(a.LastAt) { // a is the newer one: it is what the older merges into
			a, b = b, a
		}
		key := b.Topic + ">" + a.Topic
		var seen int
		_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM chat_suggestions WHERE kind='merge' AND key IN ($1,$2)`, key, a.Topic+">"+b.Topic).Scan(&seen)
		if seen > 0 {
			continue
		}
		q := fmt.Sprintf("CHAT 1: %s\n%s\n\nCHAT 2: %s\n%s", a.Title, textutil.Brief(a.profile(), 600), b.Title, textutil.Brief(b.profile(), 600))
		if !e.oneWord(ctx, "Do these two chats of one person with their assistant cover the SAME subject, so that one chat would serve for both? ANSWER ONLY SAME or DIFFERENT.", q, "SAME", "DIFFERENT") {
			_ = e.addChatSuggestion(ctx, "merge-checked", key, nil, nil, "") // not asked again
			continue
		}
		if e.addChatSuggestion(ctx, "merge", key, []string{b.Topic, a.Topic}, map[string]any{"source": b.Topic, "target": a.Topic},
			fmt.Sprintf("“%s” and “%s” look like one subject: merge the older into the newer? (it can be undone)", b.Title, a.Title)) {
			made++
		}
	}
	return made
}

// suggestSplits finds a chat with a second subject in it: a few messages far from the rest of the chat, and alike each other.
func (e *Engine) suggestSplits(ctx context.Context, chats []chatNote) int {
	made := 0
	for _, c := range chats {
		if made >= 1 {
			break
		}
		if len(c.userMsgs) < chatSplitMinMsgs || c.Busy {
			continue
		}
		var long []chatMsgRow // "yes", "ok do it" say nothing about a subject
		for _, m := range c.userMsgs {
			if len([]rune(strings.TrimSpace(m.Text))) >= 20 {
				long = append(long, m)
			}
		}
		if len(long) < chatSplitMinMsgs {
			continue
		}
		c.userMsgs = long
		in := make([]string, len(c.userMsgs))
		for i, m := range c.userMsgs {
			in[i] = textutil.Brief(m.Text, 300)
		}
		vs := e.embedTexts(ctx, in)
		if vs == nil {
			continue
		}
		far := outlierGroup(vs)
		if far == nil {
			continue
		}
		var ids []int64
		var sample []string
		for _, i := range far {
			ids = append(ids, c.userMsgs[i].ID)
			sample = append(sample, "- "+textutil.Brief(c.userMsgs[i].Text, 120))
		}
		key := c.Topic + ":" + strconv.FormatInt(ids[0], 10)
		var title struct {
			Title string `json:"title"`
		}
		q := fmt.Sprintf("THE CHAT: %s\nMESSAGES THAT SEEM TO BE ABOUT SOMETHING ELSE:\n%s", c.Title, strings.Join(sample, "\n"))
		if !e.oneWord(ctx, "Are these messages about a DIFFERENT subject than the chat they sit in? ANSWER ONLY DIFFERENT or SAME.", q, "DIFFERENT", "SAME") {
			_ = e.addChatSuggestion(ctx, "split-checked", key, nil, nil, "")
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := e.LLM.CompleteJSON(cctx, e.LLM.ActionRef(cctx, "chat_housekeeping", "fast"), `Name the subject these messages share. Answer JSON only: {"title":"2-4 word title in the user's language"}`, strings.Join(sample, "\n"), &title)
		cancel()
		if err != nil || strings.TrimSpace(title.Title) == "" {
			continue
		}
		if e.addChatSuggestion(ctx, "split", key, []string{c.Topic}, map[string]any{"messages": ids, "title": strings.TrimSpace(title.Title)},
			fmt.Sprintf("%d messages in “%s” are about something else (%s): move them to a chat “%s”? (marked where they came from)", len(ids), c.Title, textutil.Brief(c.userMsgs[far[0]].Text, 60), strings.TrimSpace(title.Title))) {
			made++
		}
	}
	return made
}

// suggestRetitles finds a chat whose title no longer fits what it is about.
func (e *Engine) suggestRetitles(ctx context.Context, chats []chatNote) int {
	made := 0
	for _, c := range chats {
		if made >= 1 {
			break
		}
		if len(c.userMsgs) < chatSplitMinMsgs || c.Busy {
			continue
		}
		recent := c.userMsgs[len(c.userMsgs)-5:]
		in := []string{c.Title}
		for _, m := range recent {
			in = append(in, textutil.Brief(m.Text, 300))
		}
		vs := e.embedTexts(ctx, in)
		if vs == nil || cosine(vs[0], centroid(vs[1:])) >= chatDriftSim {
			continue
		}
		var out struct {
			Title string `json:"title"`
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := e.LLM.CompleteJSON(cctx, e.LLM.ActionRef(cctx, "chat_housekeeping", "fast"), `Give a chat a title from its latest messages. Answer JSON only: {"title":"2-5 word title in the user's language, no quotes"}`, strings.Join(in[1:], "\n"), &out)
		cancel()
		t := strings.TrimSpace(out.Title)
		if err != nil || t == "" || strings.EqualFold(t, c.Title) {
			continue
		}
		if e.addChatSuggestion(ctx, "retitle", c.Topic+":"+t, []string{c.Topic}, map[string]any{"title": t}, fmt.Sprintf("“%s” is now about something else: rename it “%s”?", c.Title, t)) {
			made++
		}
	}
	return made
}

// ChatSuggestions lists the open suggestions.
func (e *Engine) ChatSuggestions(ctx context.Context) ([]ChatSuggestion, error) {
	rows, err := e.DB.Query(ctx, `SELECT id,kind,topics,payload,text,created_at FROM chat_suggestions WHERE status='open' AND kind IN ('merge','split','archive','retitle') ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatSuggestion{}
	for rows.Next() {
		var s ChatSuggestion
		if rows.Scan(&s.ID, &s.Kind, &s.Topics, &s.Payload, &s.Text, &s.CreatedAt) == nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// DismissChatSuggestion closes a suggestion the user does not want; it is not made again.
func (e *Engine) DismissChatSuggestion(ctx context.Context, id int64) error {
	_, err := e.DB.Exec(ctx, `UPDATE chat_suggestions SET status='dismissed' WHERE id=$1`, id)
	if err == nil {
		e.Emit("chats.suggestions", nil)
	}
	return err
}

// AcceptChatSuggestion does what was suggested.
func (e *Engine) AcceptChatSuggestion(ctx context.Context, id int64) error {
	var kind string
	var topics []string
	var pj []byte
	if err := e.DB.QueryRow(ctx, `SELECT kind,topics,payload FROM chat_suggestions WHERE id=$1 AND status='open'`, id).Scan(&kind, &topics, &pj); err != nil {
		return fmt.Errorf("that suggestion is no longer open")
	}
	chatID := func(topic string) (int64, error) {
		c, err := e.GetChat(ctx, topic)
		if err != nil || c.Archived {
			return 0, fmt.Errorf("the chat is gone")
		}
		return c.ID, nil
	}
	var err error
	switch kind {
	case "archive":
		var cid int64
		if cid, err = chatID(topics[0]); err == nil {
			yes := true
			_, err = e.UpdateChat(ctx, cid, ChatPatch{Archived: &yes})
		}
	case "retitle":
		var p struct{ Title string }
		_ = json.Unmarshal(pj, &p)
		var cid int64
		if cid, err = chatID(topics[0]); err == nil {
			_, err = e.UpdateChat(ctx, cid, ChatPatch{Title: &p.Title})
		}
	case "merge":
		var p struct{ Source, Target string }
		_ = json.Unmarshal(pj, &p)
		var s, t int64
		if s, err = chatID(p.Source); err == nil {
			if t, err = chatID(p.Target); err == nil {
				_, err = e.MergeChats(ctx, []int64{s}, t, nil)
			}
		}
	case "split":
		var p struct {
			Messages []int64
			Title    string
		}
		_ = json.Unmarshal(pj, &p)
		err = e.splitChat(ctx, topics[0], p.Messages, p.Title)
	default:
		err = fmt.Errorf("unknown suggestion")
	}
	if err != nil {
		return err
	}
	_, _ = e.DB.Exec(ctx, `UPDATE chat_suggestions SET status='accepted' WHERE id=$1`, id)
	e.Emit("chats.suggestions", nil)
	return nil
}

// splitChat moves the given messages of the user, and the answers that followed each, to a new chat; they are marked with where they came from.
func (e *Engine) splitChat(ctx context.Context, from string, userMsgs []int64, title string) error {
	if len(userMsgs) == 0 {
		return fmt.Errorf("nothing to move")
	}
	if e.ChatBusy(ChatKey("web", from)) {
		return fmt.Errorf("the chat is busy: try again when the answer is done")
	}
	dest, err := e.CreateChat(ctx, title, 0)
	if err != nil {
		return err
	}
	for _, id := range userMsgs {
		// the message and what follows it up to the next message of the user (the answer and its notes)
		if _, err := e.DB.Exec(ctx, `UPDATE chat_messages SET topic=$2, moved_from=$3 WHERE channel='web' AND topic=$3 AND id>=$1
			AND id < COALESCE((SELECT min(id) FROM chat_messages WHERE channel='web' AND topic=$3 AND role='user' AND id>$1 AND id<>ALL($4)), 9223372036854775807)`, id, dest.Topic, from, userMsgs); err != nil {
			return err
		}
	}
	e.touchChat(ctx, "web", dest.Topic)
	e.Emit("chat.reload", map[string]any{"topic": from})
	e.Emit("chats.update", map[string]any{"topic": dest.Topic})
	return nil
}

// outlierGroup finds a second subject in a chat's messages: the messages least like the chat's centre, taken as a group, are far from what
// the rest says and alike one another. It returns their indexes, or nil when the chat is one subject.
func outlierGroup(vs [][]float32) []int {
	order := make([]int, len(vs))
	for i := range order {
		order[i] = i
	}
	all := centroid(vs)
	sort.Slice(order, func(a, b int) bool { return cosine(vs[order[a]], all) < cosine(vs[order[b]], all) })
	var best []int
	for k := chatSplitMin; k <= len(vs)/2; k++ {
		cand, rest := order[:k], order[k:]
		cv, rv := make([][]float32, 0, k), make([][]float32, 0, len(rest))
		for _, i := range cand {
			cv = append(cv, vs[i])
		}
		for _, i := range rest {
			rv = append(rv, vs[i])
		}
		if cosine(centroid(cv), centroid(rv)) >= chatSplitFar {
			continue
		}
		var tog, cnt float64
		for x := 0; x < len(cand); x++ {
			for y := x + 1; y < len(cand); y++ {
				tog += cosine(vs[cand[x]], vs[cand[y]])
				cnt++
			}
		}
		if cnt > 0 && tog/cnt >= chatSplitTogether {
			best = append([]int(nil), cand...) // the largest group that still holds together
		}
	}
	sort.Ints(best)
	return best
}
