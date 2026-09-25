package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/tools"
)

// Resolver supplies an agent's default readable banks (its profile bank, user
// bank and configured extras). Implemented by the agent package.
type Resolver func(ctx context.Context, agent string) []string

// maintainers are the only agents that manage memory itself (delete, link, reflect, merge, split, consolidate).
// Everyone else stores and searches; when something needs tidying they ask Mnemosyne.
var maintainers = []string{"Mnemosyne"}

// RegisterTools adds the memory toolset. memory_find / memory_banks are part of every agent's base set.
func RegisterTools(reg *tools.Registry, s *Service, defaults Resolver) {
	reg.Register(
		&tools.Tool{
			Name: "memory_find", Category: "memory", Base: true, Risk: tools.RiskRead,
			Description: "Search long-term memory. Without 'banks' it searches the user bank, your profile bank and your assigned banks. " +
				"Bank specs: user | profile | project:<name> | domain:<name>. Use memory_banks to list what exists.",
			Params: tools.Obj("query",
				tools.Str("query", "what to look for, phrased naturally"),
				tools.StrList("banks", "optional bank specs to search instead of the defaults"),
				tools.Int("limit", "max facts (default 8)"),
				tools.Bool("history", "include superseded (outdated) facts"),
				tools.Bool("deep", "rerank the best candidates with the model: slower, more precise; use when plain results look off")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query   string   `json:"query"`
					Banks   []string `json:"banks"`
					Limit   int      `json:"limit"`
					History bool     `json:"history"`
					Deep    bool     `json:"deep"`
				}](raw)
				if err != nil {
					return "", err
				}
				banks := a.Banks
				if len(banks) == 0 {
					banks = defaults(ctx, env.Agent)
				}
				facts, err := s.Find(ctx, FindReq{Query: a.Query, Banks: banks, Agent: env.Agent, K: a.Limit, History: a.History, Deep: a.Deep})
				if err != nil {
					return "", err
				}
				if len(facts) == 0 {
					return "No matching facts. Banks searched: " + strings.Join(banks, ", "), nil
				}
				var sb strings.Builder
				for _, f := range facts {
					flag := ""
					if f.ValidTo != nil {
						flag = " [outdated since " + f.ValidTo.Format("2006-01-02") + "]"
					}
					if f.Confidence < 0.5 {
						flag += " [unverified]"
					} else if len(f.Origins) >= 2 {
						flag += fmt.Sprintf(" [confirmed by %d sites]", len(f.Origins))
					}
					if f.Kind == ConclusionKind {
						flag += fmt.Sprintf(" [conclusion from %d facts, %.0f%% sure", f.Proof, f.Confidence*100)
						if f.Stale {
							flag += "; some evidence was retired — needs review"
						}
						flag += "]"
					}
					if f.Via != nil {
						flag += fmt.Sprintf(" [linked to #%d]", *f.Via)
					}
					fmt.Fprintf(&sb, "#%d (%s, %s)%s %s\n", f.ID, f.Bank, f.CreatedAt.Format("2006-01-02"), flag, f.Text)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_models", Category: "memory", Base: true, Risk: tools.RiskRead,
			Description: "Read the user's mental models: standing questions about their world (projects, preferences, setups…) with a maintained answer each. Look here FIRST for broad questions, before memory_find digs through raw facts. Pass part of a name or question to filter.",
			Params:      tools.Obj("", tools.Str("filter", "optional text to match against model names and questions")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Filter string }](raw)
				if err != nil {
					return "", err
				}
				ms, err := s.Models(ctx)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				f := strings.ToLower(strings.TrimSpace(a.Filter))
				for _, m := range ms {
					if m.Body == "" || (f != "" && !strings.Contains(strings.ToLower(m.Name+" "+m.Query), f)) {
						continue
					}
					fmt.Fprintf(&sb, "## %s\nQ: %s\n%s\n(from facts %v", m.Name, m.Query, m.Body, m.Sources)
					if m.RefreshedAt != nil {
						fmt.Fprintf(&sb, ", refreshed %s", m.RefreshedAt.Format("2006-01-02"))
					}
					sb.WriteString(")\n\n")
				}
				if sb.Len() == 0 {
					return "No mental models match. Use memory_find.", nil
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_verify", Category: "memory", Base: true, Risk: tools.RiskWrite, Auto: true,
			Description: "Record the result of checking a memory fact or hypothesis against the web. verdict: confirmed (needs source_urls of pages from TWO different websites that you read in this task: the fact becomes trusted), contradicted (it is retired) or unclear (left as it is, not re-queued for two weeks).",
			Params: tools.Obj("id,verdict",
				tools.Int("id", "the memory fact / hypothesis id"),
				tools.Enum("verdict", "outcome of the check", "confirmed", "contradicted", "unclear"),
				tools.Str("note", "a few words why"),
				tools.StrList("source_urls", "pages you read that support the verdict")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID      int64    `json:"id"`
					Verdict string   `json:"verdict"`
					Note    string   `json:"note"`
					URLs    []string `json:"source_urls"`
				}](raw)
				if err != nil {
					return "", err
				}
				var origins []string
				for _, u := range a.URLs {
					if env.Sources == nil || !env.Sources.Has(u) {
						return "", fmt.Errorf("%s was not read in this task: only cite pages you fetched", u)
					}
					if o, ok := tools.Origin(u); ok {
						origins = append(origins, o)
					}
				}
				return s.ApplyVerdict(ctx, a.ID, a.Verdict, a.Note, origins)
			},
		},
		&tools.Tool{
			Name: "memory_banks", Category: "memory", Base: true, Risk: tools.RiskRead,
			Description: "List memory banks (user, profile, project, domain) with fact counts.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				bs, err := s.Banks(ctx)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, b := range bs {
					if b.Status != "active" {
						continue
					}
					fmt.Fprintf(&sb, "%s — %d facts", b.Label(), b.Facts)
					if b.Description != "" {
						sb.WriteString(" — " + b.Description)
					}
					sb.WriteByte('\n')
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_store", Category: "memory", Base: true, Risk: tools.RiskWrite, Auto: true,
			Description: "Remember a durable fact for later tasks. Bank: 'profile' (lessons for you, default), 'user' (facts about the user), " +
				"'project:<name>' (facts of an ongoing project — created on demand) or 'domain:<name>'. One self-contained sentence. " +
				"If it contradicts an older fact, the old one is retired automatically. Not for the current date/time itself (the clock tool answers that, and it would be stale tomorrow) or other transient state.",
			Params: tools.Obj("text",
				tools.Str("text", "the fact, one self-contained sentence"),
				tools.Str("bank", "target bank spec (default profile)"),
				tools.StrList("tags", "keywords"),
				tools.Str("source_url", "for a fact learned from the web: the page URL it came from (must be a page you actually read). The same fact found on independent sites is trusted more")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Text string   `json:"text"`
					Bank string   `json:"bank"`
					Tags []string `json:"tags"`
					URL  string   `json:"source_url"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Bank == "" {
					a.Bank = "profile"
				}
				origin := ""
				if a.URL != "" && env.Tainted {
					if !env.Sources.Has(a.URL) {
						return "", errors.New("source_url must be a page you read in this task; leave it out if the fact did not come from a web page")
					}
					origin, _ = tools.Origin(a.URL)
				}
				conf := 0.75
				src := "agent:" + env.Agent
				if env.Tainted { // memory poisoning defence: facts learned from untrusted content are flagged
					conf = 0.4
					src += " (tainted)"
					a.Tags = append(a.Tags, "unverified")
				}
				r, err := s.Store(ctx, StoreReq{Bank: a.Bank, Agent: env.Agent, Text: a.Text, Tags: a.Tags, Source: src, Confidence: conf, Origin: origin, TaskID: env.TaskID})
				if err != nil {
					return "", err
				}
				switch {
				case r.Corroborated:
					return fmt.Sprintf("Already known (fact #%d); a second source raised its confidence to %.0f%% (sources: %s).", r.Fact.ID, r.Fact.Confidence*100, strings.Join(r.Fact.Origins, ", ")), nil
				case r.Duplicate:
					return fmt.Sprintf("Already known (fact #%d); reinforced.", r.Fact.ID), nil
				case len(r.Superseded) > 0:
					return fmt.Sprintf("Stored as #%d; it supersedes fact(s) %v.", r.Fact.ID, r.Superseded), nil
				}
				return fmt.Sprintf("Stored as #%d in %s.", r.Fact.ID, r.Fact.Bank), nil
			},
		},
		&tools.Tool{
			Name: "memory_feedback", Category: "memory", Base: true, Risk: tools.RiskWrite, Auto: true,
			Description: "Tell memory whether a retrieved fact (by #id) was useful or wrong, so ranking improves.",
			Params:      tools.Obj("id,useful", tools.Int("id", "fact id"), tools.Bool("useful", "true if helpful, false if wrong/outdated")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID     int64 `json:"id"`
					Useful bool  `json:"useful"`
				}](raw)
				if err != nil {
					return "", err
				}
				return "ok", s.Feedback(ctx, a.ID, a.Useful)
			},
		},
		&tools.Tool{
			Name: "memory_project", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Create (or reopen) a named project memory bank, e.g. 'Price check November'. Facts stored there stay separate from general memory.",
			Params:      tools.Obj("name", tools.Str("name", "project name"), tools.Str("description", "what the project is about")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Name, Description string }](raw)
				if err != nil {
					return "", err
				}
				b, err := s.EnsureBank(ctx, KindProject, strings.TrimSpace(a.Name), "", a.Description)
				if err != nil {
					return "", err
				}
				return "Project bank ready: " + b.Label(), nil
			},
		},
		&tools.Tool{
			Name: "memory_delete", Category: "memory", Only: maintainers, Risk: tools.RiskWrite,
			Description: "Delete a fact by id (memory maintenance / user asked to forget). Reserved for Mnemosyne.",
			Params:      tools.Obj("id", tools.Int("id", "fact id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				return "deleted", s.DeleteFact(ctx, a.ID)
			},
		},
		&tools.Tool{
			Name: "memory_consolidate", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Run memory housekeeping now: distil pending raw messages into facts, archive long-unused low-rank facts, purge old history.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				n, err := s.Process(ctx, 60, true)
				if err != nil {
					return "", err
				}
				arch, purged, err := s.Prune(ctx, 180*24*time.Hour)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Distilled %d facts from raw; archived %d stale facts; purged %d old history rows.", n, arch, purged), nil
			},
		},
		&tools.Tool{
			Name: "memory_list", Category: "memory", Risk: tools.RiskRead,
			Description: "List facts of a bank (maintenance), highest rank first.",
			Params:      tools.Obj("bank", tools.Str("bank", "bank spec"), tools.Int("limit", "max facts (default 40)"), tools.Str("contains", "substring filter")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Bank     string
					Limit    int
					Contains string
				}](raw)
				if err != nil {
					return "", err
				}
				b, err := s.BankBySpec(ctx, a.Bank, env.Agent, false)
				if err != nil {
					if strings.Contains(err.Error(), "does not exist") {
						// banks are created on first store, so a fresh agent's own profile bank is legitimately absent
						var names []string
						if bs, berr := s.Banks(ctx); berr == nil {
							for _, x := range bs {
								names = append(names, x.Label())
							}
						}
						return fmt.Sprintf("Bank %q has no facts yet (banks are created on first store). Existing banks: %s", a.Bank, strings.Join(names, ", ")), nil
					}
					return "", err
				}
				if a.Limit == 0 {
					a.Limit = 40
				}
				fs, err := s.Facts(ctx, b.ID, a.Contains, false, a.Limit, 0)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, f := range fs {
					fmt.Fprintf(&sb, "#%d rank=%.2f hits=%d %s\n", f.ID, f.Rank, f.Hits, f.Text)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_link", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Link two facts (by #id) that belong together, in any banks: kind related (default), supports or contradicts. Retrieval follows links, so a search that finds one fact also surfaces its linked facts. Set remove=true to delete a link.",
			Params: tools.Obj("a,b", tools.Int("a", "first fact id"), tools.Int("b", "second fact id"),
				tools.Str("kind", "related | supports | contradicts"), tools.Str("note", "why they belong together"), tools.Bool("remove", "delete the link instead")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					A      int64  `json:"a"`
					B      int64  `json:"b"`
					Kind   string `json:"kind"`
					Note   string `json:"note"`
					Remove bool   `json:"remove"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Remove {
					return "unlinked", s.Unlink(ctx, a.A, a.B)
				}
				if a.Kind == LinkEvidence {
					return "", errors.New("evidence links are made by reflection (memory_reflect)")
				}
				if err := s.Link(ctx, a.A, a.B, a.Kind, a.Note, "agent", 0.7); err != nil {
					return "", err
				}
				return fmt.Sprintf("Linked #%d and #%d.", a.A, a.B), nil
			},
		},
		&tools.Tool{
			Name: "memory_reflect", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Reflect on a bank: draw conclusions (durable generalisations that several facts support, each citing its evidence), strengthen ones that gained evidence, revise stale ones. Runs automatically when a bank has gathered enough new facts; call it to force a pass. bank omitted = every bank with something new.",
			Params:      tools.Obj("", tools.Str("bank", "bank spec, e.g. user or project:Trip")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Bank string }](raw)
				if err != nil {
					return "", err
				}
				var rs []ReflectResult
				if strings.TrimSpace(a.Bank) != "" {
					b, err := s.BankBySpec(ctx, a.Bank, env.Agent, false)
					if err != nil {
						return "", err
					}
					r, err := s.Reflect(ctx, b.ID, true, 0)
					if err != nil {
						return "", err
					}
					rs = append(rs, r)
				} else if rs, err = s.ReflectDue(ctx, 0, 4); err != nil {
					return "", err
				}
				if len(rs) == 0 {
					return "Nothing to reflect on yet: no bank has gathered enough new facts.", nil
				}
				var sb strings.Builder
				for _, r := range rs {
					sb.WriteString(r.String() + "\n")
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_analyze", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Deep analysis of a bank: derive patterns, deductions, hypotheses, trends, risks and open questions (each citing its evidence), flag contradicting facts for review, retire duplicate facts and refresh the bank's profile card. Runs automatically after enough new facts; call it to force a pass. bank omitted = every bank with something new.",
			Params:      tools.Obj("", tools.Str("bank", "bank spec, e.g. user or project:Trip")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Bank string }](raw)
				if err != nil {
					return "", err
				}
				var rs []AnalyzeResult
				if strings.TrimSpace(a.Bank) != "" {
					b, err := s.BankBySpec(ctx, a.Bank, env.Agent, false)
					if err != nil {
						return "", err
					}
					r, err := s.Analyze(ctx, b.ID, true, 0)
					if err != nil {
						return "", err
					}
					rs = append(rs, r)
				} else if rs, err = s.AnalyzeDue(ctx, 0, 3); err != nil {
					return "", err
				}
				if len(rs) == 0 {
					return "Nothing to analyse yet: no bank has gathered enough new facts.", nil
				}
				var sb strings.Builder
				for _, r := range rs {
					sb.WriteString(r.String() + "\n")
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_synthesize", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Higher levels of thinking. level 2 reads the conclusions and insights of every bank together and derives cross-domain syntheses (themes, causes, implications, tensions); level 3 distils standing principles, open tensions and gaps from level 2. Each cites the level below, so chains end in real facts. Runs automatically after enough new material; call it to force a pass. level omitted = both.",
			Params:      tools.Obj("", tools.Int("level", "2 or 3 (default both)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Level int }](raw)
				if err != nil {
					return "", err
				}
				levels := []int{2, 3}
				if a.Level == 2 || a.Level == 3 {
					levels = []int{a.Level}
				}
				var sb strings.Builder
				for _, lv := range levels {
					r, err := s.Synthesize(ctx, lv, true, 0)
					if err != nil {
						return sb.String(), err
					}
					sb.WriteString(r.String() + "\n")
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_merge_banks", Category: "memory", Only: maintainers, Risk: tools.RiskWrite,
			Description: "Merge project (or domain) banks into one: all facts move into 'into' (an existing bank of the same kind, or a new bank created with that name) and the emptied banks are removed. Exact duplicates collapse. Use when several banks turned out to cover one topic.",
			Params:      tools.Obj("banks,into", tools.StrList("banks", "bank specs to merge, e.g. project:Trip plan, project:Trip notes"), tools.Str("into", "target bank spec, e.g. project:Trip")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Banks []string `json:"banks"`
					Into  string   `json:"into"`
				}](raw)
				if err != nil {
					return "", err
				}
				var ids []int64
				for _, spec := range a.Banks {
					b, err := s.BankBySpec(ctx, spec, env.Agent, false)
					if err != nil {
						return "", err
					}
					ids = append(ids, b.ID)
				}
				_, name, _, err := ParseSpec(a.Into, env.Agent)
				if err != nil {
					return "", err
				}
				var into int64
				if b, err := s.BankBySpec(ctx, a.Into, env.Agent, false); err == nil {
					into = b.ID
				}
				r, err := s.MergeBanks(ctx, ids, into, name)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Merged into %s: %d facts moved, %d duplicates collapsed.", r.Bank.Label(), r.Moved, r.Dropped), nil
			},
		},
		&tools.Tool{
			Name: "memory_split_bank", Category: "memory", Only: maintainers, Risk: tools.RiskWrite,
			Description: "Split a project or domain bank: without fact_ids it only PROPOSES sub-topics (names with the fact ids of each); with name and fact_ids it moves those facts into a new bank of the same kind.",
			Params: tools.Obj("bank", tools.Str("bank", "bank spec to split"), tools.Str("name", "name of the new bank"),
				tools.Str("description", "what the new bank is about"), tools.IntList("fact_ids", "facts to move")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Bank        string  `json:"bank"`
					Name        string  `json:"name"`
					Description string  `json:"description"`
					FactIDs     []int64 `json:"fact_ids"`
				}](raw)
				if err != nil {
					return "", err
				}
				b, err := s.BankBySpec(ctx, a.Bank, env.Agent, false)
				if err != nil {
					return "", err
				}
				if len(a.FactIDs) == 0 {
					gs, err := s.SuggestSplit(ctx, b.ID)
					if err != nil {
						return "", err
					}
					if len(gs) == 0 {
						return b.Label() + " looks like one topic; no split proposed.", nil
					}
					var sb strings.Builder
					for _, g := range gs {
						fmt.Fprintf(&sb, "%s — %s (facts %v)\n", g.Name, g.Description, g.FactIDs)
					}
					sb.WriteString("Call memory_split_bank again with name and fact_ids to apply one.")
					return sb.String(), nil
				}
				r, err := s.SplitBank(ctx, b.ID, a.Name, a.Description, a.FactIDs)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Moved %d facts into %s.", r.Moved, r.Bank.Label()), nil
			},
		},
		&tools.Tool{
			Name: "memory_auto_merge_banks", Category: "memory", Only: maintainers, Risk: tools.RiskWrite,
			Description: "Find project/domain banks that are really the same topic (e.g. one bank per model version instead of one bank for the model family) and merge them. Runs automatically after new facts are distilled; call this to force a pass now.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				rs, err := s.AutoMergeBanks(ctx)
				if err != nil {
					return "", err
				}
				if len(rs) == 0 {
					return "No near-duplicate banks found.", nil
				}
				var sb strings.Builder
				for _, r := range rs {
					fmt.Fprintf(&sb, "Merged into %s: %d facts, %d duplicates collapsed.\n", r.Bank.Label(), r.Moved, r.Dropped)
				}
				return sb.String(), nil
			},
		},
	)
}
