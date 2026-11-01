package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The helper child holds the lock until its stdin closes or it is killed.
func TestInstanceLockHelper(t *testing.T) {
	db := os.Getenv("PIGTAIL_TEST_LOCK_DB")
	if db == "" {
		t.Skip("child helper only")
	}
	release, err := AcquireInstanceLock(db)
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(3)
	}
	defer release()
	fmt.Println("LOCKED", os.Getpid())
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func startLockChild(t *testing.T, db string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInstanceLockHelper$")
	cmd.Env = append(os.Environ(), "PIGTAIL_TEST_LOCK_DB="+db)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		var pid int
		if _, err := fmt.Sscanf(sc.Text(), "LOCKED %d", &pid); err == nil {
			return cmd, pid
		}
	}
	t.Fatal("child never reported LOCKED")
	return nil, 0
}

func TestInstanceLockRefusesSecond(t *testing.T) {
	db := filepath.Join(t.TempDir(), "pigtail.db")
	_, pid := startLockChild(t, db)
	release, err := AcquireInstanceLock(db)
	if err == nil {
		release()
		t.Fatal("second acquisition succeeded")
	}
	if !errors.Is(err, ErrAnotherInstance) {
		t.Fatalf("want ErrAnotherInstance, got %v", err)
	}
	want := fmt.Sprintf("another pigtaild (pid %d) already serves %s", pid, db)
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "-db <other path>") {
		t.Fatalf("message %q lacks %q", err, want)
	}
}

func TestInstanceLockReleasedOnExit(t *testing.T) {
	db := filepath.Join(t.TempDir(), "pigtail.db")
	cmd, _ := startLockChild(t, db)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, err := AcquireInstanceLock(db)
		if err == nil {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not released after kill: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestInstanceLockReacquireAfterRelease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "pigtail.db")
	r1, err := AcquireInstanceLock(db)
	if err != nil {
		t.Fatal(err)
	}
	r1()
	r2, err := AcquireInstanceLock(db)
	if err != nil {
		t.Fatal(err)
	}
	r2()
}

func TestBindErrorHintsPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = listenTCP(ln.Addr().String())
	if err == nil {
		t.Fatal("bind on a busy port succeeded")
	}
	for _, w := range []string{"port in use: is another pigtaild running?", "--http 127.0.0.1:<port>"} {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("%q lacks %q", err, w)
		}
	}
}
