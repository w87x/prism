package memory

import (
	"fmt"
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
