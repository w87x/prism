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

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/tools"
)

// A code workspace is a git worktree on its own branch, created under the PRISM data dir. The agent edits and
// commits there — never in the user's checkout — and the user reviews the diff, then keeps the branch, applies
// it to their checkout as staged changes, or throws it away.
type Workspace struct {
	ID        int64     `json:"id"`
	Repo      string    `json:"repo"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Base      string    `json:"base"`
	Agent     string    `json:"agent"`
	TaskID    *int64    `json:"task_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	Stat      string    `json:"stat"` // changed files vs base (open workspaces)
}

const wsCols = `id,repo,path,branch,base,agent,task_id,status,created_at`

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 30 {
		s = s[:30]
	}
	if s == "" {
		s = "work"
	}
	return s
}

func getWorkspace(ctx context.Context, db *pgxpool.Pool, id int64) (Workspace, error) {
	var w Workspace
	err := db.QueryRow(ctx, `SELECT `+wsCols+` FROM code_workspaces WHERE id=$1`, id).
		Scan(&w.ID, &w.Repo, &w.Path, &w.Branch, &w.Base, &w.Agent, &w.TaskID, &w.Status, &w.CreatedAt)
	if err != nil {
		return w, fmt.Errorf("workspace #%d does not exist", id)
	}
	return w, nil
}

