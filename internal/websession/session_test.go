package websession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionSecretPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", SessionFileName)
	a, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Value() == "" || a.Value() != b.Value() {
		t.Fatalf("reloading must read the same secret: %q vs %q", a.Value(), b.Value())
	}
	if !b.Check(a.Value()) {
		t.Fatal("the reloaded session must accept the first secret")
	}
}

func TestSessionNewFileNewSecret(t *testing.T) {
	dir := t.TempDir()
	a, _ := LoadSession(filepath.Join(dir, "a"))
	b, _ := LoadSession(filepath.Join(dir, "b"))
	if a.Value() == b.Value() {
		t.Fatal("two fresh session files share a secret")
	}
	if len(a.Value()) < 32 {
		t.Fatalf("secret %q is too short", a.Value())
	}
}

func TestSessionFileMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionFileName)
	if _, err := LoadSession(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSessionCompareConstantTime(t *testing.T) {
	s, err := LoadSession(filepath.Join(t.TempDir(), SessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	v := s.Value()
	if !s.Check(v) {
		t.Fatal("the right value must pass (positive control)")
	}
	for name, bad := range map[string]string{
		"wrong":     "x" + v[1:],
		"empty":     "",
		"truncated": v[:len(v)-1],
		"extended":  v + "0",
	} {
		if s.Check(bad) {
			t.Errorf("%s value accepted", name)
		}
	}
	var nilSession *Session
	if nilSession.Check(v) {
		t.Error("a nil session must reject every value")
	}
}

func TestSessionEmptyFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionFileName)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession(path); err == nil {
		t.Fatal("an empty session file must be an error, never an empty secret every cookie matches")
	}
}

func TestLoadSessionAdoptsLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacySessionFileName)
	if err := os.WriteFile(legacy, []byte("legacy-secret-0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSession(filepath.Join(dir, SessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if s.Value() != "legacy-secret-0123456789abcdef0123456789abcdef" {
		t.Fatalf("secret = %q, want the legacy one", s.Value())
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy file still present (err=%v)", err)
	}
}

func TestLoadSessionKeepsNewFileOverLegacy(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, SessionFileName)
	_ = os.WriteFile(newPath, []byte("new-secret-0123456789abcdef0123456789abcdef00"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, LegacySessionFileName), []byte("old-secret-0123456789abcdef0123456789abcdef00"), 0o600)
	s, err := LoadSession(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.Value() != "new-secret-0123456789abcdef0123456789abcdef00" {
		t.Fatalf("secret = %q, want the new file's", s.Value())
	}
}
