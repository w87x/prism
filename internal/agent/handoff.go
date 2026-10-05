package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/tools"
)

// Agents have fixed toolsets, so work that needs a tool an agent lacks moves between agents — and what moves
// is a REFERENCE (an artifact id), not the material itself: it keeps every agent's context small and gives the
// reader exactly the document the writer meant, instead of a retelling.

// artifactRefs validates artifact ids an agent wants to hand over and renders them for the reader. An id that
// does not exist (or expired) is an error, so a reference is never invented.
func (e *Engine) artifactRefs(ctx context.Context, ids []int64) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	if len(ids) > 12 {
		return "", errors.New("at most 12 references")
	}
	if e.DB == nil {
		return "", errors.New("artifacts are not available")
	}
	rows, err := e.DB.Query(ctx, `SELECT id,name,size FROM artifacts WHERE id=ANY($1) AND (expires_at IS NULL OR expires_at>now())`, ids)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	have := map[int64]string{}
	for rows.Next() {
		var id, size int64
		var name string
		if err := rows.Scan(&id, &name, &size); err != nil {
			return "", err
		}
		have[id] = fmt.Sprintf("artifact #%d %s (%d bytes)", id, name, size)
	}
	var lines []string
	for _, id := range ids {
		d, ok := have[id]
		if !ok {
			return "", fmt.Errorf("artifact #%d does not exist (or has expired): save the material with artifact_save first and pass the id it returns", id)
		}
		lines = append(lines, "- "+d)
	}
	return "\n\nReferences (open with artifact_read; they are the material this request is about):\n" + strings.Join(lines, "\n"), nil
}

// toolReportBlocked ends a delegated run honestly when its toolset cannot do the job and no colleague can: the
// requester gets what is missing, what was already done, and references to the partial results — and routes
// the rest (typically by delegating to an agent that holds the tool, then continuing this task).
func (e *Engine) toolReportBlocked() *tools.Tool {
	return &tools.Tool{
		Name: "report_blocked", Category: "agents", Base: true, Risk: tools.RiskRead,
		Description: "You cannot finish because a tool or capability you need is not in your toolset, and no colleague you can ask (ask_colleague) holds it. " +
			"Ends your run and tells whoever asked you exactly what is missing, what you already did, and where your partial results are (artifact references), so they can route the rest. Use it instead of a workaround or a vague apology.",
		Params: tools.Obj("missing", tools.Str("missing", "the tool or capability you lack (a tool name if you know it)"),
			tools.Str("why", "what it was needed for"),
			tools.Str("done", "what you have already finished, briefly"),
			tools.IntList("refs", "artifact ids holding your partial results or the material the next agent needs")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Missing string  `json:"missing"`
				Why     string  `json:"why"`
				Done    string  `json:"done"`
				Refs    []int64 `json:"refs"`
			}](raw)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Missing) == "" {
				return "", errors.New("say what is missing")
			}
			refs, err := e.artifactRefs(ctx, a.Refs)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			sb.WriteString("MISSING CAPABILITY: " + strings.TrimSpace(a.Missing))
			if w := strings.TrimSpace(a.Why); w != "" {
				sb.WriteString("\nNeeded for: " + w)
			}
			if d := strings.TrimSpace(a.Done); d != "" {
				sb.WriteString("\nDone so far: " + d)
			}
			if names := e.holdersOf(ctx, strings.TrimSpace(a.Missing)); len(names) > 0 {
				sb.WriteString("\nAgents that hold it: " + strings.Join(names, ", "))
			}
			sb.WriteString(refs)
			if env.Ask == nil {
				return sb.String(), nil
			}
			// the run ends here; the question travels up to the requester like any other "needs input"
			ans, err := env.Ask(ctx, tools.Question{Kind: "clarify", Text: sb.String()})
			if err != nil {
				return "", err
			}
			return "Your requester answered: " + ans, nil
		},
	}
}

// holdersOf names the enabled agents holding a tool when `missing` is (or contains) a registered tool name.
func (e *Engine) holdersOf(ctx context.Context, missing string) []string {
	holders := e.toolHolders(ctx)
	var out []string
	for name, who := range holders {
		if strings.Contains(strings.ToLower(missing), strings.ToLower(name)) {
			out = append(out, who...)
		}
	}
	return uniqueSorted(out)
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// toolScratchShare publishes the agent's private scratchpad as a temporary artifact, so notes can be handed to
// another agent by reference (the scratchpad itself stays private to the session).
func (e *Engine) toolScratchShare() *tools.Tool {
	return &tools.Tool{
		Name: "scratchpad_share", Category: "core", Base: true, Risk: tools.RiskWrite, Auto: true,
		Description: "Publish your private scratchpad as a temporary artifact and get its id, to hand your notes to a colleague or requester by reference (ask_colleague refs, report_blocked refs). The scratchpad itself stays private.",
		Params:      tools.Obj("", tools.Int("ttl_minutes", "how long it stays readable (default 120, max 1440)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				TTL int `json:"ttl_minutes"`
			}](raw)
			if err != nil {
				return "", err
			}
			text := strings.TrimSpace(e.Sessions.Scratch(ctx, env.SessionID))
			if text == "" {
				return "", errors.New("your scratchpad is empty: write the notes first with scratchpad_write")
			}
			if a.TTL <= 0 || a.TTL > 1440 {
				a.TTL = 120
			}
			if e.SaveHandoff == nil {
				return "", errors.New("artifacts are not available")
			}
			name := fmt.Sprintf("scratchpad-%s-%s.md", strings.ToLower(env.Agent), time.Now().Format("150405"))
			id, err := e.SaveHandoff(ctx, name, []byte(text), env.Agent, time.Duration(a.TTL)*time.Minute, env.Tainted, env.SessionID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Scratchpad shared as temporary artifact #%d (expires in %d min). Pass it in refs: [%d].", id, a.TTL, id), nil
		},
	}
}