// Workspaces lists the workspaces, newest first; open ones carry a summary of what changed.
func Workspaces(ctx context.Context, db *pgxpool.Pool) ([]Workspace, error) {
	rows, err := db.Query(ctx, `SELECT `+wsCols+` FROM code_workspaces ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Repo, &w.Path, &w.Branch, &w.Base, &w.Agent, &w.TaskID, &w.Status, &w.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	rows.Close()
	for i := range out {
		if out[i].Status == "open" {
			if _, err := os.Stat(out[i].Path); err == nil {
				_, _ = runGit(ctx, out[i].Path, 30*time.Second, "", "add", "-A")
				out[i].Stat, _ = runGit(ctx, out[i].Path, 30*time.Second, "", "diff", "--cached", "--stat", "--no-color", out[i].Base)
				out[i].Stat = strings.TrimSpace(out[i].Stat)
			}
		} else if out[i].Status == "kept" {
			out[i].Stat, _ = runGit(ctx, out[i].Repo, 30*time.Second, "", "diff", "--stat", "--no-color", out[i].Base+".."+out[i].Branch)
			out[i].Stat = strings.TrimSpace(out[i].Stat)
		}
	}
	return out, nil
}

// OpenWorkspace creates a worktree of repo on a new branch. Uncommitted changes in the repo are not part of it.
func (d Deps) OpenWorkspace(ctx context.Context, repo, name, agent string, taskID int64) (*Workspace, string, error) {
	rp, err := d.resolve(ctx, repo)
	if err != nil {
		return nil, "", err
	}
	if err := d.canRead(ctx, rp); err != nil {
		return nil, "", err
	}
	top, err := runGit(ctx, rp, 15*time.Second, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "", fmt.Errorf("%s is not a git repository", rp)
	}
	top = strings.TrimSpace(top)
	base, err := runGit(ctx, top, 15*time.Second, "", "rev-parse", "HEAD")
	if err != nil {
		return nil, "", errors.New("the repository has no commits yet: make an initial commit first")
	}
	base = strings.TrimSpace(base)
	dirty, _ := runGit(ctx, top, 15*time.Second, "", "status", "--porcelain")
	if name == "" {
		name = filepath.Base(top)
	}
	var id int64
	var tid any
	if taskID != 0 {
		tid = taskID
	}
	if err := d.DB.QueryRow(ctx, `INSERT INTO code_workspaces(repo,path,branch,base,agent,task_id) VALUES($1,'','',$2,$3,$4) RETURNING id`, top, base, agent, tid).Scan(&id); err != nil {
		return nil, "", err
	}
	s := fmt.Sprintf("%s-%d", slug(name), id)
	path := filepath.Join(d.DataDir, "work", "worktrees", s)
	branch := "prism/" + s
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", err
	}
	if out, err := runGit(ctx, top, time.Minute, "", "worktree", "add", "-b", branch, path, base); err != nil {
		_, _ = d.DB.Exec(ctx, `DELETE FROM code_workspaces WHERE id=$1`, id)
		return nil, "", fmt.Errorf("could not create the workspace: %s", strings.TrimSpace(out))
	}
	if _, err := d.DB.Exec(ctx, `UPDATE code_workspaces SET path=$2, branch=$3 WHERE id=$1`, id, path, branch); err != nil {
		return nil, "", err
	}
	w, err := getWorkspace(ctx, d.DB, id)
	note := ""
	if strings.TrimSpace(dirty) != "" {
		note = " Note: the repository has uncommitted changes; they are NOT in this workspace (it starts from the last commit)."
	}
	return &w, note, err
}

// WorkspaceDiff is what the workspace changed relative to where it started (new files included).
func WorkspaceDiff(ctx context.Context, db *pgxpool.Pool, id int64, statOnly bool) (string, error) {
	w, err := getWorkspace(ctx, db, id)
	if err != nil {
		return "", err
	}
	dir, ref := w.Path, w.Base
	if w.Status == "open" {
		if _, err := runGit(ctx, dir, time.Minute, "", "add", "-A"); err != nil {
			return "", err
		}
		args := []string{"diff", "--cached", "--no-color"}
		if statOnly {
			args = append(args, "--stat")
		}
		out, err := runGit(ctx, dir, time.Minute, "", append(args, ref)...)
		if strings.TrimSpace(out) == "" && err == nil {
			return "(no changes yet)", nil
		}
		return out, err
	}
	if w.Status == "kept" {
		args := []string{"diff", "--no-color"}
		if statOnly {
			args = append(args, "--stat")
		}
		return runGit(ctx, w.Repo, time.Minute, "", append(args, w.Base+".."+w.Branch)...)
	}
	return "", fmt.Errorf("workspace #%d is %s", id, w.Status)
}

func commitPending(ctx context.Context, w Workspace) error {
	if _, err := os.Stat(w.Path); err != nil {
		return nil
	}
	if out, err := runGit(ctx, w.Path, time.Minute, "", "add", "-A"); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	if out, _ := runGit(ctx, w.Path, 30*time.Second, "", "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil
	}
	msg := fmt.Sprintf("PRISM workspace #%d (%s)", w.ID, w.Agent)
	if out, err := runGit(ctx, w.Path, time.Minute, "", "-c", "user.name=PRISM", "-c", "user.email=prism@localhost", "commit", "-m", msg); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func removeWorktree(ctx context.Context, w Workspace) {
	_, _ = runGit(ctx, w.Repo, time.Minute, "", "worktree", "remove", "--force", w.Path)
	_ = os.RemoveAll(w.Path)
	_, _ = runGit(ctx, w.Repo, 30*time.Second, "", "worktree", "prune")
}

// WorkspaceKeep commits what is pending and keeps the branch in the repository, removing the working copy.
func WorkspaceKeep(ctx context.Context, db *pgxpool.Pool, id int64) (string, error) {
	w, err := getWorkspace(ctx, db, id)
	if err != nil || w.Status != "open" {
		return "", fmt.Errorf("workspace #%d is not open", id)
	}
	if err := commitPending(ctx, w); err != nil {
		return "", err
	}
	removeWorktree(ctx, w)
	_, err = db.Exec(ctx, `UPDATE code_workspaces SET status='kept' WHERE id=$1`, id)
	return fmt.Sprintf("Branch %s is kept in %s. Merge it when you are ready: git merge %s", w.Branch, w.Repo, w.Branch), err
}

// WorkspaceApply brings the workspace's changes into the user's checkout as staged (uncommitted) changes; it
// refuses when the checkout has uncommitted work of its own, and leaves it exactly as it was on a conflict.
func WorkspaceApply(ctx context.Context, db *pgxpool.Pool, id int64) (string, error) {
	w, err := getWorkspace(ctx, db, id)
	if err != nil || (w.Status != "open" && w.Status != "kept") {
		return "", fmt.Errorf("workspace #%d cannot be applied", id)
	}
	if out, _ := runGit(ctx, w.Repo, 30*time.Second, "", "status", "--porcelain"); strings.TrimSpace(out) != "" {
		return "", errors.New("your checkout has uncommitted changes: commit or stash them first, or choose \"keep branch\" and merge later")
	}
	if w.Status == "open" {
		if err := commitPending(ctx, w); err != nil {
			return "", err
		}
	}
	if out, err := runGit(ctx, w.Repo, time.Minute, "", "merge", "--squash", w.Branch); err != nil {
		_, _ = runGit(ctx, w.Repo, time.Minute, "", "reset", "--merge")
		return "", fmt.Errorf("the changes do not apply cleanly to your checkout (nothing was changed):\n%s", strings.TrimSpace(out))
	}
	if w.Status == "open" {
		removeWorktree(ctx, w)
	}
	_, err = db.Exec(ctx, `UPDATE code_workspaces SET status='applied' WHERE id=$1`, id)
	return "Applied as staged changes in " + w.Repo + " — review with git diff --cached and commit when happy.", err
}

// WorkspaceDiscard throws the workspace and its branch away.
func WorkspaceDiscard(ctx context.Context, db *pgxpool.Pool, id int64) error {
	w, err := getWorkspace(ctx, db, id)
	if err != nil || (w.Status != "open" && w.Status != "kept") {
		return fmt.Errorf("workspace #%d cannot be discarded", id)
	}
	removeWorktree(ctx, w)
	_, _ = runGit(ctx, w.Repo, time.Minute, "", "branch", "-D", w.Branch)
	_, err = db.Exec(ctx, `UPDATE code_workspaces SET status='discarded' WHERE id=$1`, id)
	return err
}

func registerWorkspaces(reg *tools.Registry, d Deps) {
	reg.Register(
		&tools.Tool{
			Name: "workspace_open", Category: "git", Risk: tools.RiskWrite, Auto: true,
			Description: "Start coding work on a repository safely: creates an isolated git worktree on its own branch (from the last commit) and returns its path. Do ALL edits, tests and commits inside that path; the user's own checkout stays untouched, and they review your diff (workspace_diff) and decide whether to keep or apply it. Open one workspace per coding task.",
			Params:      tools.Obj("repo", tools.Str("repo", "path of the git repository"), tools.Str("name", "short name for the work (default: the repo name)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Repo, Name string }](raw)
				if err != nil {
					return "", err
				}
				if err := tools.ConfirmIfTainted(ctx, env, "workspace_open", a.Repo); err != nil {
					return "", err
				}
				w, note, err := d.OpenWorkspace(ctx, a.Repo, a.Name, env.Agent, env.TaskID)
				if err != nil {
					return "", err
				}
				info := scanRepo(ctx, w.Path).String()
				return fmt.Sprintf("Workspace #%d ready at %s on branch %s (from %s).%s\nWork inside that directory: file_edit / apply_patch / shell (cwd) / git_commit. Show the result with workspace_diff.\n\nWhat the project looks like (store new facts with memory_store in project:%s if memory does not know them yet):\n%s", w.ID, w.Path, w.Branch, w.Base[:min(8, len(w.Base))], note, filepath.Base(w.Repo), info), nil
			},
		},
		&tools.Tool{
			Name: "workspace_diff", Category: "git", Risk: tools.RiskRead,
			Description: "Everything a workspace changed relative to where it started (including new files) — review it before reporting done. stat_only=true for the file summary.",
			Params:      tools.Obj("id", tools.Int("id", "workspace id"), tools.Bool("stat_only", "summary only")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID       int64 `json:"id"`
					StatOnly bool  `json:"stat_only"`
				}](raw)
				if err != nil {
					return "", err
				}
				return WorkspaceDiff(ctx, d.DB, a.ID, a.StatOnly)
			},
		},
	)
}
