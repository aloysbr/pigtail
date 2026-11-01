//go:build darwin && cgo

package inputprobe

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func rdevOf(t *testing.T, path string) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Rdev)
}

func TestStdinTTYReadsChildPty(t *testing.T) {
	_, slave := openPty(t)
	s, err := os.OpenFile(slave, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cmd := exec.Command("sleep", "5")
	cmd.Stdin = s
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rdev, onVnode, err := stdinTTY(cmd.Process.Pid)
	if err != nil || !onVnode || rdev != rdevOf(t, slave) {
		t.Fatalf("stdinTTY = %d, %v, %v; want the slave's rdev %d", rdev, onVnode, err, rdevOf(t, slave))
	}
}

// A pipe on fd 0 (an MCP server, a cmux hook) is "not on the tty", never an
// error: proc_pidfdinfo(pid, 0, ...) alone would fail with EBADF on it.
func TestStdinTTYPipeIsNotTTY(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	w, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	_, onVnode, err := stdinTTY(cmd.Process.Pid)
	if err != nil || onVnode {
		t.Fatalf("stdinTTY on a pipe = onVnode %v, err %v; want false, nil", onVnode, err)
	}
}

func TestStdinTTYGoneProcessIsESRCH(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_, _, err := stdinTTY(cmd.Process.Pid)
	if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("err %v, want ESRCH", err)
	}
}

// A pty held on fd 3 (what /dev/tty gives sudo, ssh, git) is listed.
func TestVnodeRdevsSeesPtyOnFd3(t *testing.T) {
	_, slave := openPty(t)
	s, err := os.OpenFile(slave, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cmd := exec.Command("sleep", "5")
	cmd.ExtraFiles = []*os.File{s}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rds, err := vnodeRdevs(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rds {
		found = found || r == rdevOf(t, slave)
	}
	if !found {
		t.Fatalf("vnodeRdevs = %v, want the slave's rdev %d", rds, rdevOf(t, slave))
	}
}
