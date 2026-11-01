package staticsrv

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testHost = "127.0.0.1:7899"

// outputsFixture lays out a mockup round tree under a temp dir:
//
//	<root>/proj/mockup/t1/index.html
//	<root>/proj/mockup/t2/index.html, shot.png
//	<root>/proj/mockup/t9/index.html   (never registered)
//	<root>/proj/shots/cap.png          (a capture, the file alone)
//	<root>/proj/shots/other.png        (beside the capture, never registered)
type outputsFixture struct {
	root string
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newOutputsFixture(t *testing.T) outputsFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := outputsFixture{root: root}
	mustWrite(t, f.p("proj/mockup/t1/index.html"), "<h1>t1</h1>")
	mustWrite(t, f.p("proj/mockup/t2/index.html"), `<h1>t2</h1><a href="../t1/">t1</a>`)
	mustWrite(t, f.p("proj/mockup/t2/shot.png"), "PNG2")
	mustWrite(t, f.p("proj/mockup/t9/index.html"), "<h1>t9</h1>")
	mustWrite(t, f.p("proj/shots/cap.png"), "CAP")
	mustWrite(t, f.p("proj/shots/other.png"), "OTHER")
	return f
}

func (f outputsFixture) p(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

func staticSource(entries ...ServedEntry) func() (string, []ServedEntry, error) {
	return func() (string, []ServedEntry, error) { return "fp", entries, nil }
}

func newTestHandler(src func() (string, []ServedEntry, error)) http.Handler {
	return NewOutputsHandler(src, []string{testHost}, []string{"http://127.0.0.1:7800"}, nil)
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+testHost+path, nil)
	req.Host = testHost
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOutputsServesRegisteredTourAndSiblings(t *testing.T) {
	f := newOutputsFixture(t)
	h := newTestHandler(staticSource(
		ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t1/index.html")},
		ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t2/index.html")},
	))
	rec := do(t, h, http.MethodGet, ServedPath(f.p("proj/mockup/t2/index.html")))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<h1>t2</h1>") {
		t.Fatalf("t2/ = %d %q; want 200 with the tour's index", rec.Code, rec.Body.String())
	}
	for _, hdr := range []struct{ k, v string }{
		{"X-Content-Type-Options", "nosniff"},
		{"Content-Security-Policy", "frame-ancestors http://127.0.0.1:7800"},
		{"Cache-Control", "no-cache"},
	} {
		if got := rec.Header().Get(hdr.k); got != hdr.v {
			t.Errorf("%s = %q; want %q", hdr.k, got, hdr.v)
		}
	}
	if rec := do(t, h, http.MethodGet, f.p("proj/mockup/t2/shot.png")); rec.Code != http.StatusOK || rec.Body.String() != "PNG2" {
		t.Fatalf("sibling image = %d %q; want 200 PNG2", rec.Code, rec.Body.String())
	}
	// ../t1/ resolved by the browser from t2/: registered → served.
	if rec := do(t, h, http.MethodGet, f.p("proj/mockup/t1")+"/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "t1") {
		t.Fatalf("../t1/ = %d; want 200", rec.Code)
	}
	// ../t9/ exists on disk but is not registered → 404.
	if rec := do(t, h, http.MethodGet, f.p("proj/mockup/t9")+"/"); rec.Code != http.StatusNotFound {
		t.Fatalf("../t9/ = %d; want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, f.p("proj/mockup/t9/index.html")); rec.Code != http.StatusNotFound {
		t.Fatalf("t9/index.html = %d; want 404", rec.Code)
	}
}

func TestOutputsRefusesOutsideRegistry(t *testing.T) {
	f := newOutputsFixture(t)
	h := newTestHandler(staticSource(
		ServedEntry{Kind: "capture", Path: f.p("proj/shots/cap.png")},
		ServedEntry{Kind: "pr", Path: ""},
		ServedEntry{Kind: "link", Path: f.p("proj/mockup/t9/index.html")},
	))
	if rec := do(t, h, http.MethodGet, f.p("proj/shots/cap.png")); rec.Code != http.StatusOK || rec.Body.String() != "CAP" {
		t.Fatalf("registered capture = %d %q; want 200 CAP", rec.Code, rec.Body.String())
	}
	for _, p := range []string{
		f.p("proj/shots/other.png"),      // beside a capture: the file alone is served
		f.p("proj/shots") + "/",          // the capture's folder
		f.p("proj/mockup/t9/index.html"), // a link kind is never served
		f.p("proj/mockup/t1/index.html"), // on disk, not registered
		"/etc/hosts",                     // anything else
		"/",                              // the root
		f.p("proj/shots/missing.png"),    // missing
	} {
		if rec := do(t, h, http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d; want 404", p, rec.Code)
		}
	}
}

func TestOutputsRefusesSymlinkEscape(t *testing.T) {
	f := newOutputsFixture(t)
	secret := f.p("secret/key.txt")
	mustWrite(t, secret, "SECRET")
	// A link inside a registered tour pointing out of it.
	if err := os.Symlink(secret, f.p("proj/mockup/t2/leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.p("secret"), f.p("proj/mockup/t2/dir")); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(staticSource(ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t2/index.html")}))
	for _, p := range []string{f.p("proj/mockup/t2/leak.txt"), f.p("proj/mockup/t2/dir/key.txt"), f.p("proj/mockup/t2/dir") + "/"} {
		rec := do(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("GET %s = %d %q; want 404 without the secret", p, rec.Code, rec.Body.String())
		}
	}
	// An index.html that is a link out of the tour is not served either.
	mustWrite(t, f.p("proj/mockup/t5/x"), "")
	if err := os.Symlink(secret, f.p("proj/mockup/t5/index.html")); err != nil {
		t.Fatal(err)
	}
	h = newTestHandler(staticSource(ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t5/x")}))
	if rec := do(t, h, http.MethodGet, f.p("proj/mockup/t5")+"/"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "SECRET") {
		t.Errorf("t5/ with a linked index = %d %q; want 404", rec.Code, rec.Body.String())
	}
}

func TestOutputsRefusesDotfilesAndDotDot(t *testing.T) {
	f := newOutputsFixture(t)
	mustWrite(t, f.p("proj/mockup/t2/.env"), "TOKEN")
	mustWrite(t, f.p("proj/mockup/t2/.hidden/a.txt"), "HIDDEN")
	h := newTestHandler(staticSource(ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t2/index.html")}))
	for _, p := range []string{
		f.p("proj/mockup/t2") + "/.env",
		f.p("proj/mockup/t2") + "/.hidden/a.txt",
		f.p("proj/mockup/t2") + "/../t9/index.html",
		f.p("proj/mockup/t2") + "/../../../secret",
		f.p("proj/mockup/t2") + "/%2e%2e/t9/index.html",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/", nil)
		req.URL.Path = p
		if strings.Contains(p, "%2e") {
			req.URL.Path = strings.ReplaceAll(p, "%2e", ".")
		}
		req.Host = testHost
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d; want 404", p, rec.Code)
		}
	}
}

func TestOutputsChecksHost(t *testing.T) {
	f := newOutputsFixture(t)
	h := NewOutputsHandler(staticSource(ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t2/index.html")}),
		[]string{testHost, "localhost:7899"}, []string{"http://127.0.0.1:7800"}, nil)
	for host, want := range map[string]int{
		testHost:            http.StatusOK,
		"localhost:7899":    http.StatusOK,
		"evil.example:7899": http.StatusForbidden,
		"127.0.0.1:7800":    http.StatusForbidden,
		"":                  http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+testHost+f.p("proj/mockup/t2")+"/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q = %d; want %d", host, rec.Code, want)
		}
	}
}

func TestOutputsOnlyGetHead(t *testing.T) {
	f := newOutputsFixture(t)
	h := newTestHandler(staticSource(ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t2/index.html")}))
	p := f.p("proj/mockup/t2") + "/"
	if rec := do(t, h, http.MethodHead, p); rec.Code != http.StatusOK {
		t.Fatalf("HEAD = %d; want 200", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		if rec := do(t, h, m, p); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d; want 405", m, rec.Code)
		}
	}
}

func TestOutputsRebuildsOnFingerprintChange(t *testing.T) {
	f := newOutputsFixture(t)
	var (
		mu      sync.Mutex
		fp      = "1"
		entries = []ServedEntry{{Kind: "mockup", Path: f.p("proj/mockup/t1/index.html")}}
		calls   int
	)
	src := func() (string, []ServedEntry, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return fp, append([]ServedEntry(nil), entries...), nil
	}
	h := newTestHandler(src)
	t9 := f.p("proj/mockup/t9") + "/"
	if rec := do(t, h, http.MethodGet, t9); rec.Code != http.StatusNotFound {
		t.Fatalf("t9 before = %d; want 404", rec.Code)
	}
	// Same fingerprint, new entries: the index is not rebuilt.
	mu.Lock()
	entries = append(entries, ServedEntry{Kind: "mockup", Path: f.p("proj/mockup/t9/index.html")})
	mu.Unlock()
	if rec := do(t, h, http.MethodGet, t9); rec.Code != http.StatusNotFound {
		t.Fatalf("t9 same fingerprint = %d; want 404 (index kept)", rec.Code)
	}
	mu.Lock()
	fp = "2"
	mu.Unlock()
	if rec := do(t, h, http.MethodGet, t9); rec.Code != http.StatusOK {
		t.Fatalf("t9 after fingerprint change = %d; want 200", rec.Code)
	}
	// A purge: the fingerprint changes and the tour leaves the index.
	mu.Lock()
	fp, entries = "3", entries[:1]
	mu.Unlock()
	if rec := do(t, h, http.MethodGet, t9); rec.Code != http.StatusNotFound {
		t.Fatalf("t9 after purge = %d; want 404", rec.Code)
	}
	if calls < 4 {
		t.Fatalf("source called %d times; want once per request", calls)
	}
}

func TestServedPathTrimsIndex(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b/t3/index.html":    "/a/b/t3/",
		"/a/b/t3/report.html":   "/a/b/t3/report.html",
		"/a/b c/t3/index.html":  "/a/b%20c/t3/",
		"/a/é/cap #1.png":       "/a/%C3%A9/cap%20%231.png",
		"/a/b/../t3/index.html": "/a/t3/",
		"/a/q?x.png":            "/a/q%3Fx.png",
	} {
		if got := ServedPath(in); got != want {
			t.Errorf("ServedPath(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestOutputsRefusesTooWideRoot(t *testing.T) {
	f := newOutputsFixture(t)
	home := f.p("home")
	launch := f.p("home/Developer/scope-a")
	repo := f.p("home/code/repo")
	mustWrite(t, filepath.Join(home, "index.html"), "HOME")
	mustWrite(t, filepath.Join(launch, "index.html"), "LAUNCH")
	mustWrite(t, filepath.Join(launch, "notes.txt"), "NOTES")
	mustWrite(t, filepath.Join(repo, "index.html"), "REPO")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repo, "main.go"), "package main")
	t.Setenv("HOME", home)
	var logged []string
	h := NewOutputsHandler(staticSource(
		ServedEntry{Kind: "html", Path: filepath.Join(home, "index.html")},
		ServedEntry{Kind: "mockup", Path: filepath.Join(launch, "index.html")},
		ServedEntry{Kind: "report", Path: filepath.Join(repo, "index.html")},
	), []string{testHost}, []string{"http://127.0.0.1:7800"}, []string{launch})
	h.logf = func(format string, args ...any) { logged = append(logged, format) }
	for _, p := range []string{
		home + "/", filepath.Join(home, "index.html"),
		launch + "/", filepath.Join(launch, "notes.txt"),
		repo + "/", filepath.Join(repo, "main.go"),
	} {
		rec := do(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			b, _ := io.ReadAll(rec.Body)
			t.Errorf("GET %s = %d %q; want 404", p, rec.Code, b)
		}
	}
	if len(logged) < 3 {
		t.Fatalf("logged %d refusals; want one per refused root (3)", len(logged))
	}
}

// TestOutputsNonTourFileServesFileOnly: a deliverable outside a tN round
// opens its file alone, never its folder: a registered mock/slug/loose.html
// must not expose mock/slug/mockup/t9/ (unregistered) nor its neighbours. A
// non-round folder registered as itself opens its own index.html only.
func TestOutputsNonTourFileServesFileOnly(t *testing.T) {
	f := newOutputsFixture(t)
	mustWrite(t, f.p("mock/slug/loose.html"), "LOOSE")
	mustWrite(t, f.p("mock/slug/other.html"), "OTHER")
	mustWrite(t, f.p("mock/slug/mockup/t9/index.html"), "T9")
	mustWrite(t, f.p("site/index.html"), "SITE")
	mustWrite(t, f.p("site/secret.txt"), "SECRET")
	mustWrite(t, f.p("site/sub/index.html"), "SUB")
	mustWrite(t, f.p("rounds/t4/index.html"), "T4")
	mustWrite(t, f.p("rounds/t4/img.png"), "IMG")
	h := newTestHandler(staticSource(
		ServedEntry{Kind: "html", Path: f.p("mock/slug/loose.html")},
		ServedEntry{Kind: "report", Path: f.p("site")},
		ServedEntry{Kind: "mockup", Path: f.p("rounds/t4")},
	))
	for p, want := range map[string]int{
		f.p("mock/slug/loose.html"):           http.StatusOK,
		f.p("mock/slug/other.html"):           http.StatusNotFound,
		f.p("mock/slug/mockup/t9/") + "/":     http.StatusNotFound,
		f.p("mock/slug/mockup/t9/index.html"): http.StatusNotFound,
		f.p("mock/slug") + "/":                http.StatusNotFound,
		f.p("site") + "/":                     http.StatusOK,
		f.p("site/secret.txt"):                http.StatusNotFound,
		f.p("site/sub") + "/":                 http.StatusNotFound,
		f.p("rounds/t4") + "/":                http.StatusOK,
		f.p("rounds/t4/img.png"):              http.StatusOK,
	} {
		if rec := do(t, h, http.MethodGet, p); rec.Code != want {
			t.Errorf("GET %s = %d; want %d", p, rec.Code, want)
		}
	}
}
