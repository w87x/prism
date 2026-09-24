package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestOrphanRootsPicksOnlyOurUnownedChromes(t *testing.T) {
	prof := "/home/u/.prism/data/browser-profile"
	self := os.Getpid()
	all := []proc{
		{100, 1, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --user-data-dir=" + prof + " --headless"}, // legacy orphan (no tag)
		{101, 100, "Chrome Helper --user-data-dir=" + prof + " --type=renderer"},                                         // its helper: not a root
		{200, 1, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},                                         // the user's own Chrome
		{201, 1, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --user-data-dir=" + prof + "-other"},      // similar-looking other profile
		{300, 1, "Google Chrome --user-data-dir=" + prof + " --prism-owner=" + strconv.Itoa(self+100000)},                // tagged, owner long gone (pid not alive)
		{400, 1, "Google Chrome --user-data-dir=" + prof + " --prism-owner=1"},                                           // tagged with a live pid (launchd) → someone's running PRISM
	}
	roots, mine := orphanRoots(all, prof, self)
	got := map[int]bool{}
	for _, r := range roots {
		got[r.pid] = true
	}
	if !got[100] || !got[300] || got[101] || got[200] || got[201] || got[400] {
		t.Fatalf("roots=%v", got)
	}
	if len(mine) != 4 {
		t.Fatalf("mine=%d", len(mine))
	}
}

func TestReapStopsOrphanAndClearsLock(t *testing.T) {
	prof := filepath.Join(t.TempDir(), "browser-profile")
	_ = os.MkdirAll(prof, 0o755)
	// stand-in for an orphaned Chrome: any process whose command line carries our profile flag
	cmd := exec.Command("sh", "-c", "sleep 60; true", "prism-fake-chrome", "--user-data-dir="+prof)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	defer cmd.Process.Kill()
	time.Sleep(300 * time.Millisecond)
	// a Chrome-style stale lock pointing at a process that no longer exists
	_ = os.Symlink("host-999999", filepath.Join(prof, "SingletonLock"))

	n, err := Reap(prof)
	if err != nil || n != 1 {
		t.Fatalf("reaped %d, %v", n, err)
	}
	time.Sleep(200 * time.Millisecond)
	if alive(cmd.Process.Pid) {
		t.Fatal("the orphan must be gone")
	}
	if _, err := os.Lstat(filepath.Join(prof, "SingletonLock")); err == nil {
		t.Fatal("the stale lock must be removed")
	}
}
