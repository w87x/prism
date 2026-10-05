package memory

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An unapproved proposal must never become input to reflection, analysis, entity extraction or the digest — those
// read "current facts" with their own SQL, and each such query has to exclude status='proposed'. This guard fails
// when a new query over live facts forgets to.
func TestQueriesOverLiveFactsExcludeProposals(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	sqlLit := regexp.MustCompile("(?s)`[^`]*`")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, lit := range sqlLit.FindAllString(string(src), -1) {
			l := strings.ToLower(lit)
			if !strings.Contains(l, "kind='fact'") || !strings.Contains(l, "valid_to is null") {
				continue
			}
			if strings.Contains(l, "update memory_facts") || strings.Contains(l, "proposed") || strings.Contains(l, "valid_to is not null") {
				continue
			}
			t.Errorf("%s: query over live facts does not exclude proposals:\n%s", f, strings.TrimSpace(lit))
		}
	}
}
