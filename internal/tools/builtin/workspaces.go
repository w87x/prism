package builtin

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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

	VerifyStatus string     `json:"verify_status"` // "" (never) | pass | fail | none (no commands known)
	VerifyOutput string     `json:"verify_output"`
	VerifiedAt   *time.Time `json:"verified_at"`
	Stale        bool       `json:"stale"` // changed since it was verified
	ReviewTaskID *int64     `json:"review_task_id"`
	Review       string     `json:"review"`  // the reviewer's report, once it has finished
	Verdict      string     `json:"verdict"` // approve | approve with fixes | reject | ""
	verifyHash   string
}

const wsCols = `id,repo,path,branch,base,agent,task_id,status,created_at,verify_status,verify_output,verify_hash,verified_at,review_task_id`

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
		Scan(&w.ID, &w.Repo, &w.Path, &w.Branch, &w.Base, &w.Agent, &w.TaskID, &w.Status, &w.CreatedAt, &w.VerifyStatus, &w.VerifyOutput, &w.verifyHash, &w.VerifiedAt, &w.ReviewTaskID)
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
		if err := rows.Scan(&w.ID, &w.Repo, &w.Path, &w.Branch, &w.Base, &w.Agent, &w.TaskID, &w.Status, &w.CreatedAt, &w.VerifyStatus, &w.VerifyOutput, &w.verifyHash, &w.VerifiedAt, &w.ReviewTaskID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	rows.Close()
	for i := range out {
		if w := &out[i]; w.ReviewTaskID != nil {
			var status, result string
			if db.QueryRow(ctx, `SELECT status, COALESCE(result,'') FROM tasks WHERE id=$1`, *w.ReviewTaskID).Scan(&status, &result) == nil {
				w.Review, w.Verdict = result, verdictOf(result)
				if status != "done" && status != "partial" {
					w.Review = "(review " + status + ")"
				}
			}
		}
		if out[i].VerifyStatus != "" && out[i].Status == "open" {
			out[i].Stale = out[i].verifyHash != workspaceHash(ctx, out[i])
		}
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
				diff, err := WorkspaceDiff(ctx, d.DB, a.ID, a.StatOnly)
				if err != nil {
					return diff, err
				}
				w, _ := getWorkspace(ctx, d.DB, a.ID)
				line := "Checks: not run yet — run workspace_verify before you report done.\n\n"
				if w.VerifyStatus != "" {
					line = fmt.Sprintf("Checks: %s (%s)", w.VerifyStatus, w.VerifiedAt.Format("15:04"))
					if w.verifyHash != workspaceHash(ctx, w) {
						line += " — but the code changed since; run workspace_verify again"
					}
					line += "\n\n"
				}
				return line + diff, nil
			},
		},
		&tools.Tool{
			Name: "workspace_verify", Category: "git", Risk: tools.RiskExec,
			Description: "Run the repository's build, lint and test commands inside a workspace and get pass/fail with the failing output. The commands come from the project's saved settings or from scanning it. Call it after your changes and before reporting done; if it fails, fix the cause and run it again. The result is shown to the user next to the diff.",
			Params:      tools.Obj("id", tools.Int("id", "workspace id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID int64 `json:"id"`
				}](raw)
				if err != nil {
					return "", err
				}
				status, report, err := VerifyWorkspace(ctx, d.DB, a.ID)
				if err != nil {
					return "", err
				}
				return strings.ToUpper(status) + "\n" + report, nil
			},
		},
	)
}

var verdictRe = regexp.MustCompile(`(?i)verdict:?\s*\**\s*(approve with fixes|approve|reject)`)

