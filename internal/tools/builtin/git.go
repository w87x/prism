package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"prism/internal/tools"
)

var safeRef = regexp.MustCompile(`^[A-Za-z0-9._/@{}~^-]+$`)

func checkRef(r string) error {
	if r == "" || strings.HasPrefix(r, "-") || !safeRef.MatchString(r) {
		return fmt.Errorf("%q is not a valid git ref", r)
	}
	return nil
}

func (d Deps) gitDir(ctx context.Context, env *tools.Env, tool, dir string, write bool) (string, error) {
	if dir == "" {
		dir = "."
	}
	p, err := d.resolve(ctx, dir)
	if err != nil {
		return "", err
	}
	if write {
		if err := d.canWrite(ctx, p); err != nil {
			return "", fmt.Errorf("%w — coding work happens in a workspace (workspace_open) so your own checkout stays untouched", err)
		}
	} else if err := d.canReadTurn(ctx, env, tool, p); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
		if out, gerr := runGit(ctx, p, 10*time.Second, "", "rev-parse", "--git-dir"); gerr != nil || strings.TrimSpace(out) == "" {
			return "", fmt.Errorf("%s is not inside a git repository", p)
		}
	}
	return p, nil
}

func registerGit(reg *tools.Registry, d Deps) {
	dirProp := tools.Str("dir", "repository or workspace directory (default: the workspace)")
	reg.Register(
		&tools.Tool{
			Name: "git_status", Category: "git", Risk: tools.RiskRead,
			Description: "Show the current branch and the changed / untracked files of a git repository.",
			Params:      tools.Obj("", dirProp),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, _ := tools.Decode[struct{ Dir string }](raw)
				p, err := d.gitDir(ctx, env, "git_status", a.Dir, false)
				if err != nil {
					return "", err
				}
				out, err := runGit(ctx, p, 30*time.Second, "", "status", "--short", "--branch")
				return strings.TrimSpace(out), err
			},
		},
		&tools.Tool{
			Name: "git_diff", Category: "git", Risk: tools.RiskRead,
			Description: "Show changes: the working tree against HEAD by default; staged=true for the index; ref to compare with a commit/branch (e.g. main); path to limit; stat=true for a summary only.",
			Params: tools.Obj("", dirProp, tools.Str("ref", "commit or branch to compare against"), tools.Bool("staged", "only staged changes"),
				tools.Str("path", "limit to this path"), tools.Bool("stat", "summary only")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Dir, Ref, Path string
					Staged, Stat   bool
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := d.gitDir(ctx, env, "git_diff", a.Dir, false)
				if err != nil {
					return "", err
				}
				args := []string{"diff", "--no-color"}
				if a.Stat {
					args = append(args, "--stat")
				}
				if a.Staged {
					args = append(args, "--cached")
				}
				if a.Ref != "" {
					if err := checkRef(a.Ref); err != nil {
						return "", err
					}
					args = append(args, a.Ref)
				}
				if a.Path != "" {
					args = append(args, "--", a.Path)
				}
				out, err := runGit(ctx, p, time.Minute, "", args...)
				if strings.TrimSpace(out) == "" && err == nil {
					return "(no changes)", nil
				}
				return out, err
			},
		},
		&tools.Tool{
			Name: "git_log", Category: "git", Risk: tools.RiskRead,
			Description: "Recent commits (hash, author, date, subject); optionally for one path or a range like main..HEAD.",
			Params:      tools.Obj("", dirProp, tools.Int("n", "how many (default 15, max 100)"), tools.Str("path", "only commits touching this path"), tools.Str("range", "e.g. main..HEAD")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Dir, Path, Range string
					N                int
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := d.gitDir(ctx, env, "git_log", a.Dir, false)
				if err != nil {
					return "", err
				}
				if a.N <= 0 || a.N > 100 {
					a.N = 15
				}
				args := []string{"log", fmt.Sprintf("-%d", a.N), "--date=short", "--pretty=format:%h %ad %an  %s"}
				if a.Range != "" {
					if err := checkRef(a.Range); err != nil {
						return "", err
					}
					args = append(args, a.Range)
				}
				if a.Path != "" {
					args = append(args, "--", a.Path)
				}
				return runGit(ctx, p, 30*time.Second, "", args...)
			},
		},
		&tools.Tool{
			Name: "git_show", Category: "git", Risk: tools.RiskRead,
			Description: "Show one commit (message and diff), or one file as of a revision when path is given.",
			Params:      tools.Obj("rev", dirProp, tools.Str("rev", "commit, branch or tag"), tools.Str("path", "show this file at that revision instead")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Dir, Rev, Path string }](raw)
				if err != nil {
					return "", err
				}
				p, err := d.gitDir(ctx, env, "git_show", a.Dir, false)
				if err != nil {
					return "", err
				}
				if err := checkRef(a.Rev); err != nil {
					return "", err
				}
				arg := a.Rev
				if a.Path != "" {
					arg = a.Rev + ":" + a.Path
				}
				return runGit(ctx, p, 30*time.Second, "", "show", "--no-color", arg)
			},
		},
		&tools.Tool{
			Name: "git_branch", Category: "git", Risk: tools.RiskWrite,
			Description: "List branches (action=list), create one from HEAD and switch to it (action=create), or switch to an existing one (action=switch). Creating and switching only work inside the workspace or configured write roots.",
			Params:      tools.Obj("", dirProp, tools.Enum("action", "list (default), create or switch", "list", "create", "switch"), tools.Str("name", "branch name")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Dir, Action, Name string }](raw)
				if err != nil {
					return "", err
				}
				if a.Action == "" || a.Action == "list" {
					p, err := d.gitDir(ctx, env, "git_branch", a.Dir, false)
					if err != nil {
						return "", err
					}
					return runGit(ctx, p, 30*time.Second, "", "branch", "-vv", "--no-color")
				}
				p, err := d.gitDir(ctx, env, "git_branch", a.Dir, true)
				if err != nil {
					return "", err
				}
				if err := checkRef(a.Name); err != nil {
					return "", err
				}
				if a.Action == "create" {
					return runGit(ctx, p, 30*time.Second, "", "switch", "-c", a.Name)
				}
				return runGit(ctx, p, 30*time.Second, "", "switch", a.Name)
			},
		},
		&tools.Tool{
			Name: "git_commit", Category: "git", Risk: tools.RiskWrite,
			Description: "Commit changes (all tracked and new files with all=true, otherwise what is already staged, or the given paths). Only inside the workspace or configured write roots. Write a clear message: what and why.",
			Params:      tools.Obj("message", dirProp, tools.Str("message", "commit message"), tools.Bool("all", "stage everything first"), tools.StrList("paths", "stage these paths first")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Dir, Message string
					All          bool
					Paths        []string
				}](raw)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(a.Message) == "" {
					return "", errors.New("a commit message is required")
				}
				p, err := d.gitDir(ctx, env, "git_commit", a.Dir, true)
				if err != nil {
					return "", err
				}
				if a.All {
					if out, err := runGit(ctx, p, time.Minute, "", "add", "-A"); err != nil {
						return "", fmt.Errorf("%v: %s", err, out)
					}
				} else if len(a.Paths) > 0 {
					if out, err := runGit(ctx, p, time.Minute, "", append([]string{"add", "--"}, a.Paths...)...); err != nil {
						return "", fmt.Errorf("%v: %s", err, out)
					}
				}
				return runGit(ctx, p, time.Minute, "", "-c", "user.name=PRISM", "-c", "user.email=prism@localhost", "commit", "-m", a.Message)
			},
		},
		&tools.Tool{
			Name: "git_push", Category: "git", Risk: tools.RiskExec,
			Description: "Push the current branch to a remote (default origin), setting its upstream. Never forces. Needed before gh_pr_create.",
			Params:      tools.Obj("", dirProp, tools.Str("remote", "remote name (default origin)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Dir, Remote string }](raw)
				if err != nil {
					return "", err
				}
				p, err := d.gitDir(ctx, env, "git_push", a.Dir, true)
				if err != nil {
					return "", err
				}
				if a.Remote == "" {
					a.Remote = "origin"
				}
				if err := checkRef(a.Remote); err != nil {
					return "", err
				}
				if err := tools.ConfirmIfTainted(ctx, env, "git_push", p); err != nil {
					return "", err
				}
				return runGit(ctx, p, 3*time.Minute, "", "push", "--set-upstream", a.Remote, "HEAD")
			},
		},
	)
}
