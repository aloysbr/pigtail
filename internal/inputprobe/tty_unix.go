//go:build unix

package inputprobe

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrNotOwnTTY: the path is not a character device owned by this user.
// ErrOutputPending: bytes of claude's own output sit in the tty's queue; the
// 4 bytes of the query could land in the middle of its escape sequence.
var (
	ErrNotOwnTTY     = errors.New("inputprobe: tty is not a character device owned by this user")
	ErrOutputPending = errors.New("inputprobe: output pending on the tty")
)

// Seams for tests: fd 0 of a pid (rdev of the vnode, onVnode false for a
// pipe, a socket or no fd 0) and the device number of a tty path.
var (
	stdinTTY   = stdinTTYImpl
	vnodeRdevs = vnodeRdevsImpl // device numbers of every vnode fd opened for reading
	ttyRdev    = func(path string) (uint64, error) {
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			return 0, err
		}
		return uint64(st.Rdev), nil //nolint:unconvert // int32 on darwin
	}
)

// TTYOf reads the terminal of pid and whether pid alone reads it:
//  1. its group holds the terminal (pgid == tpgid > 0);
//  2. it is not stopped or traced (stat without T, X or t);
//  3. no other member of that foreground group on this tty has any fd on
//     it (/dev/tty included), and pid's own fd 0 is it (else the answer would not reach pid).
//
// A failed read is an error, never a guess.
func TTYOf(pid int) (TTYInfo, error) {
	out, err := psTTY(pid)
	if err != nil {
		return TTYInfo{}, err
	}
	f := strings.Fields(out)
	if len(f) != 4 {
		return TTYInfo{}, fmt.Errorf("inputprobe: ps row of pid %d: %q", pid, strings.TrimSpace(out))
	}
	stat := f[0]
	pgid, e1 := strconv.Atoi(f[1])
	tpgid, e2 := strconv.Atoi(f[2])
	path := TTYPath(f[3])
	if e1 != nil || e2 != nil {
		return TTYInfo{}, fmt.Errorf("inputprobe: ps row of pid %d: %q", pid, strings.TrimSpace(out))
	}
	if path == "" {
		return TTYInfo{}, fmt.Errorf("inputprobe: pid %d has no tty", pid)
	}
	info := TTYInfo{TTY: path}
	if pgid != tpgid || tpgid <= 0 {
		info.Why = "its process group does not hold the terminal"
		return info, nil
	}
	if strings.ContainsAny(stat, "TXt") {
		info.Why = "stopped, traced or debugged"
		return info, nil
	}
	rdev, err := ttyRdev(path)
	if err != nil {
		return TTYInfo{}, err
	}
	rd, onVnode, err := stdinTTY(pid)
	if err != nil {
		return TTYInfo{}, err
	}
	if !onVnode || rd != rdev {
		info.Why = "claude's stdin is not its tty"
		return info, nil
	}
	list, err := listProcs()
	if err != nil {
		return TTYInfo{}, fmt.Errorf("inputprobe: process listing: %w", err)
	}
	procs, err := ParseProcsStrict(list)
	if err != nil {
		return TTYInfo{}, err
	}
	col := strings.TrimPrefix(path, "/dev/")
	for _, q := range procs {
		if q.PID == pid || q.TTY != col || q.PGID != tpgid {
			continue
		}
		rds, err := vnodeRdevs(q.PID)
		if errors.Is(err, syscall.ESRCH) {
			continue // gone between the listing and the read: it holds nothing
		}
		if err != nil {
			return TTYInfo{}, err
		}
		if slices.Contains(rds, rdev) {
			info.Why = fmt.Sprintf("another process (pid %d) of the foreground group reads the tty", q.PID)
			return info, nil
		}
	}
	info.Foreground = true
	return info, nil
}

var ttyPathRe = regexp.MustCompile(`^/dev/(ttys[0-9]+|pts/[0-9]+)$`)

// WriteTTY writes Query, and nothing else, to a tty of this user: the path
// must be /dev/ttysN or /dev/pts/N and a character device owned by the
// caller, and nothing may wait in its output queue. One write(2); a short
// write or EAGAIN is an error, never retried.
func WriteTTY(path string, b []byte) error {
	return writeTTY(path, b, ttyPathRe.MatchString)
}

// writeTTY is WriteTTY with the path check injected, so the device-type
// refusal can be tested on a regular file.
func writeTTY(path string, b []byte, pathOK func(string) bool) error {
	if !pathOK(path) {
		return fmt.Errorf("inputprobe: refusing to write to %q", path)
	}
	if string(b) != Query {
		return errors.New("inputprobe: refusing to write anything but Query")
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("inputprobe: open %s: %w", path, err)
	}
	defer unix.Close(fd) //nolint:errcheck // nothing to do on a failed close
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("inputprobe: fstat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || int(st.Uid) != os.Getuid() {
		return ErrNotOwnTTY
	}
	queued, err := unix.IoctlGetInt(fd, unix.TIOCOUTQ)
	if err != nil {
		return fmt.Errorf("inputprobe: TIOCOUTQ %s: %w", path, err)
	}
	if queued > 0 {
		return ErrOutputPending
	}
	n, err := unix.Write(fd, b)
	if err != nil {
		return fmt.Errorf("inputprobe: write %s: %w", path, err)
	}
	if n != len(b) {
		return fmt.Errorf("inputprobe: short write to %s: %d of %d", path, n, len(b))
	}
	return nil
}
