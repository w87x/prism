package agent

import (
	"context"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// The entry agent's prompt carries the roster on every turn, so it is bare names by group (no descriptions),
// and a delegation naming no agent is routed to the best-matching worker — never maintenance staff or the caller.
func TestCatalogIsCompactAndBestSpecialistRoutes(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	ps := NewProfileStore(d.Pool)
	for _, p := range []Profile{
		{Name: "Atlas", Role: RoleEntry, Enabled: true, Group: "General", Description: "entry"},
		{Name: "Scout", Role: RoleWorker, Enabled: true, Group: "Web", Description: "searches the web for release announcements and prices"},
		{Name: "Cipher", Role: RoleWorker, Enabled: true, Group: "Home", Description: "uploads and organizes files on the NAS"},
		{Name: "Oneiros", Role: RoleMaint, System: true, Enabled: true, Group: "Maintenance", Description: "dreams up release announcement briefings"},
	} {
		if _, err := ps.Save(ctx, p, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	e := NewEngine(Deps{Profiles: ps})
	cat := e.catalog(ctx)
	if !strings.Contains(cat, "- Web: Scout") || !strings.Contains(cat, "- Home: Cipher") {
		t.Fatalf("catalog not grouped by name: %q", cat)
	}
	if strings.Contains(cat, "searches the web") || strings.Contains(cat, "Atlas") {
		t.Fatalf("catalog must not carry descriptions or the entry agent: %q", cat)
	}
	got, err := e.bestSpecialist(ctx, "find release announcement prices on the web", "Atlas")
	if err != nil || got != "Scout" {
		t.Fatalf("bestSpecialist = %q, %v; want Scout", got, err)
	}
	if got, _ := e.bestSpecialist(ctx, "find release announcement prices on the web", "Scout"); got == "Scout" || got == "Oneiros" {
		t.Fatalf("must skip the caller and maintenance agents, got %q", got)
	}
}
