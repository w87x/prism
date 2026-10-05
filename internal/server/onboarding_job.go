package server

import (
	"sync"

	"prism/internal/onboarding"
)

// obJob is the state of one onboarding team generation. The generation runs in the server, not in the page, so the
// page can be closed while a slow local model writes the team; what it produced is kept here until it is applied
// (or replaced by a newer generation) and a reopened onboarding window fetches it with onboarding.job.
type obJob struct {
	mu        sync.Mutex
	Job       int64              `json:"job"`
	Stage     string             `json:"stage"`
	Note      string             `json:"note"`
	Total     int                `json:"total"`
	Drafts    []onboarding.Draft `json:"drafts"`
	Finished  bool               `json:"finished"`
	Generated bool               `json:"generated"` // a model wrote it (false: built-in templates stood in)
	Error     string             `json:"error,omitempty"`
}

func (s *Server) startOnboardingJob(id int64) *obJob {
	j := &obJob{Job: id, Stage: "planning", Note: "Starting…"}
	s.obMu.Lock()
	s.obJob = j
	s.obMu.Unlock()
	return j
}

func (j *obJob) progress(p onboarding.Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if p.Stage != "" {
		j.Stage = p.Stage
	}
	if p.Note != "" {
		j.Note = p.Note
	}
	if p.Total > 0 {
		j.Total = p.Total
	}
	if p.Draft != nil {
		for i := range j.Drafts {
			if j.Drafts[i].Name == p.Draft.Name {
				j.Drafts[i] = *p.Draft
				return
			}
		}
		j.Drafts = append(j.Drafts, *p.Draft)
	}
}

func (j *obJob) finish(drafts []onboarding.Draft, generated bool, errNote string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Finished, j.Generated, j.Stage, j.Note, j.Drafts, j.Error = true, generated, "finished", "", drafts, errNote
}

// onboardingJob returns a copy of the current job state (nil when there is none).
func (s *Server) onboardingJob() *obJob {
	s.obMu.Lock()
	j := s.obJob
	s.obMu.Unlock()
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return &obJob{Job: j.Job, Stage: j.Stage, Note: j.Note, Total: j.Total, Drafts: append([]onboarding.Draft(nil), j.Drafts...),
		Finished: j.Finished, Generated: j.Generated, Error: j.Error}
}

func (s *Server) clearOnboardingJob() {
	s.obMu.Lock()
	s.obJob = nil
	s.obMu.Unlock()
}
