package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"
)

var artifactClaim = regexp.MustCompile(`(?i)\bartifact\s*#(\d+)`)

// missingSavedArtifact checks only IDs both reported by artifact_save in this
// session and claimed in the final answer. It is a negative check: existence
// alone never proves that the artifact satisfies the task.
func (e *Engine) missingSavedArtifact(ctx context.Context, sessionID int64, result string) (string, error) {
	if e.DB == nil || e.Sessions == nil || sessionID == 0 {
		return "", nil
	}
	claimed := map[string]bool{}
	for _, m := range artifactClaim.FindAllStringSubmatch(result, -1) {
		claimed[m[1]] = true
	}
	if len(claimed) == 0 {
		return "", nil
	}
	msgs, err := e.Sessions.Messages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	savedCalls := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if c.Name == "artifact_save" {
				savedCalls[c.ID] = true
			}
		}
	}
	for _, m := range msgs {
		if m.Role != "tool" || !savedCalls[m.ToolCallID] {
			continue
		}
		for _, match := range artifactClaim.FindAllStringSubmatch(m.Content, -1) {
			if !claimed[match[1]] {
				continue
			}
			id, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				continue
			}
			var path string
			err = e.DB.QueryRow(ctx, `SELECT path FROM artifacts WHERE id=$1 AND (expires_at IS NULL OR expires_at>now())`, id).Scan(&path)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Sprintf("reported artifact #%d is missing or expired", id), nil
			}
			if err != nil {
				return "", err
			}
			if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return fmt.Sprintf("reported artifact #%d has no file on disk", id), nil
			} else if err != nil {
				return "", err
			}
		}
	}
	return "", nil
}
