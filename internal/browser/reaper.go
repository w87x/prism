package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ownerFlag tags every Chrome PRISM launches (--prism-owner=<pid>); Chrome ignores switches it does not
// know, but they show up in the process list, so a later PRISM can tell whose browser it is looking at.
const ownerFlag = "prism-owner"

var ownerRe = regexp.MustCompile(`--` + ownerFlag + `=(\d+)`)

type proc struct {
	pid, ppid int
	cmd       string
}

func processes() ([]proc, error) {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var ps []proc
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		ps = append(ps, proc{pid, ppid, strings.Join(f[2:], " ")})
	}
	return ps, nil
}

// alive reports whether a process exists. EPERM means it exists but belongs to someone else (launchd, another user).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// usesProfile is true for a Chrome process started on exactly this profile directory.
func usesProfile(cmd, profile string) bool {
	flag := "--user-data-dir=" + profile
	i := strings.Index(cmd, flag)
	return i >= 0 && (i+len(flag) == len(cmd) || cmd[i+len(flag)] == ' ')
}

// orphanRoots picks the Chrome processes on this profile that nobody owns any more: the roots of the
// process tree (their parent is not itself a Chrome on the profile) whose launching PRISM is gone.
// A Chrome tagged with a still-running PRISM is left alone.
func orphanRoots(all []proc, profile string, self int) (roots, mine []proc) {
	on := map[int]bool{}
	for _, p := range all {
		if p.pid != self && usesProfile(p.cmd, profile) {
			on[p.pid] = true
			mine = append(mine, p)
		}
	}
	for _, p := range mine {
		if on[p.ppid] {
			continue // a helper of another matched process
		}
		if m := ownerRe.FindStringSubmatch(p.cmd); m != nil {
			if owner, _ := strconv.Atoi(m[1]); owner != self && alive(owner) {
				continue // launched by a PRISM that is still running
			}
		}
		roots = append(roots, p)
	}
	return roots, mine
}

// Reap stops Chrome processes left over on PRISM's own profile (a previous PRISM was killed without
// closing its browser) and removes a stale profile lock. It never touches a Chrome on another profile,
// so the user's normal browser is safe. It returns how many browsers it stopped.
func Reap(profile string) (int, error) {
	profile = filepath.Clean(profile)
	all, err := processes()
	if err != nil {
		return 0, err
	}
	roots, mine := orphanRoots(all, profile, os.Getpid())
	for _, r := range roots {
		_ = syscall.Kill(r.pid, syscall.SIGTERM) // a normal quit: Chrome saves cookies and session state
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		still := false
		for _, r := range roots {
			if alive(r.pid) {
				still = true
			}
		}
		if !still {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, p := range mine { // whatever ignored the polite request
		for _, r := range roots {
			if p.pid == r.pid || p.ppid == r.pid {
				if alive(p.pid) {
					_ = syscall.Kill(p.pid, syscall.SIGKILL)
				}
			}
		}
	}
	clearStaleLock(profile)
	return len(roots), nil
}

// clearStaleLock removes Chrome's Singleton* files when the process they name is gone.
func clearStaleLock(profile string) {
	target, err := os.Readlink(filepath.Join(profile, "SingletonLock")) // "<host>-<pid>"
	if err != nil {
		return
	}
	i := strings.LastIndex(target, "-")
	if pid, err := strconv.Atoi(target[i+1:]); i >= 0 && err == nil && alive(pid) {
		return
	}
	for _, n := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		_ = os.Remove(filepath.Join(profile, n))
	}
}

// oneLine reduces Chrome's multi-line startup log to something a status-bar tooltip can show.
func oneLine(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "SingletonLock") || strings.Contains(s, "ProcessSingleton"):
		return "the browser profile is locked by another Chrome"
	}
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}
