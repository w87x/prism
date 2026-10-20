package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"prism/internal/textmatch"
	"prism/internal/textutil"
	"prism/internal/tools"
)

// Things that watch for something live in four places: scheduled jobs (crons), intents and watches, trackers and their rows. When the user says
// "I have the show now, stop watching it" nobody should have to know which of the four holds it. watch_find looks through all of them for
// a topic and gives each hit a reference; watch_stop ends the ones named. Nothing is deleted: a job is marked finished, an intent cancelled,
// a tracker archived, a row retired — all of it visible, and a job can be switched on again.
type watchHit struct {
	ref  string
	text string
	line string
}

// WatchItem is one thing that watches, for the Watching view.
type WatchItem struct {
	Ref    string `json:"ref"`    // cron:ID, intent:ID, tracker:NAME, row:NAME/KEY — what watch_stop takes
	Kind   string `json:"kind"`   // job | watch | tracker | row
	Detail string `json:"detail"` // one line
}

// WatchList lists what watches for something, optionally only for a topic.
func (s *Service) WatchList(ctx context.Context, query string) ([]WatchItem, error) {
	hits, err := s.watchHits(ctx)
	if err != nil {
		return nil, err
	}
	if q := strings.TrimSpace(query); q != "" {
		docs := make([]string, len(hits))
		for i, h := range hits {
			docs[i] = h.text
		}
		var kept []watchHit
		for _, r := range textmatch.Rank(q, docs, 200) {
			kept = append(kept, hits[r.Index])
		}
		hits = kept
	}
	out := make([]WatchItem, 0, len(hits))
	for _, h := range hits {
		kind := "job"
		switch {
		case strings.HasPrefix(h.ref, "intent:"):
			kind = "watch"
		case strings.HasPrefix(h.ref, "tracker:"):
			kind = "tracker"
		case strings.HasPrefix(h.ref, "row:"):
			kind = "row"
		}
		out = append(out, WatchItem{Ref: h.ref, Kind: kind, Detail: h.line})
	}
	return out, nil
}

// StopWatches ends the named things; it reports what stopped and what could not.
func (s *Service) StopWatches(ctx context.Context, refs []string, note string) (done, failed []string) {
	for _, ref := range refs {
		if err := s.stopWatch(ctx, strings.TrimSpace(ref), note); err != nil {
			failed = append(failed, ref+": "+err.Error())
		} else {
			done = append(done, ref)
		}
	}
	return done, failed
}

func (s *Service) watchHits(ctx context.Context) ([]watchHit, error) {
	var out []watchHit
	cs, err := s.Crons(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if c.System || !c.Enabled || c.FinishedAt != nil {
			continue
		}
		out = append(out, watchHit{ref: fmt.Sprintf("cron:%d", c.ID), text: c.Name + " " + c.Prompt,
			line: fmt.Sprintf("scheduled job #%d %q (%s, %s) — %s", c.ID, c.Name, c.Agent, c.Expr, textutil.Brief(c.Prompt, 140))})
	}
	is, err := s.Intents(ctx, "active")
	if err != nil {
		return nil, err
	}
	for _, i := range is {
		out = append(out, watchHit{ref: fmt.Sprintf("intent:%d", i.ID), text: i.Description + " " + string(i.Predicate),
			line: fmt.Sprintf("%s #%d (%s) — %s", i.Type, i.ID, i.Owner, textutil.Brief(i.Description, 140))})
	}
	if s.Trackers != nil {
		ts, err := s.Trackers.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if t.Status != "active" {
				continue
			}
			out = append(out, watchHit{ref: "tracker:" + t.Name, text: t.Name + " " + t.Description,
				line: fmt.Sprintf("tracker %q (%d rows) — %s", t.Name, t.Rows, textutil.Brief(t.Description, 120))})
			rows, err := s.Trackers.Rows(ctx, t.ID, "", false, 300)
			if err != nil {
				continue
			}
			for _, r := range rows {
				b, _ := json.Marshal(r.Data)
				out = append(out, watchHit{ref: "row:" + t.Name + "/" + r.Key, text: r.Key + " " + string(b),
					line: fmt.Sprintf("row %q in tracker %q — %s", r.Key, t.Name, textutil.Brief(string(b), 120))})
			}
		}
	}
	return out, nil
}

