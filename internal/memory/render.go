package memory

import (
	"fmt"
	"sort"
	"strings"
)

// FactFlags is how a fact's standing is shown to an agent next to its text: what a reader needs to weigh it
// (disputed, unverified, vouched for, about to expire…), not just the sentence. Used by memory_find and by the
// auto-recalled context packet, so both say the same thing.
func FactFlags(f Fact) string {
	var fl []string
	if f.ValidTo != nil {
		word := "outdated"
		switch f.Status {
		case StatusSuperseded:
			word = "replaced"
		case StatusExpired:
			word = "expired"
		}
		fl = append(fl, word+" since "+f.ValidTo.Format("2006-01-02"))
	}
	switch {
	case f.Status == StatusContested:
		fl = append(fl, "disputed: another fact contradicts it")
	case f.Status == StatusProposed:
		fl = append(fl, "proposed, not yet approved")
	}
	switch {
	case f.Confirmation == ConfirmUser:
		fl = append(fl, "confirmed by the user")
	case len(f.Origins) >= 2:
		fl = append(fl, fmt.Sprintf("confirmed by %d sites", len(f.Origins)))
	case f.Confirmation == ConfirmMulti:
		fl = append(fl, "confirmed by independent sources")
	case f.Confidence < 0.5:
		fl = append(fl, "unverified")
	}
	if f.ExpiresAt != nil && f.ValidTo == nil {
		fl = append(fl, "may go stale after "+f.ExpiresAt.Format("2006-01-02"))
	}
	if f.Kind == ConclusionKind {
		c := fmt.Sprintf("conclusion from %d facts, %.0f%% sure", f.Proof, f.Confidence*100)
		if f.Stale {
			c += "; some evidence was retired — needs review"
		}
		fl = append(fl, c)
	}
	if f.Via != nil {
		fl = append(fl, fmt.Sprintf("linked to #%d", *f.Via))
	}
	if len(fl) == 0 {
		return ""
	}
	return " [" + strings.Join(fl, "; ") + "]"
}

// ── bundling ────────────────────────────────────────────────────────────────────────────────────────────────

// Bundle is facts that say the same kind of thing about the same subject, rendered as one line:
// "The user owns Synology DS923, Mac Studio and iPhone 17" instead of three. Every fact keeps its own id,
// evidence and lifecycle; a bundle is only a view.
type Bundle struct {
	Facts []Fact
}

func normKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func qualKey(q map[string]string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + q[k] + ";")
	}
	return b.String()
}

// bundleKey is empty for a fact that cannot be bundled (no structure). Facts bundle only when subject,
// predicate, qualifiers, bank and standing flags all agree — a weak or disputed fact is never folded into a
// strong one's line.
func bundleKey(f Fact) string {
	if f.Subject == "" || f.Predicate == "" || f.Object == "" || f.Kind == ConclusionKind || f.Via != nil {
		return ""
	}
	return strings.Join([]string{f.Bank, normKey(f.Subject), normKey(f.Predicate), qualKey(f.Qualifiers), FactFlags(f)}, "\x1f")
}

// BundleFacts groups the facts (keeping the order of first appearance).
func BundleFacts(facts []Fact) []Bundle {
	var out []Bundle
	at := map[string]int{}
	for _, f := range facts {
		k := bundleKey(f)
		if k != "" {
			if i, ok := at[k]; ok {
				out[i].Facts = append(out[i].Facts, f)
				continue
			}
			at[k] = len(out)
		}
		out = append(out, Bundle{Facts: []Fact{f}})
	}
	return out
}

// Line renders the bundle as one recall line.
func (b Bundle) Line() string {
	first := b.Facts[0]
	if len(b.Facts) == 1 {
		return fmt.Sprintf("- [fact #%d · %s] %s%s\n", first.ID, first.Bank, strings.TrimSpace(first.Text), FactFlags(first))
	}
	ids := make([]string, len(b.Facts))
	objs := make([]string, len(b.Facts))
	for i, f := range b.Facts {
		ids[i], objs[i] = fmt.Sprintf("#%d", f.ID), f.Object
	}
	list := objs[0]
	if n := len(objs); n > 1 {
		list = strings.Join(objs[:n-1], ", ") + " and " + objs[n-1]
	}
	return fmt.Sprintf("- [facts %s · %s] %s %s %s%s\n", strings.Join(ids, " "), first.Bank, first.Subject, first.Predicate, list, FactFlags(first))
}
