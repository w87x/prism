package memory

import (
	"strings"
	"testing"
	"time"
)

func TestCoverageDoesNotDescribeRelevantMatchAsProof(t *testing.T) {
	c := judgeCoverage(Need{Item: "server storage capacity"}, []Fact{{ID: 1, Text: "The server has 16 GB RAM", Confidence: 0.9}}, time.Now())
	if !strings.Contains(c.Note, "does not establish") {
		t.Fatalf("coverage must flag relevance limitation: %+v", c)
	}
	r := CoverageReport{Needs: []Coverage{c}}
	if got := r.Render(); !strings.Contains(got, "check that the facts actually answer") {
		t.Fatalf("coverage summary overstates a match: %q", got)
	}
}