func (s *Service) registerWatching(reg *tools.Registry) {
	reg.Register(
		&tools.Tool{
			Name: "watch_find", Category: "autonomy", Risk: tools.RiskRead,
			Description: "Find everything that is watching or checking for a topic — scheduled jobs, watches/intents, trackers and tracker rows — in one search, e.g. \"dune\" or \"power outage\". Each hit has a reference for watch_stop. Use it when the user asks what is being watched, or wants something to stop being watched.",
			Params:      tools.Obj("query", tools.Str("query", "the topic: a show, a product, a place… (empty lists everything)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Query string }](raw)
				if err != nil {
					return "", err
				}
				hits, err := s.watchHits(ctx)
				if err != nil {
					return "", err
				}
				if q := strings.TrimSpace(a.Query); q != "" {
					docs := make([]string, len(hits))
					for i, h := range hits {
						docs[i] = h.text
					}
					var kept []watchHit
					for _, r := range textmatch.Rank(q, docs, 25) {
						kept = append(kept, hits[r.Index])
					}
					hits = kept
				}
				if len(hits) == 0 {
					return "Nothing is watching for that.", nil
				}
				if len(hits) > 40 {
					hits = hits[:40]
				}
				var sb strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&sb, "[%s] %s\n", h.ref, h.line)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "watch_stop", Category: "autonomy", Risk: tools.RiskWrite, Auto: true,
			Description: "Stop watching: end the things watch_find returned, by reference (cron:ID, intent:ID, tracker:NAME, row:NAME/KEY). Use it when the user asks to stop, or when what was watched has clearly happened and been dealt with (the episode is out and downloaded). Nothing is deleted: a job is marked finished with your note (it can be switched on again), an intent is cancelled, a tracker is archived together with the jobs that refresh it, a row is retired.",
			Params: tools.Obj("refs,note", tools.StrList("refs", "references from watch_find"),
				tools.Str("note", "why it ended, in one sentence (e.g. 'season 3 released and downloaded')")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Refs []string
					Note string
				}](raw)
				if err != nil {
					return "", err
				}
				if len(a.Refs) == 0 {
					return "", errors.New("name what to stop (references from watch_find)")
				}
				note := strings.TrimSpace(a.Note)
				if note == "" {
					return "", errors.New("say why it ended")
				}
				var done, failed []string
				for _, ref := range a.Refs {
					if err := s.stopWatch(ctx, strings.TrimSpace(ref), note); err != nil {
						failed = append(failed, ref+": "+err.Error())
					} else {
						done = append(done, ref)
					}
				}
				out := ""
				if len(done) > 0 {
					out = "Stopped: " + strings.Join(done, ", ") + "."
				}
				if len(failed) > 0 {
					out += " Could not stop: " + strings.Join(failed, "; ")
				}
				return strings.TrimSpace(out), nil
			},
		},
	)
}

func (s *Service) stopWatch(ctx context.Context, ref, note string) error {
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" {
		return errors.New("not a reference")
	}
	switch kind {
	case "cron":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		return s.FinishCron(ctx, n, note)
	case "intent":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		return s.UpdateIntent(ctx, n, "cancelled", nil)
	case "tracker":
		if s.Trackers == nil {
			return errors.New("trackers are not available")
		}
		if err := s.Trackers.Archive(ctx, id); err != nil {
			return err
		}
		// the jobs that refresh it have nothing left to do
		if cs, err := s.Crons(ctx); err == nil {
			for _, c := range cs {
				if !c.System && c.Enabled && c.FinishedAt == nil && strings.Contains(strings.ToLower(c.Prompt), strings.ToLower(id)) {
					_ = s.FinishCron(ctx, c.ID, "tracker "+id+" archived: "+note)
				}
			}
		}
		return nil
	case "row":
		if s.Trackers == nil {
			return errors.New("trackers are not available")
		}
		tn, key, ok := strings.Cut(id, "/")
		if !ok {
			return errors.New("a row reference is row:TRACKER/KEY")
		}
		t, err := s.Trackers.Get(ctx, tn)
		if err != nil {
			return err
		}
		_, err = s.Trackers.RetireRow(ctx, t.ID, key, note)
		return err
	}
	return fmt.Errorf("unknown kind %q", kind)
}

// jobLifecycle is added to the input of every scheduled job: a job that waits for something should end itself when it has happened.
func jobLifecycle(id int64) string {
	return fmt.Sprintf("\n\n[Job #%d] If this job's purpose is now complete — what it waits for has happened and been dealt with (the release is out and downloaded, the deadline passed, the list is empty) — call cron_finish(id=%d, note) after your reply so it stops running. Do not finish it just because nothing changed today.", id, id)
}
