package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// layout: home/Developer (dev) /home (scope) /repo (key) ; scope/repo-wt/x (worktree)
type fx struct{ home, dev, scope, key, wt, cfg string }

func newFx(t *testing.T) fx {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	f := fx{home: filepath.Join(base, "home")}
	f.dev = filepath.Join(f.home, "Developer")
	f.scope = filepath.Join(f.dev, "home")
	f.key = filepath.Join(f.scope, "repo")
	f.wt = filepath.Join(f.scope, "repo-wt", "x")
	f.cfg = filepath.Join(base, "cell")
	for _, d := range []string{f.key, f.wt, f.cfg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f fx) req() Request { return Request{ConfigDir: f.cfg, Key: f.key, Worktree: f.wt, Home: f.home} }

func (f fx) write(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.cfg, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fx) read(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.cfg, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parse(t *testing.T, b []byte) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func trustOf(t *testing.T, b []byte, key string) any {
	p, _ := parse(t, b)["projects"].(map[string]any)
	e, _ := p[key].(map[string]any)
	return e["hasTrustDialogAccepted"]
}

func TestPreTrustInheritsFromParent(t *testing.T) {
	f := newFx(t)
	f.write(t, `{"projects":{"`+f.scope+`":{"hasTrustDialogAccepted":true}}}`)
	r := PreTrust(f.req())
	if r.Outcome != Written || r.Key != f.key || r.Ancestor != f.scope {
		t.Fatalf("result %+v", r)
	}
	b := f.read(t)
	if trustOf(t, b, f.key) != true {
		t.Fatalf("key not trusted: %s", b)
	}
	e := parse(t, b)["projects"].(map[string]any)[f.key].(map[string]any)
	for _, k := range []string{"allowedTools", "mcpContextUris", "mcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "hasClaudeMdExternalIncludesApproved", "hasClaudeMdExternalIncludesWarningShown"} {
		if _, ok := e[k]; !ok {
			t.Fatalf("new entry lacks Claude default %q: %v", k, e)
		}
	}
}

// D3: a grant on ~/Developer (two levels above the repo) counts — the perso cells' case.
func TestPreTrustDeveloperCounts(t *testing.T) {
	f := newFx(t)
	f.write(t, `{"projects":{"`+f.dev+`":{"hasTrustDialogAccepted":true}}}`)
	if r := PreTrust(f.req()); r.Outcome != Written || r.Ancestor != f.dev {
		t.Fatalf("result %+v", r)
	}
}

func TestPreTrustInheritsFromWorktreeAncestor(t *testing.T) {
	f := newFx(t)
	f.write(t, `{"projects":{"`+filepath.Dir(f.wt)+`":{"hasTrustDialogAccepted":true}}}`)
	if r := PreTrust(f.req()); r.Outcome != Written || r.Ancestor != filepath.Dir(f.wt) {
		t.Fatalf("result %+v", r)
	}
}

func TestPreTrustPreservesEveryField(t *testing.T) {
	f := newFx(t)
	before := `{"oauthAccount":{"emailAddress":"a@b"},"numStartups":12345678901234567890,` +
		`"tip":"<a & b>","projects":{"` + f.scope + `":{"hasTrustDialogAccepted":true,"x":[1,{"y":null}]},` +
		`"/other":{"allowedTools":["Bash"],"hasTrustDialogAccepted":false}}}`
	f.write(t, before)
	if r := PreTrust(f.req()); r.Outcome != Written {
		t.Fatalf("result %+v", r)
	}
	got, want := parse(t, f.read(t)), parse(t, []byte(before))
	delete(got["projects"].(map[string]any), f.key)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields changed:\n got %v\nwant %v", got, want)
	}
	out := f.read(t)
	for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`} { // Go's HTML escaping of < > &
		if bytes.Contains(out, []byte(esc)) {
			t.Fatalf("HTML-escaped %s in a string Claude wrote verbatim: %s", esc, out)
		}
	}
	if !bytes.Contains(out, []byte(`"<a & b>"`)) {
		t.Fatalf("string not kept verbatim: %s", out)
	}
}

func TestPreTrustExistingEntryKeepsFields(t *testing.T) {
	f := newFx(t)
	f.write(t, `{"projects":{"`+f.scope+`":{"hasTrustDialogAccepted":true},"`+f.key+`":{"allowedTools":["Read"],"hasTrustDialogAccepted":false}}}`)
	if r := PreTrust(f.req()); r.Outcome != Written {
		t.Fatalf("result %+v", r)
	}
	e := parse(t, f.read(t))["projects"].(map[string]any)[f.key].(map[string]any)
	if e["hasTrustDialogAccepted"] != true || !reflect.DeepEqual(e["allowedTools"], []any{"Read"}) || len(e) != 2 {
		t.Fatalf("entry %v", e)
	}
}

func TestPreTrustNoAncestorWritesNothing(t *testing.T) {
	f := newFx(t)
	body := `{"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`
	f.write(t, body)
	if r := PreTrust(f.req()); r.Outcome != NoTrustedAncestor {
		t.Fatalf("result %+v", r)
	}
	if string(f.read(t)) != body {
		t.Fatal("file changed")
	}
}

func TestPreTrustFalseAncestorIgnored(t *testing.T) {
	f := newFx(t)
	body := `{"projects":{"` + f.scope + `":{"hasTrustDialogAccepted":false},"` + f.dev + `":{"hasTrustDialogAccepted":false}}}`
	f.write(t, body)
	if r := PreTrust(f.req()); r.Outcome != NoTrustedAncestor || string(f.read(t)) != body {
		t.Fatalf("result %+v", r)
	}
}

// D3: $HOME itself and everything above it never count, one grant at a time.
func TestPreTrustHomeOrAboveNeverCounts(t *testing.T) {
	f := newFx(t)
	for _, only := range []string{f.home, filepath.Dir(f.home), "/"} {
		body := `{"projects":{"` + only + `":{"hasTrustDialogAccepted":true}}}`
		f.write(t, body)
		if r := PreTrust(f.req()); r.Outcome != NoTrustedAncestor || string(f.read(t)) != body {
			t.Fatalf("only %s trusted: %+v", only, r)
		}
	}
}

func TestPreTrustAlreadyTrusted(t *testing.T) {
	f := newFx(t)
	body := `{"projects":{"` + f.scope + `":{"hasTrustDialogAccepted":true},"` + f.key + `":{"hasTrustDialogAccepted":true}}}`
	f.write(t, body)
	if r := PreTrust(f.req()); r.Outcome != AlreadyTrusted || string(f.read(t)) != body {
		t.Fatalf("result %+v", r)
	}
}

// A repo or worktree that is $HOME or lies outside it is not eligible.
func TestPreTrustKeyNotBelowHome(t *testing.T) {
	f := newFx(t)
	body := `{"projects":{"/":{"hasTrustDialogAccepted":true}}}`
	f.write(t, body)
	for _, key := range []string{f.home, filepath.Join(filepath.Dir(f.home), "elsewhere", "repo")} {
		r := f.req()
		r.Key = key
		if res := PreTrust(r); res.Outcome != NotEligible || string(f.read(t)) != body {
			t.Fatalf("key %s: %+v", key, res)
		}
	}
	r := f.req()
	r.Key = "relative/repo"
	if res := PreTrust(r); res.Outcome != NotEligible {
		t.Fatalf("relative key: %+v", res)
	}
}

func TestPreTrustSymlinkedHome(t *testing.T) {
	f := newFx(t)
	link := filepath.Join(filepath.Dir(f.cfg), "homelink")
	if err := os.Symlink(f.home, link); err != nil {
		t.Fatal(err)
	}
	f.write(t, `{"projects":{"`+f.dev+`":{"hasTrustDialogAccepted":true}}}`)
	r := f.req()
	r.Home = link // $HOME given through a symlink, git paths are realpaths
	if res := PreTrust(r); res.Outcome != Written {
		t.Fatalf("%+v", res)
	}
}

// Claude reads a legacy .config.json first, and another file under a custom
// OAuth URL: pigtail does not write a file Claude would not read.
func TestPreTrustOtherConfigFileSkips(t *testing.T) {
	f := newFx(t)
	body := `{"projects":{"` + f.scope + `":{"hasTrustDialogAccepted":true}}}`
	f.write(t, body)
	legacy := filepath.Join(f.cfg, ".config.json")
	if err := os.WriteFile(legacy, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := PreTrust(f.req()); r.Outcome != NotEligible || string(f.read(t)) != body {
		t.Fatalf("legacy .config.json: %+v", r)
	}
	_ = os.Remove(legacy)
	t.Setenv("CLAUDE_CODE_CUSTOM_OAUTH_URL", "https://example.invalid")
	if r := PreTrust(f.req()); r.Outcome != NotEligible || string(f.read(t)) != body {
		t.Fatalf("custom OAuth URL: %+v", r)
	}
}

func TestPreTrustMissingOrBrokenFile(t *testing.T) {
	f := newFx(t)
	if r := PreTrust(f.req()); r.Outcome != NoTrustedAncestor {
		t.Fatalf("missing file: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(f.cfg, FileName)); !os.IsNotExist(err) {
		t.Fatal("created a config file")
	}
	f.write(t, `{"projects":`)
	if r := PreTrust(f.req()); r.Outcome != Failed || string(f.read(t)) != `{"projects":` {
		t.Fatalf("broken file: %+v", r)
	}
}

func TestPreTrustLockedFails(t *testing.T) {
	shortLock(t)
	f := newFx(t)
	body := `{"projects":{"` + f.scope + `":{"hasTrustDialogAccepted":true}}}`
	f.write(t, body)
	if err := os.Mkdir(filepath.Join(f.cfg, FileName+".lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := PreTrust(f.req())
	if r.Outcome != Failed || string(f.read(t)) != body {
		t.Fatalf("%+v", r)
	}
}

func TestPreTrustReleasesLock(t *testing.T) {
	f := newFx(t)
	f.write(t, `{"projects":{"`+f.scope+`":{"hasTrustDialogAccepted":true}}}`)
	_ = PreTrust(f.req())
	if _, err := os.Stat(filepath.Join(f.cfg, FileName+".lock")); !os.IsNotExist(err) {
		t.Fatal("lock left behind")
	}
}

// AC5: the lock is removed on every failure path taken after it was acquired.
func TestPreTrustReleasesLockOnLockedFailures(t *testing.T) {
	lock := func(f fx) string { return filepath.Join(f.cfg, FileName+".lock") }
	t.Run("file corrupted between the first read and the locked re-read", func(t *testing.T) {
		f := newFx(t)
		f.write(t, `{"projects":{"`+f.scope+`":{"hasTrustDialogAccepted":true}}}`)
		saved := lockedHook
		t.Cleanup(func() { lockedHook = saved })
		lockedHook = func(path string) { _ = os.WriteFile(path, []byte(`{"projects":`), 0o600) }
		if r := PreTrust(f.req()); r.Outcome != Failed {
			t.Fatalf("%+v", r)
		}
		if _, err := os.Stat(lock(f)); !os.IsNotExist(err) {
			t.Fatal("lock left behind after a failed re-read")
		}
	})
	t.Run("write fails", func(t *testing.T) {
		f := newFx(t)
		body := `{"projects":{"` + f.scope + `":{"hasTrustDialogAccepted":true}}}`
		f.write(t, body)
		saved := writeConfig
		t.Cleanup(func() { writeConfig = saved })
		writeConfig = func(string, []byte) error { return errors.New("read-only file system") }
		if r := PreTrust(f.req()); r.Outcome != Failed || string(f.read(t)) != body {
			t.Fatalf("%+v", r)
		}
		if _, err := os.Stat(lock(f)); !os.IsNotExist(err) {
			t.Fatal("lock left behind after a failed write")
		}
	})
}

// "projects": null, a project entry that is null, and a file that is null
// must not panic; a null entry reads as absent (Claude: projects[key] ?? defaults).
func TestPreTrustNullValues(t *testing.T) {
	f := newFx(t)
	for _, body := range []string{`{"projects":null}`, `null`} {
		f.write(t, body)
		if r := PreTrust(f.req()); r.Outcome != NoTrustedAncestor || string(f.read(t)) != body {
			t.Fatalf("%s: %+v", body, r)
		}
	}
	f.write(t, `{"projects":{"`+f.scope+`":{"hasTrustDialogAccepted":true},"`+f.key+`":null}}`)
	if r := PreTrust(f.req()); r.Outcome != Written {
		t.Fatalf("null key entry: %+v", r)
	}
	e := parse(t, f.read(t))["projects"].(map[string]any)[f.key].(map[string]any)
	if e["hasTrustDialogAccepted"] != true || e["allowedTools"] == nil {
		t.Fatalf("null entry not replaced by the defaults: %v", e)
	}
}