// verdictOf reads the reviewer's one-line verdict from its report.
func verdictOf(report string) string {
	m := verdictRe.FindAllStringSubmatch(report, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.ToLower(m[len(m)-1][1])
}

// workspaceHash identifies the current state of a workspace's content (HEAD plus uncommitted changes), so a
// verification can be recognised as out of date once the code changes again.
func workspaceHash(ctx context.Context, w Workspace) string {
	head, _ := runGit(ctx, w.Path, 20*time.Second, "", "rev-parse", "HEAD")
	_, _ = runGit(ctx, w.Path, 20*time.Second, "", "add", "-A")
	diff, _ := runGit(ctx, w.Path, 30*time.Second, "", "diff", "--cached", "--no-color", "HEAD")
	sum := sha1.Sum([]byte(strings.TrimSpace(head) + "\n" + diff))
	return hex.EncodeToString(sum[:])
}

// CodeCommands are the build, test and lint commands checks run for a repository.
type CodeCommands struct {
	Repo  string `json:"repo"`
	Build string `json:"build"`
	Test  string `json:"test"`
	Lint  string `json:"lint"`
	// Detected are what scanning the repository suggests, shown as placeholders.
	DetectedBuild string `json:"detected_build"`
	DetectedTest  string `json:"detected_test"`
	DetectedLint  string `json:"detected_lint"`
}

func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

// Commands returns the saved commands of a repository together with what scanning it suggests.
func Commands(ctx context.Context, db *pgxpool.Pool, repo string) (CodeCommands, error) {
	c := CodeCommands{Repo: repo}
	_ = db.QueryRow(ctx, `SELECT build_cmd,test_cmd,lint_cmd FROM code_projects WHERE repo=$1`, repo).Scan(&c.Build, &c.Test, &c.Lint)
	if st, err := os.Stat(repo); err == nil && st.IsDir() {
		rs := scanRepo(ctx, repo)
		c.DetectedBuild, c.DetectedTest, c.DetectedLint = first(rs.Build), first(rs.Test), first(rs.Lint)
	}
	return c, nil
}

func SetCommands(ctx context.Context, db *pgxpool.Pool, c CodeCommands) error {
	_, err := db.Exec(ctx, `INSERT INTO code_projects(repo,build_cmd,test_cmd,lint_cmd) VALUES($1,$2,$3,$4)
		ON CONFLICT (repo) DO UPDATE SET build_cmd=$2, test_cmd=$3, lint_cmd=$4, updated_at=now()`,
		c.Repo, strings.TrimSpace(c.Build), strings.TrimSpace(c.Test), strings.TrimSpace(c.Lint))
	return err
}

func tailLines(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) > n {
		ls = append([]string{fmt.Sprintf("…(%d earlier lines)", len(ls)-n)}, ls[len(ls)-n:]...)
	}
	return strings.Join(ls, "\n")
}

// VerifyWorkspace runs the repository's build, lint and test commands inside the workspace and records the
// outcome. The commands are the ones saved for the repository, or what scanning it suggests — never something an
// agent makes up on the spot.
func VerifyWorkspace(ctx context.Context, db *pgxpool.Pool, id int64) (status, report string, err error) {
	w, err := getWorkspace(ctx, db, id)
	if err != nil || w.Status != "open" {
		return "", "", fmt.Errorf("workspace #%d is not open", id)
	}
	c, _ := Commands(ctx, db, w.Repo)
	steps := []struct{ name, cmd string }{{"build", firstNonEmpty2(c.Build, c.DetectedBuild)}, {"lint", firstNonEmpty2(c.Lint, c.DetectedLint)}, {"test", firstNonEmpty2(c.Test, c.DetectedTest)}}
	var sb strings.Builder
	status = "pass"
	ran := 0
	for _, st := range steps {
		if st.cmd == "" {
			continue
		}
		ran++
		start := time.Now()
		out, rerr := runCmd(ctx, 10*time.Minute, w.Path, "sh", "-c", st.cmd)
		failed := rerr != nil || strings.Contains(out, "\n[exit status ") || strings.HasPrefix(out, "[exit status ") || strings.Contains(out, "[timed out after")
		mark := "✓"
		if failed {
			mark, status = "✗", "fail"
		}
		fmt.Fprintf(&sb, "%s %s: %s (%s)\n", mark, st.name, st.cmd, time.Since(start).Round(100*time.Millisecond))
		if failed {
			sb.WriteString(tailLines(out, 40) + "\n")
			break // the later steps would only bury the first problem
		}
	}
	if ran == 0 {
		status = "none"
		sb.WriteString("No build, lint or test command is known for this repository. Set them in Library → Code → Checks (or add a Makefile / go.mod / package.json the scan can read).\n")
	}
	report = strings.TrimSpace(sb.String())
	_, err = db.Exec(ctx, `UPDATE code_workspaces SET verify_status=$2, verify_output=$3, verify_hash=$4, verified_at=now() WHERE id=$1`, id, status, report, workspaceHash(ctx, w))
	return status, report, err
}

func firstNonEmpty2(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}

// OpenWorkspacesOfTask lists the still-open workspaces a task created.
func OpenWorkspacesOfTask(ctx context.Context, db *pgxpool.Pool, taskID int64) ([]Workspace, error) {
	rows, err := db.Query(ctx, `SELECT id FROM code_workspaces WHERE task_id=$1 AND status='open' AND review_task_id IS NULL ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []Workspace
	for _, id := range ids {
		if w, err := getWorkspace(ctx, db, id); err == nil {
			out = append(out, w)
		}
	}
	return out, nil
}

// SetWorkspaceReview links a workspace to the review task that judges it.
func SetWorkspaceReview(ctx context.Context, db *pgxpool.Pool, id, taskID int64) error {
	_, err := db.Exec(ctx, `UPDATE code_workspaces SET review_task_id=$2 WHERE id=$1`, id, taskID)
	return err
}
