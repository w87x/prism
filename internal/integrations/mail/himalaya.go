package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Himalaya runs mail through the himalaya CLI (https://github.com/pimalaya/himalaya), configured by the user
// in its own config file. It brings OAuth and per-provider quirks that PRISM's own client does not handle.
type Himalaya struct {
	Account string // name of the account in himalaya's config
	Bin     string // default "himalaya"
	Config  string // optional -c path (tests)
	Timeout time.Duration
}

func (h *Himalaya) bin() string {
	if h.Bin != "" {
		return h.Bin
	}
	return "himalaya"
}

// HimalayaAvailable reports whether the CLI is installed.
func HimalayaAvailable() bool {
	_, err := exec.LookPath("himalaya")
	return err == nil
}

// himalayaError turns the CLI's JSON error document (or plain text) into a sentence.
func himalayaError(out []byte, stderr string, err error) error {
	var e struct {
		Error   string   `json:"error"`
		Sources []string `json:"sources"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &e) == nil && e.Error != "" {
		msg := e.Error
		if len(e.Sources) > 0 {
			msg += ": " + strings.TrimSpace(e.Sources[len(e.Sources)-1])
		}
		return fmt.Errorf("himalaya: %s", msg)
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("himalaya: %s", tailStr(s, 300))
	}
	return fmt.Errorf("himalaya: %w", err)
}

func tailStr(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

func (h *Himalaya) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if h.Account == "" {
		return nil, errors.New("no himalaya account selected for this mail account")
	}
	limit := h.Timeout
	if limit <= 0 {
		limit = 90 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	full := []string{"--json", "-a", h.Account}
	if h.Config != "" {
		full = append([]string{"-c", h.Config}, full...)
	}
	cmd := exec.CommandContext(cctx, h.bin(), append(full, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return nil, fmt.Errorf("himalaya did not answer within %s", limit)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("himalaya is not installed (brew install himalaya)")
		}
		return nil, himalayaError(stdout.Bytes(), stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

func (h *Himalaya) Folders(ctx context.Context) ([]Folder, error) {
	out, err := h.run(ctx, nil, "mailbox", "list", "--counts")
	if err != nil {
		return nil, err
	}
	var r struct {
		Mailboxes []struct {
			Name   string `json:"name"`
			Total  *int   `json:"total"`
			Unread *int   `json:"unread"`
		} `json:"mailboxes"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("unexpected himalaya output: %w", err)
	}
	var fs []Folder
	for _, m := range r.Mailboxes {
		f := Folder{Name: m.Name}
		if m.Total != nil {
			f.Total = *m.Total
		}
		if m.Unread != nil {
			f.Unread = *m.Unread
		}
		switch strings.ToLower(m.Name) {
		case "inbox":
			f.Role = "inbox"
		case "drafts", "draft":
			f.Role = "drafts"
		case "sent", "sent items", "sent messages":
			f.Role = "sent"
		}
		fs = append(fs, f)
	}
	return fs, nil
}

// himalayaQuery translates a Query into himalaya's search DSL (separate argv items, so patterns with spaces
// need no quoting and nothing is parsed by a shell).
func himalayaQuery(q Query) []string {
	var parts []string
	add := func(cond ...string) {
		if len(parts) > 0 {
			parts = append(parts, "and")
		}
		parts = append(parts, cond...)
	}
	// himalaya's query language takes ONE word per condition (quotes are matched literally), so a phrase becomes
	// one condition per word, all of which must match; its own keywords are dropped from patterns.
	reserved := map[string]bool{"and": true, "or": true, "not": true, "order": true, "by": true, "asc": true, "desc": true}
	words := func(field, s string) {
		for _, w := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(s)) {
			if !reserved[strings.ToLower(w)] {
				add(field, w)
			}
		}
	}
	words("from", q.From)
	words("to", q.To)
	words("subject", q.Subject)
	words("body", q.Text)
	if q.Unread {
		add("not", "flag", "seen")
	}
	if !q.Since.IsZero() {
		add("after", q.Since.AddDate(0, 0, -1).Format("2006-01-02")) // "after" is exclusive
	}
	return parts
}

