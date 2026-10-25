package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"prism/internal/tools"
)

// Resolver supplies an agent's default readable banks (its profile bank, user
// bank and configured extras). Implemented by the agent package.
type Resolver func(ctx context.Context, agent string) []string

// maintainers are the only agents that manage memory itself (delete, link, reflect, merge, split, consolidate).
// Everyone else stores and searches; when something needs tidying they ask Mnemosyne.
var maintainers = []string{"Mnemosyne"}

var (
	coreMu  sync.Mutex
	coreLog = map[string][]time.Time{} // agent → recent core-memory edits, for the rate limit
)

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
				facts, err := s.Find(ctx, FindReq{Query: a.Query, Banks: banks, Agent: env.Agent, K: a.Limit, History: a.History, Deep: a.Deep, Scope: useScope(env.TaskID, env.SessionID)})
				if err != nil {
					return "", err
				}
				if len(facts) == 0 {
					return "No matching facts. Banks searched: " + strings.Join(banks, ", "), nil
				}
				// a fact about another named thing than the task's (a different show, game or product) goes behind the rest; what the
				// search itself names (Synology, Jackett…) is not "another thing"
				if req := s.taskRequest(ctx, env.TaskID); req != "" {
					infra := map[string]bool{}
					for _, w := range strings.Fields(strings.ToLower(a.Query)) {
						if w = strings.Trim(w, ".,:;()\"'"); len(w) >= 4 {
							infra[w] = true
						}
					}
					facts = RankForRequest(req, facts, infra)
				}
				var sb strings.Builder
				for _, f := range facts {
					flag := FactFlags(f)
					fmt.Fprintf(&sb, "#%d (%s, %s)%s %s\n", f.ID, f.Bank, f.CreatedAt.Format("2006-01-02"), flag, f.Text)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_check", Category: "memory", Base: true, Risk: tools.RiskRead,
			Description: "Check what memory can SUPPORT for a task: give the specific things you need (a price, a spec, a date) and get, per item, whether memory supports it, disputes it, only has a stale or unverified version, or has nothing in the searched banks — plus the list of gaps. " +
				"Use it before relying on memory for facts a task depends on, instead of guessing from a few memory_find hits. A gap means 'not found here', not 'false': go and fetch or verify those items.",
			Params: tools.Obj("needs", tools.ObjList("needs", "the things you need", "item",
				tools.Str("item", "what you need, as a short question or phrase, e.g. 'GMKtec EVO-X2 price'"),
				tools.Int("max_age_days", "optional: evidence learned longer ago than this counts as stale (e.g. 14 for a price)")),
				tools.StrList("banks", "bank specs to search (default: your usual banks)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Needs []Need   `json:"needs"`
					Banks []string `json:"banks"`
				}](raw)
				if err != nil {
					return "", err
				}
				banks := a.Banks
				if len(banks) == 0 {
					banks = defaults(ctx, env.Agent)
				}
				rep, err := s.CoverageOf(ctx, banks, env.Agent, a.Needs)
				if err != nil {
					return "", err
				}
				return rep.Render(), nil
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
						return "", fmt.Errorf("%s was not read in this task: only cite pages you actually fetched. Fetch it with web_fetch first, then cite it — or, if you cannot read it, give verdict \"unclear\" instead", u)
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
				tools.Str("source_url", "for a fact learned from the web: the page URL it came from (must be a page you actually read). The same fact found on independent sites is trusted more"),
				tools.Int("ttl_days", "only for a fact that goes stale (a price, availability, current status): days until it should be forgotten. Omit for lasting facts")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Text string   `json:"text"`
					Bank string   `json:"bank"`
					Tags []string `json:"tags"`
					URL  string   `json:"source_url"`
					TTL  int      `json:"ttl_days"`
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
				// a probationary agent may only keep notes for itself; for a shared bank it can propose
				propose := env.Restricted && !strings.HasPrefix(strings.ToLower(a.Bank), "profile")
				r, err := s.Store(ctx, StoreReq{Propose: propose, Bank: a.Bank, Agent: env.Agent, Text: a.Text, Tags: a.Tags, Source: src, Confidence: conf, Origin: origin, TaskID: env.TaskID, TTLDays: clampTTL(a.TTL), SourceRef: a.URL})
				if err != nil {
					return "", err
				}
				switch {
				case r.Rejected:
					return fmt.Sprintf("Not stored: the user already rejected this as false (fact #%d). Do not store it again; if you have new evidence, say so to the user instead.", r.Fact.ID), nil
				case propose && !r.Duplicate:
					return fmt.Sprintf("Proposed as #%d: you are on probation, so a curator must approve it before it enters %s. It is not used until then.", r.Fact.ID, a.Bank), nil
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
			Name: "core_memory_update", Category: "memory", Risk: tools.RiskWrite, Auto: true,
			Description: "Edit CORE memory: a few short labelled blocks (about_user, preferences, now, …) that are always in every agent's prompt. Use it sparingly, for lasting things EVERY agent should simply know about the user — not for facts that can be searched for (those go in memory_store). " +
				"mode replace (default) rewrites the block, append adds a line. A block holds at most 800 characters, so keep it tight; the previous text is kept in a history. Never based on something you read on the web.",
			Params: tools.Obj("label,text", tools.Str("label", "block label, e.g. about_user, preferences, now"), tools.Str("text", "the text (the whole new text for replace, one line for append)"),
				tools.Enum("mode", "replace (default) or append", "replace", "append")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Label, Text, Mode string }](raw)
				if err != nil {
					return "", err
				}
				if env.Tainted {
					return "", errors.New("core memory is not edited on the strength of web or other untrusted content: ask the user, or store an ordinary fact with memory_store")
				}
				if env.Restricted {
					return "", errors.New("an agent on probation cannot edit core memory")
				}
				coreMu.Lock()
				now := time.Now()
				var keep []time.Time
				for _, t := range coreLog[env.Agent] {
					if now.Sub(t) < time.Hour {
						keep = append(keep, t)
					}
				}
				if len(keep) >= 10 {
					coreMu.Unlock()
					return "", errors.New("core memory edit limit reached (10 per hour): batch your edits")
				}
				coreLog[env.Agent] = append(keep, now)
				coreMu.Unlock()
				text := strings.TrimSpace(a.Text)
				if strings.EqualFold(a.Mode, "append") {
					bs, _ := s.CoreBlocks(ctx)
					for _, b := range bs {
						if strings.EqualFold(b.Label, a.Label) && strings.TrimSpace(b.Text) != "" {
							text = strings.TrimSpace(b.Text) + "\n" + text
						}
					}
				}
				if err := s.SetCoreBlock(ctx, a.Label, text, "", env.Agent); err != nil {
					return "", err
				}
				return fmt.Sprintf("Core memory block %q updated (%d characters).", strings.ToLower(strings.TrimSpace(a.Label)), len([]rune(text))), nil
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
			Name: "memory_reclassify", Category: "memory", Only: maintainers, Risk: tools.RiskWrite,
			Description: "Move a fact to the bank it actually belongs in. Unlike memory_merge_banks/memory_split_bank (project/domain banks only), this works on ANY bank including user and profile — use it when memory_store filed something in the wrong place, e.g. a fact about a third party sitting in the user's own bank. Target bank is created if it doesn't exist yet, exactly like memory_store.",
			Params:      tools.Obj("id,bank", tools.Int("id", "fact id"), tools.Str("bank", "target bank spec: user | profile:<agent> | project:<name> | domain:<name>"), tools.Str("reason", "one line on why the original bank was wrong (for the audit trail)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID     int64
					Bank   string
					Reason string
				}](raw)
				if err != nil {
					return "", err
				}
				cur, err := s.GetFact(ctx, a.ID)
				if err != nil {
					return "", fmt.Errorf("fact %d not found: %w", a.ID, err)
				}
				dst, err := s.BankBySpec(ctx, a.Bank, env.Agent, true)
				if err != nil {
					return "", err
				}
				if dst.ID == cur.BankID {
					return "", fmt.Errorf("fact %d is already in %s", a.ID, dst.Label())
				}
				if err := s.MoveFact(ctx, a.ID, dst.ID); err != nil {
					return "", err
				}
				return fmt.Sprintf("moved fact %d from %s to %s", a.ID, cur.Bank, dst.Label()), nil
			},
		},
		&tools.Tool{
			Name: "memory_share", Category: "memory", Only: maintainers, Risk: tools.RiskWrite,
			Description: "Make a fact visible in one more bank WITHOUT copying it (e.g. a fact about the user's NAS that also belongs to project:homelab). The fact keeps its home bank, evidence and history; searching either bank finds it. To take it out of an extra bank again, pass remove=true.",
			Params:      tools.Obj("id,bank", tools.Int("id", "fact id"), tools.Str("bank", "bank spec to show it in: user | profile:<agent> | project:<name> | domain:<name>"), tools.Bool("remove", "take the fact out of that extra bank instead")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID     int64
					Bank   string
					Remove bool
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Remove {
					if err := s.UnshareFact(ctx, env.Agent, a.ID, a.Bank); err != nil {
						return "", err
					}
					return fmt.Sprintf("fact %d is no longer shown in %s", a.ID, a.Bank), nil
				}
				if err := s.ShareFact(ctx, env.Agent, a.ID, a.Bank); err != nil {
					return "", err
				}
				return fmt.Sprintf("fact %d is now also visible in %s", a.ID, a.Bank), nil
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
			Name: "memory_analyze_across", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Cross-bank analysis: bank-by-bank analysis cannot see that two banks disagree or repeat each other. This takes the newest facts, finds the closest facts in OTHER banks and judges each pair — contradictions are flagged for review, duplicates merged (the kept fact becomes visible in both banks), agreement recorded — and names connections that only show across banks. Runs automatically after enough new facts; call it to force a pass.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				r, err := s.AnalyzeAcross(ctx, true, 0)
				if err != nil {
					return "", err
				}
				return r.String(), nil
			},
		},
		&tools.Tool{
			Name: "memory_sweep", Category: "memory", Only: maintainers, Risk: tools.RiskWrite, Auto: true,
			Description: "Duplicate sweep: finds pairs of live facts that read almost alike across the whole memory, has the main model judge each pair once, and retires the weaker of two that say the same thing (or the older of a changed value). Different names, places, numbers or dates are kept apart. Every merge can be undone. Runs automatically after analysis; dry_run=true only lists what it would merge.",
			Params:      tools.Obj("", tools.Bool("dry_run", "list what would be merged without changing anything")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					DryRun bool `json:"dry_run"`
				}](raw)
				if err != nil {
					return "", err
				}
				r, err := s.Sweep(ctx, a.DryRun)
				if err != nil {
					return "", err
				}
				return r.String(), nil
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
					Bank        string `json:"bank"`
					Name        string `json:"name"`
					Description string `json:"description"`
					FactIDs     IDs    `json:"fact_ids"`
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
