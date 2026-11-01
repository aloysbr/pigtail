package cmuxcheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readPS(t *testing.T, name string) []psProc {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	ps := parsePS(string(b))
	if len(ps) == 0 {
		t.Fatalf("%s: no process parsed", name)
	}
	return ps
}

func envOf(m map[int]string) func(int) (string, error) {
	return func(pid int) (string, error) {
		e, ok := m[pid]
		if !ok {
			return "", errors.New("gone")
		}
		return e, nil
	}
}

func noTree() (map[string]string, error) { return nil, errors.New("cmux tree must not be read") }

// Env path (step 1, unchanged): two panes on one tty (F9) are told apart by
// the CMUX_SURFACE_ID of their own shell when it is readable; cmux tree is
// not read then.
func TestShellIdleBySurfaceEnv(t *testing.T) {
	ps := readPS(t, "ps-shells.txt")
	environ := envOf(map[int]string{
		502: "-zsh CMUX_SURFACE_ID=S-B", 610: "gitstatusd CMUX_SURFACE_ID=S-B",
		802: "-zsh CMUX_SURFACE_ID=S-A", 900: "node CMUX_SURFACE_ID=S-A",
		950: "-zsh CMUX_SURFACE_ID=S-X", 960: "-zsh CMUX_SURFACE_ID=S-X",
	})
	if sh, err := shellOfSurface(ps, "S-B", 500, environ, noTree); err != nil || !sh.atPrompt() {
		t.Errorf("S-B: %+v err=%v, want at its prompt", sh, err)
	}
	if sh, err := shellOfSurface(ps, "S-A", 500, environ, noTree); err != nil || sh.atPrompt() {
		t.Errorf("S-A: %+v err=%v, want busy (node in the foreground)", sh, err)
	}
	if _, err := shellOfSurface(ps, "S-X", 500, environ, noTree); err == nil {
		t.Error("S-X: two shells claim it — want an error, never a guess")
	}
	if _, err := shellOfSurface(nil, "S-B", 500, environ, noTree); err == nil {
		t.Error("an empty listing is an error, not 'at the prompt'")
	}
}

// D22: real cmux 0.64.24 execs every pane shell through /usr/bin/login, whose
// environment macOS hides (ps -E shows argv only). The shell is then tied by
// the surface's tty from cmux tree — unique, under a login of the running
// cmux — and corroborated by any readable environment under that login.
func TestShellOfSurfaceByTTY(t *testing.T) {
	const real = "05153769-6402-4B24-A818-FB21C6EE07A6"
	ps := readPS(t, "ps-login-shells.txt")
	hidden := map[int]string{ // argv only, like ps -Eww on a login shell
		2609: "-/bin/zsh", 3101: "-/bin/zsh", 4101: "-/bin/zsh", 4201: "-/bin/zsh",
		5101: "-/bin/zsh", 5102: "-/bin/zsh", 6101: "-/bin/zsh", 7101: "-/bin/zsh", 9101: "-/bin/zsh",
		3882: "-/bin/zsh", 14223: "-/bin/zsh",
		2700:  "gitstatusd CMUX_SURFACE_ID=" + real,     // same surface: corroborates
		14226: "gitstatusd CMUX_SURFACE_ID=S-STALE",     // same tty, not under the login: ignored
		6150:  "gitstatusd CMUX_SURFACE_ID=S-ELSEWHERE", // under the login, another surface
		8101:  "-zsh CMUX_SURFACE_ID=S-ENV",
	}
	ttys := map[string]string{
		real: "ttys027", "S-BUSY": "ttys040", "S-OLD": "ttys041", "S-ORPHAN": "ttys042",
		"S-TWO": "ttys043", "S-OTHER": "ttys044", "S-NOTLOGIN": "ttys045", "S-ENV": "",
		"S-DUP1": "ttys034", "S-DUP2": "ttys034", "S-EMPTY": "",
	}
	tree := func() (map[string]string, error) { return ttys, nil }
	const cmuxPID = 98098

	sh, err := shellOfSurface(ps, real, cmuxPID, envOf(hidden), tree)
	if err != nil || sh.pid != 2609 || !sh.atPrompt() {
		t.Fatalf("real surface: %+v err=%v, want shell 2609 at its prompt", sh, err)
	}
	if sh, err := shellOfSurface(ps, "S-BUSY", cmuxPID, envOf(hidden), tree); err != nil || sh.pid != 3101 || sh.atPrompt() {
		t.Errorf("S-BUSY: %+v err=%v, want shell 3101, not at its prompt (sleep in the foreground)", sh, err)
	}
	if idle, err := shellIdle(Deps{PS: func() (string, error) { b, _ := os.ReadFile("testdata/ps-login-shells.txt"); return string(b), nil },
		Environ: envOf(hidden), SurfaceTTYs: tree}, real, cmuxPID); err != nil || !idle {
		t.Errorf("shellIdle(real): idle=%v err=%v, want at its prompt", idle, err)
	}
	refused := map[string]string{
		"S-DUP1":     "other surface", // tty claimed by two surfaces (F9)
		"S-EMPTY":    "no tty",
		"S-OLD":      "under a login of the running cmux", // login of an older cmux
		"S-ORPHAN":   "under a login of the running cmux", // stale shell, ppid 1
		"S-NOTLOGIN": "under a login of the running cmux", // parent is not login
		"S-TWO":      "2 shells",
		"S-OTHER":    "S-ELSEWHERE", // readable env under the login names another surface
		"S-NONE":     "no tty",
	}
	for s, want := range refused {
		if sh, err := shellOfSurface(ps, s, cmuxPID, envOf(hidden), tree); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %+v err=%v, want refused (%q)", s, sh, err, want)
		}
	}
	if _, err := shellOfSurface(ps, real, 0, envOf(hidden), tree); err == nil {
		t.Error("cmux pid unknown: want refused")
	}
	if _, err := shellOfSurface(ps, real, 12345, envOf(hidden), tree); err == nil {
		t.Error("another cmux pid: shell 2609's login is not its child — want refused")
	}
	if _, err := shellOfSurface(ps, real, cmuxPID, envOf(hidden), func() (map[string]string, error) { return nil, errors.New("cmux tree: timeout") }); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("cmux tree unreadable: err=%v, want refused with its reason", err)
	}
	// Step 1 still wins: S-ENV has no tty in cmux tree, its shell's own
	// environment names it.
	if sh, err := shellOfSurface(ps, "S-ENV", cmuxPID, envOf(hidden), noTree); err != nil || sh.pid != 8101 {
		t.Errorf("S-ENV: %+v err=%v, want shell 8101 by its environment, cmux tree unread", sh, err)
	}
}

