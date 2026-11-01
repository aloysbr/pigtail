package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// backfillEnv is a test daemon whose static-server list maps port 9101 to
// serverDir, plus an empty claude-opens log directory.
type backfillEnv struct {
	s         *stubDaemon
	st        *store.Store
	logs      string
	serverDir string
}

func newBackfillEnv(t *testing.T) *backfillEnv {
	t.Helper()
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	e := &backfillEnv{logs: t.TempDir(), serverDir: t.TempDir()}
	views := []pigtail.StaticServerView{{StaticServer: pigtail.StaticServer{ID: "ss_1", Name: "m", Dir: e.serverDir, Port: 9101}}}
	e.s, e.st = realSubjectDaemonWith(t, func(r chi.Router, _ *store.Store) {
		r.Get("/static-servers", func(w http.ResponseWriter, _ *http.Request) { writeData(w, http.StatusOK, views) })
	})
	return e
}

func (e *backfillEnv) file(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(e.serverDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *backfillEnv) log(t *testing.T, name string, entries ...map[string]any) {
	t.Helper()
	var b strings.Builder
	for _, en := range entries {
		j, err := json.Marshal(en)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(e.logs, name+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *backfillEnv) run(t *testing.T, extra ...string) string {
	t.Helper()
	out, _, err := runCobraIO(t, e.s, subjectCmd, append([]string{"output", "backfill", "--dir", e.logs}, extra...)...)
	if err != nil {
		t.Fatalf("backfill: %v (out %q)", err, out)
	}
	return out
}

func wantSummary(t *testing.T, out string, registered, skipped, missing, already int) {
	t.Helper()
	want := "inscrites " + strconv.Itoa(registered) + "\nsautées (sans sujet) " + strconv.Itoa(skipped) +
		"\ndisparues " + strconv.Itoa(missing) + "\ndéjà au registre " + strconv.Itoa(already) + "\n"
	if out != want {
		t.Fatalf("summary =\n%q\nwant\n%q", out, want)
	}
}

func paths(rows []pigtail.SubjectOutput) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Path)
	}
	return out
}

func TestBackfillParsesTargetItemsArgv(t *testing.T) {
	e := newBackfillEnv(t)
	direct := e.file(t, "direct/a.html")
	a := e.file(t, "i1/index.html")
	b := e.file(t, "i2/index.html")
	c := e.file(t, "av/index.html")
	e.log(t, "s1",
		map[string]any{"type": "mockup", "subject_id": "sub_p", "ts": 1700000000.5, "target": direct, "is_path": true},
		map[string]any{"type": "report", "subject_id": "sub_p", "ts": 1700000001.0,
			"items": "1 http://localhost:9101/i1/\n2 http://127.0.0.1:9101/i2/index.html\n"},
		map[string]any{"type": "mockup", "subject_id": "sub_p", "ts": 1700000002.0,
			"argv": []string{"open", "http://localhost:9101/av/", "--marker", "x"}},
		map[string]any{"type": "server", "subject_id": "sub_p", "ts": 1700000003.0, "target": direct},
	)
	wantSummary(t, e.run(t), 4, 0, 0, 0)
	rows := outputRows(t, e.st, "sub_p")
	got := strings.Join(paths(rows), "|")
	for _, p := range []string{direct, a, b, c} {
		if !strings.Contains(got, p) {
			t.Errorf("path %s not registered; rows %s", p, got)
		}
	}
	for _, r := range rows {
		if r.Path == direct && (r.Kind != "mockup" || r.TS != 1700000000500 || r.Title != "a.html") {
			t.Errorf("direct row = %+v", r)
		}
	}
}

