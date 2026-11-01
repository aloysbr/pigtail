package claudeusage

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// TestRefreshLockHelper is not a test: TestRefreshLockExclusive re-runs the
// test binary on it to hold the lock from another process. It takes the lock
// in REFRESH_LOCK_DIR, prints "held", and keeps it until stdin closes.
func TestRefreshLockHelper(t *testing.T) {
	dir := os.Getenv("REFRESH_LOCK_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	unlock, held, err := TryRefreshLock(dir)
	if err != nil || held {
		os.Stdout.WriteString("failed\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("held\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // until the parent closes stdin
	unlock()
	os.Exit(0)
}

// The lock is between processes (pigtail usage --refresh vs the daemon's
// sweep): a child process holds it, the parent is told held; once the child
// exits, the parent gets it.
func TestRefreshLockExclusive(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRefreshLockHelper$")
	cmd.Env = append(os.Environ(), "REFRESH_LOCK_DIR="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "held\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("child: %q, %v", line, err)
	}
	unlock, held, err := TryRefreshLock(dir)
	if err != nil || !held || unlock != nil {
		t.Fatalf("while the child holds it: held=%v err=%v unlock=%v, want held, no error", held, err, unlock != nil)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	unlock, held, err = TryRefreshLock(dir)
	if err != nil || held || unlock == nil {
		t.Fatalf("after the child exited: held=%v err=%v, want the lock", held, err)
	}
	unlock()
	if _, err := os.Stat(RefreshLockPath(dir)); err != nil {
		t.Fatalf("lock file: %v", err)
	}
}

// Each taker opens its own descriptor: of N concurrent takers exactly one
// gets the lock, the others are told held.
func TestRefreshLockConcurrentTakers(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	got, heldN := 0, 0
	var unlocks []func()
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			unlock, held, err := TryRefreshLock(dir)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				t.Errorf("taker: %v", err)
			case held:
				heldN++
			default:
				got++
				unlocks = append(unlocks, unlock)
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, u := range unlocks {
		u()
	}
	if got != 1 || heldN != n-1 {
		t.Fatalf("got=%d held=%d, want 1 and %d", got, heldN, n-1)
	}
}

// A lock that cannot be opened is an error, never "held" and never a lock.
func TestRefreshLockError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	ro := filepath.Join(parent, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	unlock, held, err := TryRefreshLock(ro)
	if err == nil || held || unlock != nil {
		t.Fatalf("held=%v err=%v unlock=%v, want an error", held, err, unlock != nil)
	}
}
