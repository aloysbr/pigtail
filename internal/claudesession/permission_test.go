package claudesession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionMode(t *testing.T) {
	cases := map[string]string{
		"/x/bin/claude --settings /t/cmux-claude-settings.a":             "",
		"/x/bin/claude --resume abc --permission-mode auto":              "auto",
		"/x/bin/claude --permission-mode=plan":                           "plan",
		"claude --permission-mode default --permission-mode acceptEdits": "acceptEdits",
		"claude --dangerously-skip-permissions":                          ModeBypass,
		"claude --permission-mode auto --dangerously-skip-permissions":   ModeBypass,
		"claude --permission-mode bypassPermissions":                     ModeBypass,
		"claude '/example-skill --permission-mode-ish text'":             "",
	}
	for args, want := range cases {
		if got := PermissionMode(args); got != want {
			t.Errorf("PermissionMode(%q) = %q, want %q", args, got, want)
		}
	}
}

func TestDefaultPermissionMode(t *testing.T) {
	dir := t.TempDir()
	if m, err := DefaultPermissionMode(dir); err != nil || m != "" {
		t.Fatalf("no settings.json = %q %v, want empty, nil", m, err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"permissions":{"defaultMode":"auto","allow":[]}}`)
	if m, err := DefaultPermissionMode(dir); err != nil || m != "auto" {
		t.Fatalf("auto = %q %v", m, err)
	}
	write(`{"model":"x"}`)
	if m, err := DefaultPermissionMode(dir); err != nil || m != "" {
		t.Fatalf("no defaultMode = %q %v", m, err)
	}
	write(`{broken`)
	if _, err := DefaultPermissionMode(dir); err == nil {
		t.Fatal("unparsable settings.json must be an error, never a silent default")
	}
}

func TestStricter(t *testing.T) {
	cases := []struct {
		a, b, want string
		ok         bool
	}{
		{"auto", "plan", "plan", true},
		{"default", "auto", "default", true},
		{"acceptEdits", "auto", "acceptEdits", true},
		{"dontAsk", "default", "dontAsk", true},
		{"auto", "auto", "auto", true},
		{"auto", ModeBypass, "", false},
		{"weird", "auto", "", false},
	}
	for _, c := range cases {
		if got, ok := Stricter(c.a, c.b); got != c.want || ok != c.ok {
			t.Errorf("Stricter(%q, %q) = %q %v, want %q %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestPidSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "4242.json"), []byte(`{"pid":4242,"sessionId":"abc-1","cwd":"/w"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if sid, ok := PidSession(dir, 4242); !ok || sid != "abc-1" {
		t.Fatalf("PidSession = %q %v", sid, ok)
	}
	if _, ok := PidSession(dir, 1); ok {
		t.Fatal("no pid file must be not found")
	}
}
