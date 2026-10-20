package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Core memory: a handful of short labelled blocks that are ALWAYS in every agent's prompt (Letta's "memory blocks"). The rest of
// memory has to be searched for; these are what every agent should simply know — who the user is, how they like things done,
// what they are on right now. The user edits them on the Memory page; agents with the core_memory_update tool may keep them
// current, but never on the strength of web content, and every change is kept in a history.

const (
	maxCoreBlocks   = 8
	maxCoreBlockLen = 800
	maxCoreTotal    = 2400 // what is put into a prompt, in characters
	// CharterLabel is the block that holds the team charter: it is shown under its own heading and may be longer than other blocks.
	CharterLabel  = "charter"
	maxCharterLen = 1600
)

var coreLabelRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,23}$`)

// CoreBlock is one block.
type CoreBlock struct {
	Label       string    `json:"label"`
	Text        string    `json:"text"`
	Description string    `json:"description"`
	Position    int       `json:"position"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CoreBlocks lists the blocks in display order.
func (s *Service) CoreBlocks(ctx context.Context) ([]CoreBlock, error) {
	rows, err := s.db.Query(ctx, `SELECT label,text,description,position,updated_by,updated_at FROM core_blocks ORDER BY position, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoreBlock{}
	for rows.Next() {
		var b CoreBlock
		if err := rows.Scan(&b.Label, &b.Text, &b.Description, &b.Position, &b.UpdatedBy, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CoreText is the core memory as a prompt section ("" when every block is empty).
func (s *Service) CoreText(ctx context.Context) string {
	bs, err := s.CoreBlocks(ctx)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range bs {
		t := strings.TrimSpace(b.Text)
		if t == "" || b.Label == CharterLabel { // the charter has its own section (CharterText)
			continue
		}
		if sb.Len()+len(t) > maxCoreTotal {
			break
		}
		fmt.Fprintf(&sb, "- %s: %s\n", strings.ReplaceAll(b.Label, "_", " "), strings.Join(strings.Fields(t), " "))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// CharterText is the team charter ("" when there is none), for the prompt section of its own.
func (s *Service) CharterText(ctx context.Context) string {
	var t string
	if s.db.QueryRow(ctx, `SELECT text FROM core_blocks WHERE label=$1`, CharterLabel).Scan(&t) != nil {
		return ""
	}
	t = strings.TrimSpace(t)
	if r := []rune(t); len(r) > maxCharterLen {
		t = string(r[:maxCharterLen])
	}
	return t
}

// SetCoreBlock writes a block (creating it when new), keeping what it said before in the history. description is kept when empty.
func (s *Service) SetCoreBlock(ctx context.Context, label, text, description, by string) error {
	label = strings.ToLower(strings.TrimSpace(label))
	if !coreLabelRe.MatchString(label) {
		return errors.New("a block's label is 2-24 lowercase letters, digits or underscores, starting with a letter")
	}
	text = strings.TrimSpace(text)
	limit := maxCoreBlockLen
	if label == CharterLabel {
		limit = maxCharterLen
	}
	if len([]rune(text)) > limit {
		return fmt.Errorf("a block holds at most %d characters — core memory is for the few things every agent must always know; put the rest in ordinary memory", limit)
	}
	var old string
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT true, text FROM core_blocks WHERE label=$1`, label).Scan(&exists, &old); err != nil {
		exists = false
	}
	if !exists {
		var n int
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM core_blocks`).Scan(&n)
		if n >= maxCoreBlocks {
			return fmt.Errorf("there are already %d blocks; edit or delete one", n)
		}
		_, err := s.db.Exec(ctx, `INSERT INTO core_blocks(label,text,description,position,updated_by) VALUES($1,$2,$3,(SELECT COALESCE(max(position),0)+1 FROM core_blocks),$4)`, label, text, strings.TrimSpace(description), by)
		if err == nil {
			s.audit(ctx, by, "core_set", 0, label)
			s.changed()
		}
		return err
	}
	if old == text && description == "" {
		return nil
	}
	if strings.TrimSpace(old) != "" {
		_, _ = s.db.Exec(ctx, `INSERT INTO core_block_history(label,text,updated_by) VALUES($1,$2,(SELECT updated_by FROM core_blocks WHERE label=$1))`, label, old)
	}
	_, err := s.db.Exec(ctx, `UPDATE core_blocks SET text=$2, description=CASE WHEN $3='' THEN description ELSE $3 END, updated_by=$4, updated_at=now() WHERE label=$1`, label, text, strings.TrimSpace(description), by)
	if err == nil {
		s.audit(ctx, by, "core_set", 0, label)
		s.changed()
	}
	return err
}

// DeleteCoreBlock removes a block (its history stays).
func (s *Service) DeleteCoreBlock(ctx context.Context, label string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM core_blocks WHERE label=$1`, strings.ToLower(strings.TrimSpace(label)))
	if err == nil {
		s.changed()
	}
	return err
}

// CoreHistory lists a block's earlier texts, newest first.
func (s *Service) CoreHistory(ctx context.Context, label string) ([]CoreBlock, error) {
	rows, err := s.db.Query(ctx, `SELECT label,text,updated_by,at FROM core_block_history WHERE label=$1 ORDER BY id DESC LIMIT 20`, label)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoreBlock{}
	for rows.Next() {
		var b CoreBlock
		if err := rows.Scan(&b.Label, &b.Text, &b.UpdatedBy, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
