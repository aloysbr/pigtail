//go:build unix

package inputprobe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteTTYRefusesPathOutsideDev(t *testing.T) {
	for _, p := range []string{"/tmp/x", "/dev/null", "/dev/ttys1/../disk0", "/dev/tty", "", "/dev/ttys", "/dev/ttys1x"} {
		if err := WriteTTY(p, []byte(Query)); err == nil {
			t.Errorf("WriteTTY(%q) = nil, want a refusal", p)
		}
	}
}

func TestWriteTTYRefusesOtherBytes(t *testing.T) {
	for _, b := range []string{"a", "\x1b[I", Query + "\n", ""} {
		// pathOK true and a regular file: only the bytes can stop it, and
		// they must stop it before anything is opened.
		f := filepath.Join(t.TempDir(), "tty")
		if err := writeTTY(f, []byte(b), func(string) bool { return true }); err == nil {
			t.Errorf("writeTTY(%q) = nil, want a refusal", b)
		}
		if _, err := os.Stat(f); err == nil {
			t.Errorf("writeTTY(%q) touched the path", b)
		}
	}
}

func TestWriteTTYRefusesNonCharDevice(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeTTY(f, []byte(Query), func(string) bool { return true })
	if !errors.Is(err, ErrNotOwnTTY) {
		t.Fatalf("err %v, want ErrNotOwnTTY", err)
	}
	if b, _ := os.ReadFile(f); len(b) != 0 {
		t.Fatalf("wrote %q into a regular file", b)
	}
}

func TestTTYOfParsesPs(t *testing.T) {
	defer stubTTYOf(t, "S+ 812 812 ttys037", nil)()
	got, err := TTYOf(812)
	if err != nil || got.TTY != "/dev/ttys037" || !got.Foreground {
		t.Fatalf("TTYOf = %+v, %v", got, err)
	}
	for _, bad := range []string{"S 812 812 ??", "S 812 812 -", "", "S+ 812 812"} {
		restore := stubTTYOf(t, bad, nil)
		if _, err := TTYOf(812); err == nil {
			t.Errorf("ps %q: want an error", bad)
		}
		restore()
	}
}

// stubTTYOf replaces the seams of TTYOf: own row, group listing, stdin of
// each pid (nil = the claude's stdin is the tty, the others' a pipe).
func stubTTYOf(t *testing.T, own string, listing []string) func() {
	t.Helper()
	oldPS, oldList, oldStdin, oldRdev := psTTY, listProcs, stdinTTY, ttyRdev
	psTTY = func(int) (string, error) { return own, nil }
	listProcs = func() ([]byte, error) { return []byte(strings.Join(listing, "\n")), nil }
	stdinTTY = func(pid int) (uint64, bool, error) {
		if pid == 812 {
			return 7, true, nil
		}
		return 0, false, nil
	}
	ttyRdev = func(string) (uint64, error) { return 7, nil }
	oldFDs := vnodeRdevs
	// default: a member's only reading fd is its fd 0, as the stdinTTY stub says
	vnodeRdevs = func(pid int) ([]uint64, error) {
		rd, on, err := stdinTTY(pid)
		if err != nil || !on {
			return nil, err
		}
		return []uint64{rd}, nil
	}
	return func() { psTTY, listProcs, stdinTTY, ttyRdev, vnodeRdevs = oldPS, oldList, oldStdin, oldRdev, oldFDs }
}

func TestTTYOfForegroundRule(t *testing.T) {
	type tc struct {
		name    string
		own     string
		listing []string
		stdin   func(pid int) (uint64, bool, error)
		want    bool
		wantErr bool
	}
	group := []string{
		"812 812 812 S+ ttys037",
		"900 812 812 S+ ttys037",
	}
	onTTY := func(pid int) (uint64, bool, error) { return 7, true, nil }
	cases := []tc{
		{name: "foreground alone", own: "S+ 812 812 ttys037", listing: group, want: true},
		{name: "pgid != tpgid", own: "S 812 500 ttys037", listing: group},
		{name: "tpgid 0", own: "S 812 0 ttys037", listing: group},
		{name: "stopped", own: "T+ 812 812 ttys037", listing: group},
		{name: "member reads the tty", own: "S+ 812 812 ttys037", listing: group, stdin: onTTY},
		{name: "member on a pipe", own: "S+ 812 812 ttys037", listing: group, want: true},
		{name: "claude stdin off tty", own: "S+ 812 812 ttys037", listing: group,
			stdin: func(pid int) (uint64, bool, error) { return 0, false, nil }},
		{name: "claude stdin other tty", own: "S+ 812 812 ttys037", listing: group,
			stdin: func(pid int) (uint64, bool, error) { return 9, true, nil }},
		{name: "fd list fails", own: "S+ 812 812 ttys037", listing: group, wantErr: true,
			stdin: func(pid int) (uint64, bool, error) { return 0, false, errors.New("proc_pidinfo failed") }},
		{name: "member gone is ignored", own: "S+ 812 812 ttys037", listing: group, want: true,
			stdin: func(pid int) (uint64, bool, error) {
				if pid == 812 {
					return 7, true, nil
				}
				return 0, false, syscall.ESRCH
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer stubTTYOf(t, c.own, c.listing)()
			if c.stdin != nil {
				stdinTTY = c.stdin
				if c.name == "member reads the tty" {
					stdinTTY = func(pid int) (uint64, bool, error) { return 7, true, nil }
				}
			}
			got, err := TTYOf(812)
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && got.Foreground != c.want {
				t.Fatalf("Foreground %v (%s), want %v", got.Foreground, got.Why, c.want)
			}
			if err == nil && !c.want && got.Why == "" {
				t.Fatal("a false Foreground needs its Why")
			}
		})
	}
}

func TestTTYOfErrorsOnMalformedListingLine(t *testing.T) {
	defer stubTTYOf(t, "S+ 812 812 ttys037", []string{"812 812 812 S+ ttys037", "900 812 garbage S+ ttys037"})()
	if got, err := TTYOf(812); err == nil {
		t.Fatalf("TTYOf = %+v, nil; a malformed listing line must be an error (fails open otherwise)", got)
	}
}

func TestTTYOfRefusesTracedStates(t *testing.T) {
	for _, stat := range []string{"T+", "X+", "S+t", "Ut"} {
		defer stubTTYOf(t, stat+" 812 812 ttys037", []string{"812 812 812 S+ ttys037"})()
		got, err := TTYOf(812)
		if err != nil || got.Foreground {
			t.Errorf("stat %q: %+v, %v; want not foreground", stat, got, err)
		}
	}
}

func TestTTYOfSeesMemberReadingTTYOnAnotherFd(t *testing.T) {
	defer stubTTYOf(t, "S+ 812 812 ttys037", []string{"812 812 812 S+ ttys037", "900 812 812 S+ ttys037"})()
	vnodeRdevs = func(pid int) ([]uint64, error) {
		if pid == 900 {
			return []uint64{3, 7}, nil // fd 0 on a pipe, /dev/tty on another fd
		}
		return nil, nil
	}
	got, err := TTYOf(812)
	if err != nil || got.Foreground {
		t.Fatalf("TTYOf = %+v, %v; a member holding the tty on fd 3 must refuse", got, err)
	}
}
