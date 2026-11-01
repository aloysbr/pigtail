package claudesession

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeTranscript(t *testing.T, slug string, lines ...string) Transcript {
	t.Helper()
	tr := Transcript{ID: "s", ConfigDir: t.TempDir(), Slug: slug}
	must(t, os.MkdirAll(filepath.Dir(tr.JSONLPath()), 0o700))
	must(t, os.WriteFile(tr.JSONLPath(), []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return tr
}

func TestOriginCwd(t *testing.T) {
	long := `{"content":"` + strings.Repeat("x", 3<<20) + `"}`
	for _, c := range []struct {
		name, slug string
		lines      []string
		want       string
		ok         bool
	}{
		{"first matching the slug", "-w-orig", []string{`{"cwd":null}`, `{"cwd":"/w/orig/sub"}`, `{"cwd":"/w/orig"}`}, "/w/orig", true},
		{"none matches", "-w-orig", []string{`{"cwd":"/elsewhere"}`}, "", false},
		// measured on disk: '.' and '_' become '-' too
		{"dots and underscores", "-Users-x--claude-wt-a--outputs", []string{`{"cwd":"/Users/x/.claude/wt_a/_outputs"}`}, "/Users/x/.claude/wt_a/_outputs", true},
		{"3 MB line first", "-w", []string{long, `{"cwd":"/w"}`}, "/w", true},
	} {
		cwd, ok, err := OriginCwd(writeTranscript(t, c.slug, c.lines...))
		if err != nil || ok != c.ok || cwd != c.want {
			t.Errorf("%s: OriginCwd = %q, %v, %v; want %q, %v", c.name, cwd, ok, err, c.want, c.ok)
		}
	}
	if _, _, err := OriginCwd(Transcript{ID: "nope", ConfigDir: t.TempDir(), Slug: "-w"}); err == nil {
		t.Error("missing transcript: want an error")
	}
}

// appendBytes appends s to p.
func appendBytes(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteString(s)
	must(t, err)
	must(t, f.Close())
}

func TestCovers(t *testing.T) {
	sub := func(tr Transcript) string { return filepath.Join(tr.SidecarDir(), "subagents", "s.jsonl") }
	for _, c := range []struct {
		name      string
		alter     func(t *testing.T, head, stale Transcript)
		fwd, back bool // Covers(head, stale), Covers(stale, head)
	}{
		{"identical", func(*testing.T, Transcript, Transcript) {}, true, true},
		{"head has one more line", func(t *testing.T, h, _ Transcript) { appendBytes(t, h.JSONLPath(), "{\"n\":2}\n") }, true, false},
		{"same size, other bytes", func(t *testing.T, _, s Transcript) { must(t, os.WriteFile(s.JSONLPath(), []byte("[]\n"), 0o600)) }, false, false},
		{"longer but forks before stale's end", func(t *testing.T, h, _ Transcript) {
			must(t, os.WriteFile(h.JSONLPath(), []byte("{]\n{\"n\":2}\n"), 0o600))
		}, false, false},
		{"empty stale .jsonl", func(t *testing.T, _, s Transcript) { must(t, os.WriteFile(s.JSONLPath(), nil, 0o600)) }, true, false},
		{"empty stale .jsonl, sidecar file head lacks", func(t *testing.T, _, s Transcript) {
			must(t, os.WriteFile(s.JSONLPath(), nil, 0o600))
			must(t, os.WriteFile(sub(s)+".x", nil, 0o600))
		}, false, false},
		{"head's sidecar file longer", func(t *testing.T, h, _ Transcript) { appendBytes(t, sub(h), "{\"s\":2}\n") }, true, false},
		{"stale's sidecar file differs", func(t *testing.T, _, s Transcript) {
			must(t, os.WriteFile(sub(s), []byte("{\"s\":2}\n"), 0o600))
		}, false, false},
		{"extra sidecar file at head", func(t *testing.T, h, _ Transcript) { must(t, os.WriteFile(sub(h)+".x", nil, 0o600)) }, true, false},
		{"no sidecar at stale", func(t *testing.T, _, s Transcript) { must(t, os.RemoveAll(s.SidecarDir())) }, true, false},
		{"no sidecar at head", func(t *testing.T, h, _ Transcript) { must(t, os.RemoveAll(h.SidecarDir())) }, false, true},
		{"no sidecar either side", func(t *testing.T, h, s Transcript) {
			must(t, errors.Join(os.RemoveAll(h.SidecarDir()), os.RemoveAll(s.SidecarDir())))
		}, true, true},
	} {
		var tr [2]Transcript
		for i := range tr {
			tr[i] = Transcript{ID: "abc", ConfigDir: t.TempDir(), Slug: "-w"}
			seed(t, tr[i].ConfigDir, "-w", "abc", true)
			must(t, os.WriteFile(sub(tr[i]), []byte("{\"s\":1}\n"), 0o600))
		}
		head, stale := tr[0], tr[1]
		c.alter(t, head, stale)
		fwd, err := Covers(head, stale)
		if err != nil || fwd != c.fwd {
			t.Errorf("%s: Covers(head, stale) = %v, %v; want %v", c.name, fwd, err, c.fwd)
		}
		back, err := Covers(stale, head)
		if err != nil || back != c.back {
			t.Errorf("%s: Covers(stale, head) = %v, %v; want %v", c.name, back, err, c.back)
		}
	}
}

func TestLockSessionIsExclusive(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	unlock, err := LockSession("abc")
	must(t, err)
	if _, err := LockSession("abc"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second lock: %v; want ErrSessionBusy", err)
	}
	unlock()
	again, err := LockSession("abc")
	must(t, err)
	again()
}
