//go:build darwin

package inputprobe

import (
	"errors"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPty opens a pty pair owned by the test; the test never touches any
// other tty.
func openPty(t *testing.T) (master *os.File, slavePath string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		unix.Close(fd)
		t.Skipf("grant: %v", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		unix.Close(fd)
		t.Skipf("unlock: %v", err)
	}
	var name [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); e != 0 { //nolint:staticcheck // x/sys has no wrapper for TIOCPTYGNAME
		unix.Close(fd)
		t.Skipf("gname: %v", e)
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	_ = unix.SetNonblock(fd, true) // pollable, so reads take a deadline
	m := os.NewFile(uintptr(fd), "ptmx")
	t.Cleanup(func() { m.Close() })
	return m, string(name[:n])
}

func readWithin(t *testing.T, f *os.File, max int) []byte {
	t.Helper()
	if err := f.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Skipf("no read deadline on the master: %v", err)
	}
	buf := make([]byte, max)
	n, _ := f.Read(buf)
	return buf[:n]
}

func TestWriteTTYWritesQueryToOwnPty(t *testing.T) {
	m, slave := openPty(t)
	// claude holds the slave open for its whole life; so does the test.
	held, err := os.OpenFile(slave, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := WriteTTY(slave, []byte(Query)); err != nil {
		t.Fatal(err)
	}
	if got := readWithin(t, m, 16); string(got) != Query {
		t.Fatalf("master read %q, want %q", got, Query)
	}
}

func TestWriteTTYRefusesWhenOutputPending(t *testing.T) {
	m, slave := openPty(t)
	s, err := os.OpenFile(slave, os.O_WRONLY|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	if err := WriteTTY(slave, []byte(Query)); !errors.Is(err, ErrOutputPending) {
		t.Fatalf("err %v, want ErrOutputPending", err)
	}
	if got := string(readWithin(t, m, 16)); got != "pending" {
		t.Fatalf("master read %q: only the pending bytes, never the query", got)
	}
}
