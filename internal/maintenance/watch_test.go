package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	w := NewWatch(dir)
	if _, ok, err := w.Current(); ok || err != nil {
		t.Fatal("absent")
	}
	st := onState()
	_ = Save(dir, st)
	got, ok, _ := w.Current()
	if !ok || !got.On {
		t.Fatal("not reloaded after Save")
	}
	_ = Lift(dir, st.ID, t0, "", "", "")
	if got, _, _ = w.Current(); got.On || got.FrozenFor("x") {
		t.Fatal("not reloaded after off")
	}
}

// D8: an unreadable state is reported as an error; every consumer refuses on it.
func TestWatchCorruptReportsError(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{tronqué"), 0o600)
	_, ok, err := NewWatch(dir).Current()
	if err == nil || ok {
		t.Fatalf("corrupt: ok=%v err=%v", ok, err)
	}
}

// D8: only "does not exist" means no maintenance; a permission error is an
// unreadable state (frozen), for Stat and for the read alike.
func TestWatchPermissionErrorIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes — run as a normal user")
	}
	dir := t.TempDir()
	_ = Save(dir, onState())
	f := filepath.Join(dir, FileName)
	_ = os.Chmod(f, 0o000)
	defer os.Chmod(f, 0o600)
	if _, ok, err := NewWatch(dir).Current(); err == nil || ok {
		t.Fatalf("unreadable file: ok=%v err=%v", ok, err)
	}
	_ = os.Chmod(f, 0o600)
	_ = os.Chmod(dir, 0o000) // Stat itself fails
	defer os.Chmod(dir, 0o700)
	if _, ok, err := NewWatch(dir).Current(); err == nil || ok || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unstatable file: ok=%v err=%v", ok, err)
	}
}

func TestUnreadableMessage(t *testing.T) {
	m := UnreadableMessage("/d", errors.New("unexpected EOF"))
	for _, want := range []string{"unreadable", "/d/maintenance.json", "unexpected EOF", "frozen",
		"the user runs `pigtail maintenance off` outside Claude Code (who set it is unreadable too)"} {
		if !strings.Contains(m, want) {
			t.Fatalf("missing %q in %q", want, m)
		}
	}
}

// The refusal and the notice name the state file exactly once: the path has
// one source (the message), never also the error (verify N2, t1: « (…/maintenance.json:
// …/maintenance.json: unexpected end of JSON input) »).
func TestUnreadableMessagesNameThePathOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	_ = os.WriteFile(path, []byte("{"), 0o600)
	_, _, loadErr := Load(dir)
	_, _, watchErr := NewWatch(dir).Current()
	errs := map[string]error{"Load, corrupt": loadErr, "Watch, corrupt": watchErr}
	if os.Geteuid() != 0 {
		_ = Save(dir, onState())
		_ = os.Chmod(path, 0o000) // read error
		_, _, errs["Load, unreadable"] = Load(dir)
		_ = os.Chmod(path, 0o600)
		_ = os.Chmod(dir, 0o000) // stat error
		_, _, errs["Watch, unstatable"] = NewWatch(dir).Current()
		_ = os.Chmod(dir, 0o700)
	}
	for name, err := range errs {
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		for kind, m := range map[string]string{"UnreadableMessage": UnreadableMessage(dir, err), "CorruptNotice": CorruptNotice(dir, err)} {
			if n := strings.Count(m, path); n != 1 {
				t.Errorf("%s, %s: path %d times in %q", name, kind, n, m)
			}
		}
	}
}

// Focus D26: the store holds focus seals while a freeze is on; an unreadable
// state is frozen (D8); a maintenance without --freeze holds nothing.
func TestSealsHeld(t *testing.T) {
	if SealsHeld(nil) {
		t.Fatal("nil watch holds seals")
	}
	dir := t.TempDir()
	w := NewWatch(dir)
	if SealsHeld(w) {
		t.Fatal("no maintenance.json holds seals")
	}
	_ = Save(dir, State{On: true, ID: "mnt_0123456789abcdef", Since: t0})
	if SealsHeld(w) {
		t.Fatal("maintenance without --freeze holds seals")
	}
	_ = Save(dir, State{On: true, Freeze: true, ID: "mnt_0123456789abcdee", Since: t0})
	if !SealsHeld(w) {
		t.Fatal("freeze does not hold seals")
	}
	if os.Geteuid() != 0 {
		f := filepath.Join(dir, FileName)
		_ = os.Chmod(f, 0o000)
		if !SealsHeld(NewWatch(dir)) {
			t.Fatal("unreadable state does not hold seals")
		}
		_ = os.Chmod(f, 0o600)
	}
	_ = os.Remove(filepath.Join(dir, FileName))
	if SealsHeld(w) {
		t.Fatal("removed state still holds seals")
	}
}