// ShellAtPrompt (the folder arrangement's proof that an anchor is bare, spec 3
// of sub_x000000000000041) hands shellOfSurface's error back as is: never
// « at its prompt » on an error.
func TestShellAtPromptReturnsError(t *testing.T) {
	d := Deps{
		PS:          func() (string, error) { return "98098 1 98098 0 ?? /Applications/cmux.app/Contents/MacOS/cmux\n  10     1    10    10 ttys001 -zsh\n", nil },
		Environ:     func(int) (string, error) { return "", nil },
		SurfaceTTYs: func() (map[string]string, error) { return nil, errors.New("tree unreadable") },
	}
	sentinel := filepath.Join(t.TempDir(), "running.sentinel")
	if err := os.WriteFile(sentinel, []byte("pid=98098"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := shellAtPrompt(d, sentinel, "S-NONE")
	if err == nil || ok || !strings.Contains(err.Error(), "tree unreadable") {
		t.Fatalf("shellAtPrompt = %v, %v: shellOfSurface's error expected, never true", ok, err)
	}
	// No sentinel: the running cmux is unknown, the tty path refuses.
	ok, err = shellAtPrompt(d, filepath.Join(t.TempDir(), "none"), "S-NONE")
	if err == nil || ok {
		t.Fatalf("no sentinel: %v, %v: an error expected", ok, err)
	}
	d.PS = func() (string, error) { return "", errors.New("ps failed") }
	if ok, err := shellAtPrompt(d, sentinel, "S-NONE"); err == nil || ok {
		t.Fatalf("ps failing: %v, %v", ok, err)
	}
}

// Bug 2026-10-23: a short-lived second cmux overwrote the sentinel with its
// own (dead) pid; every respawn through a tab's shell then failed. The
// running cmux is taken from the process table when the sentinel's pid is not
// a live cmux; zero or several candidates stay « unknown ».
func TestShellAtPromptStaleSentinel(t *testing.T) {
	const real = "05153769-6402-4B24-A818-FB21C6EE07A6"
	b, err := os.ReadFile("testdata/ps-login-shells.txt") // cmux is pid 98098
	if err != nil {
		t.Fatal(err)
	}
	hidden := map[int]string{2609: "-/bin/zsh", 3101: "-/bin/zsh", 3882: "-/bin/zsh", 14223: "-/bin/zsh",
		2700: "gitstatusd CMUX_SURFACE_ID=" + real, 14226: "gitstatusd CMUX_SURFACE_ID=S-STALE"}
	mk := func(psText string) Deps {
		return Deps{
			PS:          func() (string, error) { return psText, nil },
			Environ:     envOf(hidden),
			SurfaceTTYs: func() (map[string]string, error) { return map[string]string{real: "ttys027"}, nil },
		}
	}
	sentinel := func(pid string) string {
		p := filepath.Join(t.TempDir(), "running.sentinel")
		if err := os.WriteFile(p, []byte("pid="+pid), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Dead pid in the sentinel, one real cmux in the table: found.
	if idle, err := shellAtPrompt(mk(string(b)), sentinel("67389"), real); err != nil || !idle {
		t.Errorf("stale sentinel: idle=%v err=%v, want the shell at its prompt", idle, err)
	}
	// Sentinel naming a live process that is not cmux: same.
	if idle, err := shellAtPrompt(mk(string(b)), sentinel("2609"), real); err != nil || !idle {
		t.Errorf("sentinel on a non-cmux process: idle=%v err=%v", idle, err)
	}
	// Two cmux processes and a stale sentinel: unknown, never a guess.
	two := string(b) + "98099     1 98099     0 ??       /Applications/cmux.app/Contents/MacOS/cmux\n"
	if _, err := shellAtPrompt(mk(two), sentinel("67389"), real); err == nil {
		t.Error("two cmux, stale sentinel: want refused")
	}
	// No cmux at all: unknown.
	none := strings.Replace(string(b), "/Applications/cmux.app/Contents/MacOS/cmux", "/usr/bin/other", 1)
	if _, err := shellAtPrompt(mk(none), sentinel("67389"), real); err == nil {
		t.Error("no cmux, stale sentinel: want refused")
	}
}
