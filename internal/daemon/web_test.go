package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/websession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const testWebAddr = "127.0.0.1:7800"

// newWebServer builds a daemon with HTTP on (token and session secret
// loaded, nothing listening) and a front with one hashed asset.
func newWebServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: testWebAddr, LaunchOverrides: noCmuxLaunch}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.webFS = fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>webfront index</title><script src=\"/assets/app-abc.js\"></script>")},
		"assets/app-abc.js":    {Data: []byte("console.log('webfront')")},
		"assets/sub/nested.js": {Data: []byte("nested")},
	}
	return srv, srv.buildRouter()
}

// noCmuxLaunch keeps the launcher off the real cmux: a launch reaching it is
// refused CMUX_UNREACHABLE before anything is created.
func noCmuxLaunch(d *launch.Deps) {
	d.Windows = func() ([]cmux.Window, error) { return nil, errors.New("no cmux in the webfront tests") }
	d.NewWorkspace = func(cmux.WorkspaceSpec) (string, error) { return "", errors.New("no cmux in the webfront tests") }
}

// tcpReq is a request arriving on the HTTP loopback (not the socket).
func tcpReq(method, target, host string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:50123"
	req.Host = host
	return req
}

func serveWeb(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// issueCode asks the login-code route on the socket, as `pigtail web` does.
func issueCode(t *testing.T, h http.Handler) pigtail.WebLoginCode {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/web/login-code", nil)
	req.RemoteAddr = ""
	rec := serveWeb(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login-code on the socket = %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data pigtail.WebLoginCode `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.Code == "" {
		t.Fatalf("decode login-code %s: %v", rec.Body.String(), err)
	}
	return env.Data
}

func withCookie(req *http.Request, v string) *http.Request {
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: v})
	return req
}

func assertExpiredPage(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := rec.Body.String()
	for _, want := range []string{"<title>Lien expiré</title>", "Relance", "pigtail web", "dans un terminal pour te reconnecter."} {
		if !strings.Contains(body, want) {
			t.Errorf("expired page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Errorf("expired page carries a script:\n%s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestLoginCodeSocketOnly(t *testing.T) {
	srv, h := newWebServer(t)
	req := tcpReq(http.MethodPost, "/web/login-code", testWebAddr)
	req.Header.Set("Authorization", "Bearer "+srv.token)
	if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("login-code over HTTP with the Bearer = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	got := issueCode(t, h) // positive control: the socket gets one
	if got.HTTPAddr != testWebAddr {
		t.Fatalf("http_addr = %q, want %q", got.HTTPAddr, testWebAddr)
	}
}

func TestLoginCodeNoHTTPAddr409(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/web/login-code", nil)
	req.RemoteAddr = ""
	rec := serveWeb(srv.buildRouter(), req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("login-code with HTTP off = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "HTTP") {
		t.Fatalf("the 409 must say HTTP is off: %s", rec.Body.String())
	}
}

func TestLoginSetsCookieAndRedirects(t *testing.T) {
	srv, h := newWebServer(t)
	code := issueCode(t, h).Code
	rec := serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("login = %d Location %q, want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
	sc := rec.Header().Values("Set-Cookie")
	if len(sc) != 1 {
		t.Fatalf("Set-Cookie = %q, want exactly one", sc)
	}
	want := webCookieName + "=" + srv.webSession.Value() + "; Path=/; Max-Age=31536000; HttpOnly; SameSite=Strict"
	if sc[0] != want {
		t.Fatalf("Set-Cookie = %q\nwant        %q", sc[0], want)
	}
	if strings.Contains(strings.ToLower(sc[0]), "domain") {
		t.Fatalf("the cookie must be host-only (no Domain): %q", sc[0])
	}
}

func TestUsedCodeShowsExpiredPage(t *testing.T) {
	_, h := newWebServer(t)
	code := issueCode(t, h).Code
	if rec := serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr)); rec.Code != http.StatusSeeOther {
		t.Fatalf("first use = %d, want 303", rec.Code)
	}
	rec := serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr))
	if rec.Code != http.StatusGone {
		t.Fatalf("second use = %d, want 410", rec.Code)
	}
	if sc := rec.Header().Get("Set-Cookie"); sc != "" {
		t.Fatalf("a used code set a cookie: %q", sc)
	}
	assertExpiredPage(t, rec)
}

type webClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *webClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func TestExpiredCodeShowsExpiredPage(t *testing.T) {
	srv, h := newWebServer(t)
	clk := &webClock{t: time.Unix(1_700_000_000, 0)}
	srv.webCodes = websession.NewCodes(websession.CodeTTL, clk.now)
	code := issueCode(t, h).Code
	clk.mu.Lock()
	clk.t = clk.t.Add(websession.CodeTTL + time.Second)
	clk.mu.Unlock()
	rec := serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr))
	if rec.Code != http.StatusGone {
		t.Fatalf("expired code = %d, want 410", rec.Code)
	}
	if sc := rec.Header().Get("Set-Cookie"); sc != "" {
		t.Fatalf("an expired code set a cookie: %q", sc)
	}
	assertExpiredPage(t, rec)
}

func TestRootWithoutCookieShowsExpired(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	srv, h := newWebServer(t)
	// A subject in the store: its title must not reach the page.
	th := &pigtail.Theme{ID: "thm_webfront", Name: "Zqx webfront theme", Kind: "area"}
	if err := srv.store.CreateTheme(th); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateSubject(&pigtail.Subject{ID: "sub_webfrontzqx", ThemeID: th.ID, Title: "Zqx webfront secret", Slug: "zqx-webfront", Status: pigtail.StatusInbox}); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{"", "not-the-secret"} {
		req := tcpReq(http.MethodGet, "/", testWebAddr)
		if cookie != "" {
			withCookie(req, cookie)
		}
		rec := serveWeb(h, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / cookie %q = %d, want 200", cookie, rec.Code)
		}
		assertExpiredPage(t, rec)
		for _, m := range []string{"sub_webfrontzqx", "Zqx webfront secret", "zqx-webfront", "Zqx webfront theme"} {
			if strings.Contains(rec.Body.String(), m) {
				t.Errorf("expired page leaks %q", m)
			}
		}
	}
}

func TestRootWithCookieServesIndex(t *testing.T) {
	srv, h := newWebServer(t)
	rec := serveWeb(h, withCookie(tcpReq(http.MethodGet, "/", testWebAddr), srv.webSession.Value()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "webfront index") {
		t.Fatalf("GET / with the cookie = %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestAssetsNeedCookie(t *testing.T) {
	srv, h := newWebServer(t)
	if rec := serveWeb(h, tcpReq(http.MethodGet, "/assets/app-abc.js", testWebAddr)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("asset without cookie = %d, want 401", rec.Code)
	}
	rec := serveWeb(h, withCookie(tcpReq(http.MethodGet, "/assets/app-abc.js", testWebAddr), srv.webSession.Value()))
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('webfront')" {
		t.Fatalf("asset with cookie = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	// No directory listing, no escape out of assets/.
	for _, p := range []string{"/assets/", "/assets/sub", "/assets/../index.html", "/assets/missing.js"} {
		if rec := serveWeb(h, withCookie(tcpReq(http.MethodGet, p, testWebAddr), srv.webSession.Value())); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with cookie = %d, want 404: %s", p, rec.Code, rec.Body.String())
		}
	}
}

func TestHostRebinding403(t *testing.T) {
	srv, h := newWebServer(t)
	code := issueCode(t, h).Code
	evil := "evil.example:7800"
	for _, p := range []string{"/", "/web/login?code=" + code, "/assets/app-abc.js"} {
		rec := serveWeb(h, withCookie(tcpReq(http.MethodGet, p, evil), srv.webSession.Value()))
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with Host %s = %d, want 403", p, evil, rec.Code)
		}
		if sc := rec.Header().Get("Set-Cookie"); sc != "" {
			t.Errorf("GET %s with a foreign Host set a cookie: %q", p, sc)
		}
		if strings.Contains(rec.Body.String(), "webfront index") || strings.Contains(rec.Body.String(), "console.log") {
			t.Errorf("GET %s with a foreign Host served content: %s", p, rec.Body.String())
		}
	}
	// The code was not consumed by the refused attempt.
	if rec := serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr)); rec.Code != http.StatusSeeOther {
		t.Fatalf("the code must survive a refused Host: login = %d", rec.Code)
	}
}

func TestHostLocalhostAllowed(t *testing.T) {
	srv, h := newWebServer(t)
	for _, host := range []string{"localhost:7800", testWebAddr} {
		rec := serveWeb(h, withCookie(tcpReq(http.MethodGet, "/", host), srv.webSession.Value()))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "webfront index") {
			t.Errorf("GET / with Host %s = %d", host, rec.Code)
		}
	}
	if rec := serveWeb(h, withCookie(tcpReq(http.MethodGet, "/", "localhost:7801"), srv.webSession.Value())); rec.Code != http.StatusForbidden {
		t.Errorf("another port's localhost = %d, want 403", rec.Code)
	}
}

// The expired page's inline command and its block render in the t10 mono
// face at body size, on the t10 line height (capture review, lot 1).
func TestExpiredPageUsesT10Type(t *testing.T) {
	for _, want := range []string{
		"code{font-family:var(--font-mono);font-size:1em}",
		"font:13px/1.45 var(--font-sans)",
		"font:13px/1.45 var(--font-mono)",
	} {
		if !strings.Contains(webExpiredHTML, want) {
			t.Errorf("expired page CSS lacks %q", want)
		}
	}
}

func TestOldCockpitCookieShowsExpired(t *testing.T) {
	srv, h := newWebServer(t)
	req := tcpReq(http.MethodGet, "/", testWebAddr)
	req.AddCookie(&http.Cookie{Name: "pigtail_cockpit", Value: srv.webSession.Value()})
	rec := serveWeb(h, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Lien expiré") || !strings.Contains(body, "pigtail web") {
		t.Fatalf("status %d, body %q: want the expired page naming pigtail web", rec.Code, body)
	}
	if strings.Contains(strings.ToLower(body), "cockpit") {
		t.Fatalf("expired page still says cockpit: %q", body)
	}
}

// The old login link is no public route any more: authMW answers its JSON
// 401, with no cookie.
func TestOldLoginRouteRefused(t *testing.T) {
	_, h := newWebServer(t)
	rec := serveWeb(h, tcpReq(http.MethodGet, "/cockpit/login?code=x", testWebAddr))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), string(pigtail.ErrUnauthorized)) {
		t.Fatalf("status %d, body %q: want the JSON 401", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) > 0 {
		t.Fatalf("old login route set a cookie: %v", rec.Result().Cookies())
	}
}
