package agent

import (
	"context"
	"testing"

	"prism/internal/testutil"
)

// Bug: Search fed both agent_find/delegate (pick a specialist) and the scheduler's "owner was deleted, find
// a replacement" fallback, but only excluded the entry agent (Atlas) — a narrow single-purpose system agent
// (e.g. Oneiros, built only to draft daily briefings) could still win on a name/trait match and silently
// inherit an arbitrary watch or cron it was never built to handle. Caught via real intents that had landed
// on Oneiros this way.
func TestSearchExcludesMaintenanceAndEntryAgents(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	s := NewProfileStore(d.Pool)

	for _, p := range []Profile{
		{Name: "Atlas", Role: RoleEntry, Enabled: true, Description: "the dreamer of watches and releases"},
		{Name: "Oneiros", Role: RoleMaint, System: true, Enabled: true, Description: "the dreamer, drafts morning briefings about releases"},
		{Name: "Scout", Role: RoleWorker, Enabled: true, Description: "watches for release announcements"},
	} {
		if _, err := s.Save(ctx, p, "seed"); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := s.Search(ctx, "release announcement watch dreamer", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Name == "Atlas" || h.Name == "Oneiros" {
			t.Fatalf("Search returned %q (role=%q) — entry and maintenance agents must never be offered as a delegation/replacement target: %+v", h.Name, h.Role, hits)
		}
	}
	if len(hits) != 1 || hits[0].Name != "Scout" {
		t.Fatalf("expected only Scout, got %+v", hits)
	}
}