func (h *Himalaya) Search(ctx context.Context, q Query) ([]Envelope, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	folder := q.Folder
	if folder == "" {
		folder = "INBOX"
	}
	args := []string{"envelope", "search", "-m", folder, "-s", strconv.Itoa(limit)}
	dsl := himalayaQuery(q)
	if len(dsl) == 0 {
		args = []string{"envelope", "list", "-m", folder, "-s", strconv.Itoa(limit)}
	} else {
		args = append(args, dsl...)
	}
	out, err := h.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var r struct {
		Envelopes []struct {
			ID      string            `json:"id"`
			Subject string            `json:"subject"`
			From    []Addr            `json:"from"`
			To      []Addr            `json:"to"`
			Date    string            `json:"date"`
			Flags   []json.RawMessage `json:"flags"`
			Attach  *bool             `json:"has-attachment"`
			MsgID   string            `json:"message-id"`
		} `json:"envelopes"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("unexpected himalaya output: %w", err)
	}
	var es []Envelope
	for _, e := range r.Envelopes {
		env := Envelope{ID: e.ID, Folder: folder, From: e.From, To: e.To, Subject: e.Subject, MessageID: strings.Trim(e.MsgID, "<>"), Unread: true}
		if t, err := time.Parse(time.RFC3339, e.Date); err == nil {
			env.Date = t
		}
		for _, f := range e.Flags {
			s := strings.ToLower(string(f))
			if strings.Contains(s, "seen") {
				env.Unread = false
			}
			if strings.Contains(s, "flagged") {
				env.Flagged = true
			}
		}
		if e.Attach != nil {
			env.HasAttach = *e.Attach
		}
		es = append(es, env)
	}
	if !q.Before.IsZero() { // himalaya has no "before": filter here
		kept := es[:0]
		for _, e := range es {
			if e.Date.IsZero() || e.Date.Before(q.Before) {
				kept = append(kept, e)
			}
		}
		es = kept
	}
	return es, nil
}

func (h *Himalaya) Fetch(ctx context.Context, folder, id string) ([]byte, error) {
	if folder == "" {
		folder = "INBOX"
	}
	if strings.TrimSpace(id) == "" || strings.HasPrefix(strings.TrimSpace(id), "-") {
		return nil, fmt.Errorf("%q is not a message id (take it from mail_search)", id)
	}
	out, err := h.run(ctx, nil, "message", "read", "-m", folder, "--raw", id)
	if err != nil {
		return nil, err
	}
	var r struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("unexpected himalaya output: %w", err)
	}
	return []byte(r.Message), nil
}

func (h *Himalaya) Append(ctx context.Context, folder string, raw []byte, flags ...string) error {
	args := []string{"message", "add", "-m", folder}
	for _, f := range flags {
		switch strings.ToLower(strings.TrimPrefix(f, "\\")) {
		case "seen", "answered", "flagged", "draft":
			args = append(args, "-f", strings.ToLower(strings.TrimPrefix(f, "\\")))
		}
	}
	_, err := h.run(ctx, raw, args...)
	return err
}

func (h *Himalaya) Send(ctx context.Context, from string, to []string, raw []byte) error {
	// himalaya reads the sender and the recipients from the message's own headers
	_, err := h.run(ctx, raw, "message", "send")
	return err
}

func (h *Himalaya) SetSeen(ctx context.Context, folder, id string, seen bool) error {
	if folder == "" {
		folder = "INBOX"
	}
	verb := "add"
	if !seen {
		verb = "remove"
	}
	_, err := h.run(ctx, nil, "flag", verb, "-m", folder, "-f", "seen", id)
	return err
}

// HimalayaAccounts lists the accounts in the user's himalaya config (for the settings dropdown).
func HimalayaAccounts(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "himalaya", "--json", "account", "list").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("himalaya is not installed")
		}
		return nil, himalayaError(out, "", err)
	}
	var r struct {
		Accounts []struct {
			Name string `json:"name"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		// tolerate a bare array of accounts
		var arr []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(out, &arr) != nil {
			return nil, fmt.Errorf("unexpected himalaya output: %w", err)
		}
		for _, a := range arr {
			r.Accounts = append(r.Accounts, struct {
				Name string `json:"name"`
			}{a.Name})
		}
	}
	var names []string
	for _, a := range r.Accounts {
		names = append(names, a.Name)
	}
	return names, nil
}