func TestBackfillResolvesPortToDir(t *testing.T) {
	e := newBackfillEnv(t)
	idx := e.file(t, "t1/index.html")
	other := e.file(t, "t1/other.html")
	e.log(t, "s1",
		map[string]any{"type": "mockup", "subject_id": "sub_p", "target": "http://localhost:9101/t1/?x=1#h"},
		map[string]any{"type": "mockup", "subject_id": "sub_p", "target": "http://localhost:9101/t1/other.html"},
	)
	wantSummary(t, e.run(t), 2, 0, 0, 0)
	got := paths(outputRows(t, e.st, "sub_p"))
	if len(got) != 2 || !strings.Contains(strings.Join(got, "|"), idx) || !strings.Contains(strings.Join(got, "|"), other) {
		t.Fatalf("paths = %v, want %s and %s", got, idx, other)
	}
}

func TestBackfillCountsSkippedAndMissing(t *testing.T) {
	e := newBackfillEnv(t)
	e.file(t, "ok/index.html")
	e.log(t, "s1",
		map[string]any{"type": "mockup", "subject_id": "sub_p", "target": "http://localhost:9101/ok/"},
		map[string]any{"type": "mockup", "target": "http://localhost:9101/ok/"},                       // no subject
		map[string]any{"type": "report", "subject_id": "sub_p", "target": "http://localhost:9999/x/"}, // unknown port
		map[string]any{"type": "report", "subject_id": "sub_p", "target": "http://localhost:9101/gone/"},
		map[string]any{"type": "report", "subject_id": "sub_p", "target": "file:///nowhere/x.html"},
		map[string]any{"type": "report", "subject_id": "sub_p", "target": "/nowhere/abs.html", "is_path": true},
	)
	wantSummary(t, e.run(t), 1, 1, 4, 0)
}

func TestBackfillIsIdempotent(t *testing.T) {
	e := newBackfillEnv(t)
	e.file(t, "a/index.html")
	e.file(t, "b/index.html")
	e.log(t, "s1",
		map[string]any{"type": "mockup", "subject_id": "sub_p", "target": "http://localhost:9101/a/"},
		map[string]any{"type": "mockup", "subject_id": "sub_p", "target": "http://localhost:9101/a/index.html"}, // same file: in-memory dedupe
		map[string]any{"type": "report", "subject_id": "sub_p", "target": "http://localhost:9101/b/"},
	)
	wantSummary(t, e.run(t), 2, 0, 0, 0)
	wantSummary(t, e.run(t), 0, 0, 0, 2)
	if n := len(outputRows(t, e.st, "sub_p")); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	e := newBackfillEnv(t)
	e.file(t, "a/index.html")
	e.log(t, "s1", map[string]any{"type": "mockup", "subject_id": "sub_p", "target": "http://localhost:9101/a/"})
	out := e.run(t, "--dry-run")
	if !strings.Contains(out, "inscrites 1") {
		t.Fatalf("dry-run out = %q", out)
	}
	if n := len(outputRows(t, e.st, "sub_p")); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	for _, p := range e.s.paths {
		if strings.HasPrefix(p, "POST /subjects/") {
			t.Fatalf("dry-run wrote: %s", p)
		}
	}
}

func TestBackfillReadsSubjectAlias(t *testing.T) {
	e := newBackfillEnv(t)
	e.file(t, "a/index.html")
	e.log(t, "s1", map[string]any{"type": "report", "subject": "sub_p", "target": "http://localhost:9101/a/"})
	wantSummary(t, e.run(t), 1, 0, 0, 0)
	if n := len(outputRows(t, e.st, "sub_p")); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestBackfillMissingDirFails(t *testing.T) {
	e := newBackfillEnv(t)
	notDir := filepath.Join(e.logs, "a-file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(e.logs, "nonexistent", "x"), notDir} {
		out, _, err := runCobraIO(t, e.s, subjectCmd, "output", "backfill", "--dir", d, "--dry-run")
		if err == nil {
			t.Fatalf("--dir %s: want an error, got nil (out %q)", d, out)
		}
		if !strings.Contains(err.Error(), d) || !strings.Contains(err.Error(), "--dir") {
			t.Fatalf("error %q must name the path and say to pass --dir", err)
		}
		if out != "" {
			t.Fatalf("no counter expected, got %q", out)
		}
	}
	// An existing but empty directory stays a success with zeros.
	wantSummary(t, e.run(t, "--dry-run"), 0, 0, 0, 0)
}
