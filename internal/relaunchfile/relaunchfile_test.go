package relaunchfile

import (
	"os"
	"path/filepath"
	"testing"
)

// The shared <key>.sid claim, as respawn and move use it.
func TestSidClaim(t *testing.T) {
	newPath := func(t *testing.T) string { return filepath.Join(t.TempDir(), "_dev_ttys9.sid") }
	read := func(t *testing.T, p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("<key>.sid: %v", err)
		}
		return string(b)
	}
	gone := func(t *testing.T, p string) {
		t.Helper()
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("<key>.sid still there: %v", err)
		}
	}

	t.Run("writes the hook's format, 0600, and removes ours", func(t *testing.T) {
		p := newPath(t)
		c, err := ClaimSid(p, "sid-a")
		if err != nil {
			t.Fatal(err)
		}
		if got := read(t, p); got != "sid-a\n" {
			t.Fatalf("<key>.sid = %q, want %q", got, "sid-a\n")
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
		entries, _ := os.ReadDir(filepath.Dir(p))
		if len(entries) != 1 {
			t.Fatalf("dir holds %d entries, want only the .sid (no temp left behind)", len(entries))
		}
		c.Release()
		gone(t, p)
	})
	t.Run("rewritten by another is kept", func(t *testing.T) {
		p := newPath(t)
		c, err := ClaimSid(p, "sid-a")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("sid-other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c.Release()
		if got := read(t, p); got != "sid-other\n" {
			t.Fatalf("<key>.sid = %q, want the other id kept", got)
		}
	})
	t.Run("never written removes nothing", func(t *testing.T) {
		p := newPath(t)
		if err := os.WriteFile(p, []byte("sid-a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var c *SidClaim // nothing claimed
		c.Release()
		if got := read(t, p); got != "sid-a\n" {
			t.Fatalf("<key>.sid = %q, want the hook's file kept", got)
		}
	})
	t.Run("released once: a later same-id file is kept", func(t *testing.T) {
		p := newPath(t)
		c, err := ClaimSid(p, "sid-a")
		if err != nil {
			t.Fatal(err)
		}
		c.Release()
		if err := os.WriteFile(p, []byte("sid-a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c.Release()
		if got := read(t, p); got != "sid-a\n" {
			t.Fatalf("<key>.sid = %q, want a second release to touch nothing", got)
		}
	})
	t.Run("rewrite after release takes a new claim", func(t *testing.T) {
		p := newPath(t)
		c1, err := ClaimSid(p, "sid-a")
		if err != nil {
			t.Fatal(err)
		}
		c1.Release()
		gone(t, p)
		c2, err := ClaimSid(p, "sid-b")
		if err != nil {
			t.Fatal(err)
		}
		if got := read(t, p); got != "sid-b\n" {
			t.Fatalf("<key>.sid = %q, want %q", got, "sid-b\n")
		}
		c1.Release() // the spent claim leaves the new write alone
		if got := read(t, p); got != "sid-b\n" {
			t.Fatalf("<key>.sid = %q after the old claim's release", got)
		}
		c2.Release()
		gone(t, p)
	})
	t.Run("ReleaseSid", func(t *testing.T) {
		p := newPath(t)
		if err := os.WriteFile(p, []byte("sid-a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ReleaseSid(p, "sid-a"); err != nil {
			t.Fatal(err)
		}
		gone(t, p)

		if err := os.WriteFile(p, []byte("sid-other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ReleaseSid(p, "sid-a"); err != nil {
			t.Fatalf("foreign id: %v, want nil", err)
		}
		if got := read(t, p); got != "sid-other\n" {
			t.Fatalf("<key>.sid = %q, want the foreign id kept", got)
		}

		if err := ReleaseSid(filepath.Join(t.TempDir(), "absent.sid"), "sid-a"); err != nil {
			t.Fatalf("absent file: %v, want nil", err)
		}
	})
	t.Run("unwritable dir fails and claims nothing", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "missing", "_dev_ttys9.sid")
		c, err := ClaimSid(p, "sid-a")
		if err == nil || c != nil {
			t.Fatalf("ClaimSid = %v, %v; want nil claim and an error", c, err)
		}
	})
}

func TestSidPathAllowed(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{filepath.Join(dir, "_dev_ttys001.sid"), true},
		{filepath.Join(dir, "x.handoff"), false},
		{dir + "/../evil.sid", false},
		{"/etc/x.sid", false},
		{filepath.Join(dir, "sub", "x.sid"), false},
		{"", false},
	} {
		err := SidPathAllowed(dir, tc.path)
		if (err == nil) != tc.ok {
			t.Errorf("SidPathAllowed(%q) = %v, want ok=%v", tc.path, err, tc.ok)
		}
	}
}

func TestDir(t *testing.T) {
	env := map[string]string{"HOME": "/h"}
	get := func(k string) string { return env[k] }
	if got := Dir(get); got != "/h/.local/state/claude-relaunch" {
		t.Fatalf("Dir = %q", got)
	}
	env["XDG_STATE_HOME"] = "/x"
	if got := Dir(get); got != "/x/claude-relaunch" {
		t.Fatalf("Dir = %q", got)
	}
}
