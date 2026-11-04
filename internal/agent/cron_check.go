package agent

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
)

var cronClaim = regexp.MustCompile(`(?i)\bcron\s*#(\d+)`)
var cronCreated = regexp.MustCompile(`(?i)^\s*cron\s*#(\d+) created;`)

// missingCreatedCron only checks a cron ID returned by this session's successful
// cron_create and claimed in the report. Existence does not prove the schedule
// or standing instruction match the user's intent; model review still follows.
func (e *Engine) missingCreatedCron(ctx context.Context, sessionID int64, result string) (string, error) {
	if e.DB == nil || e.Sessions == nil || sessionID == 0 {
		return "", nil
	}
	claimed := map[string]bool{}
	for _, m := range cronClaim.FindAllStringSubmatch(result, -1) {
		claimed[m[1]] = true
	}
	if len(claimed) == 0 {
		return "", nil
	}
	msgs, err := e.Sessions.Messages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	calls := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if c.Name == "cron_create" {
				calls[c.ID] = true
			}
		}
	}
	for _, m := range msgs {
		if m.Role != "tool" || !calls[m.ToolCallID] {
			continue
		}
		for _, match := range cronCreated.FindAllStringSubmatch(m.Content, -1) {
			if !claimed[match[1]] {
				continue
			}
			id, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				continue
			}
			var exists bool
			if err = e.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crons WHERE id=$1)`, id).Scan(&exists); err != nil {
				return "", err
			}
			if !exists {
				return fmt.Sprintf("reported cron #%d no longer exists", id), nil
			}
		}
	}
	return "", nil
}
