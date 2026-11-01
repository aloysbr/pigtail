package cmux

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeSettings(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// stubDefaults replaces the UserDefaults reader for the test: never the real Mac.
func stubDefaults(t *testing.T, val string, found bool) {
	t.Helper()
	old := ReadDefaults
	ReadDefaults = func(key string) (string, bool, error) {
		if key != AgentAutoResumeKey {
			t.Errorf("defaults read of %q, want %q", key, AgentAutoResumeKey)
		}
		return val, found, nil
	}
	t.Cleanup(func() { ReadDefaults = old })
}

// D1 (sub_x000000000000037): pigtail must know whether cmux resumes the
// sessions itself. Absent file or key = cmux's default (on); an unreadable
// file is "unknown", never "off".
func TestAgentAutoResume(t *testing.T) {
	stubDefaults(t, "", false)
	cases := []struct {
		name, body string
		on, known  bool
	}{
		{"no key", "{\n  // comment\n  \"sidebar\": {},\n}\n", true, true},
		{"off", "{\n  \"terminal\": {\n    // D1\n    \"autoResumeAgentSessions\": false,\n  },\n}\n", false, true},
		{"on", `{"terminal": {"autoResumeAgentSessions": true}}`, true, true},
		{"garbage", "{ not json", true, false},
	}
	for _, c := range cases {
		on, known := AgentAutoResume(writeSettings(t, c.body))
		if on != c.on || known != c.known {
			t.Errorf("%s: on=%v known=%v, want %v %v", c.name, on, known, c.on, c.known)
		}
	}
	if on, known := AgentAutoResume(filepath.Join(t.TempDir(), "absent.json")); !on || !known {
		t.Errorf("absent file: on=%v known=%v, want cmux's default (on, known)", on, known)
	}
}

// The setting can also live in UserDefaults (cmux's Settings window): read
// when settings.json does not carry the key; the file wins when it does.
func TestAgentAutoResumeUserDefaultsFallback(t *testing.T) {
	for _, c := range []struct {
		val string
		on  bool
	}{{"0", false}, {"false", false}, {"NO", false}, {"1", true}, {"true", true}} {
		stubDefaults(t, c.val, true)
		if on, known := AgentAutoResume(writeSettings(t, `{}`)); on != c.on || !known {
			t.Errorf("defaults %q: on=%v known=%v, want %v known", c.val, on, known, c.on)
		}
	}
	stubDefaults(t, "0", true)
	if on, known := AgentAutoResume(filepath.Join(t.TempDir(), "absent.json")); on || !known {
		t.Errorf("absent file + defaults 0: on=%v known=%v, want off known", on, known)
	}
	if on, known := AgentAutoResume(writeSettings(t, `{"terminal":{"autoResumeAgentSessions":true}}`)); !on || !known {
		t.Errorf("file says on, defaults 0: on=%v known=%v, want the file to win", on, known)
	}
	stubDefaults(t, "garbage", true)
	if on, known := AgentAutoResume(writeSettings(t, `{}`)); !on || known {
		t.Errorf("unparseable defaults value: on=%v known=%v, want unknown", on, known)
	}
	if on, known := AgentAutoResume(writeSettings(t, `{ not json`)); !on || known {
		t.Errorf("invalid file + defaults: on=%v known=%v, want unknown (never off)", on, known)
	}
}

// Review round 1: only « key does not exist » is absence. A defaults that
// fails otherwise (timeout, no binary, another exit) is unknown — never on.
func TestAgentAutoResumeDefaultsErrorIsUnknown(t *testing.T) {
	old := ReadDefaults
	ReadDefaults = func(string) (string, bool, error) { return "", false, errors.New("defaults: timed out") }
	t.Cleanup(func() { ReadDefaults = old })
	for name, path := range map[string]string{"key absent from file": writeSettings(t, `{}`), "no file": filepath.Join(t.TempDir(), "absent.json")} {
		if on, known := AgentAutoResume(path); !on || known {
			t.Errorf("%s: on=%v known=%v, want unknown (on, not known)", name, on, known)
		}
	}
	// The file still wins: no defaults read needed when it carries the key.
	if on, known := AgentAutoResume(writeSettings(t, `{"terminal":{"autoResumeAgentSessions":false}}`)); on || !known {
		t.Errorf("file with the key: on=%v known=%v, want off known", on, known)
	}
}

// Review round 2: the real exec path of ReadDefaults, with `defaults` a
// PATH-stubbed script (independent of the OS: no `defaults` on Linux CI).
// macOS 27 words absence as "Could not find key" / "Domain … not found", in
// every locale: exit status 1 is the absence, whatever the wording.
func TestReadDefaultsExecPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh stub")
	}
	for _, c := range []struct {
		name, script string
		val          string
		found, err   bool
	}{
		{"could not find key", "echo \"Error: Could not find key 'k' in domain 'com.cmuxterm.app'.\" >&2; exit 1", "", false, false},
		{"domain not found", "echo \"Error: Domain 'com.cmuxterm.app' not found.\" >&2; exit 1", "", false, false},
		{"older wording", "echo \"The domain/default pair of (x, k) does not exist\" >&2; exit 1", "", false, false},
		{"value 0", "echo 0", "0", true, false},
		{"value 1", "echo 1", "1", true, false},
		{"exit 2 is unknown", "echo boom >&2; exit 2", "", false, true},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "defaults"), []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		val, found, err := ReadDefaults(AgentAutoResumeKey)
		if val != c.val || found != c.found || (err != nil) != c.err {
			t.Errorf("%s: got (%q, %v, %v), want (%q, %v, err=%v)", c.name, val, found, err, c.val, c.found, c.err)
		}
	}
}
