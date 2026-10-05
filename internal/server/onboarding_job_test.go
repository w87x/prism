package server

import (
	"testing"

	"prism/internal/onboarding"
)

// The generation lives in the server so the window can be closed: progress accumulates, a snapshot is an independent
// copy, finishing keeps the (ordered) drafts until they are applied, and a new generation replaces the old one.
func TestOnboardingJobSurvivesTheWindow(t *testing.T) {
	s := &Server{}
	if s.onboardingJob() != nil {
		t.Fatal("no job yet")
	}
	j := s.startOnboardingJob(7)
	j.progress(onboarding.Progress{Stage: "planning", Note: "Planning the team…", Total: 3})
	j.progress(onboarding.Progress{Stage: "writing", Draft: &onboarding.Draft{Name: "Alpha", Soul: "a"}, Total: 3})
	j.progress(onboarding.Progress{Stage: "writing", Draft: &onboarding.Draft{Name: "Beta", Soul: "b"}, Total: 3})
	j.progress(onboarding.Progress{Stage: "writing", Draft: &onboarding.Draft{Name: "Alpha", Soul: "a2"}, Total: 3}) // a redraft replaces, not duplicates
	snap := s.onboardingJob()
	if snap.Job != 7 || snap.Finished || snap.Total != 3 || len(snap.Drafts) != 2 || snap.Drafts[0].Soul != "a2" {
		t.Fatalf("running snapshot: %+v", snap)
	}
	snap.Drafts[0].Name = "mutated" // the caller's copy must not reach the stored state
	if s.onboardingJob().Drafts[0].Name != "Alpha" {
		t.Fatal("snapshot must be a copy")
	}
	j.finish([]onboarding.Draft{{Name: "Beta"}, {Name: "Alpha"}, {Name: "Gamma"}}, true, "")
	done := s.onboardingJob()
	if !done.Finished || !done.Generated || len(done.Drafts) != 3 || done.Drafts[0].Name != "Beta" || done.Stage != "finished" {
		t.Fatalf("finished snapshot keeps the final ordered drafts: %+v", done)
	}
	s.startOnboardingJob(8) // a newer generation replaces it
	if s.onboardingJob().Job != 8 || len(s.onboardingJob().Drafts) != 0 {
		t.Fatalf("a new job starts empty: %+v", s.onboardingJob())
	}
	s.clearOnboardingJob()
	if s.onboardingJob() != nil {
		t.Fatal("cleared once applied")
	}
}
