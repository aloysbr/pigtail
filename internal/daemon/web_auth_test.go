package daemon

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/webui"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedWebSubjects puts one theme and one subject in every declared scope;
// it returns the subject ids, the master's first.
func seedWebSubjects(t *testing.T, srv *Server) []string {
	t.Helper()
	names := scope.Active().Names()
	master := scope.Master()
	ordered := []scope.Scope{master}
	for _, n := range names {
		if n != master {
			ordered = append(ordered, n)
		}
	}
	if len(ordered) < 2 {
		t.Fatalf("the scope table declares %d scopes: the test needs a non-master one", len(ordered))
	}
	var ids []string
	for i, sc := range ordered {
		th := &pigtail.Theme{ID: "thm_ck" + string(rune('a'+i)), Name: "ck theme " + string(sc), Kind: "area", Scope: string(sc)}
		if err := srv.store.CreateTheme(th); err != nil {
			t.Fatal(err)
		}
		id := "sub_ck" + string(rune('a'+i))
		if err := srv.store.CreateSubject(&pigtail.Subject{ID: id, ThemeID: th.ID, Title: "ck subject " + string(sc),
			Slug: "ck-" + string(rune('a'+i)), Status: pigtail.StatusInbox}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// cookieReq is a browser request over TCP carrying the session cookie.
func cookieReq(srv *Server, method, target string) *http.Request {
	return withCookie(tcpReq(method, target, testWebAddr), srv.webSession.Value())
}

// assertCookieSubjects200 is the positive control every refusal test runs:
// the same cookie reads GET /subjects.
func assertCookieSubjects200(t *testing.T, srv *Server, h http.Handler) {
	t.Helper()
	if rec := serveWeb(h, cookieReq(srv, http.MethodGet, "/subjects")); rec.Code != http.StatusOK {
		t.Fatalf("positive control: GET /subjects with the cookie = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPIWithoutCookieIs401(t *testing.T) {
	_, h := newWebServer(t)
	if rec := serveWeb(h, tcpReq(http.MethodGet, "/subjects", testWebAddr)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /subjects with neither cookie nor Bearer = %d, want 401", rec.Code)
	}
	req := withCookie(tcpReq(http.MethodGet, "/subjects", testWebAddr), "wrong-secret")
	if rec := serveWeb(h, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /subjects with a wrong cookie = %d, want 401", rec.Code)
	}
}

func TestCookieGetSubjects200(t *testing.T) {
	srv, h := newWebServer(t)
	req := cookieReq(srv, http.MethodGet, "/subjects")
	req.Header.Set("Origin", "http://"+testWebAddr)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if rec := serveWeb(h, req); rec.Code != http.StatusOK {
		t.Fatalf("GET /subjects, cookie + same origin = %d %s", rec.Code, rec.Body.String())
	}
	assertCookieSubjects200(t, srv, h)
}

func TestCookieWithForeignOriginIs403(t *testing.T) {
	srv, h := newWebServer(t)
	req := cookieReq(srv, http.MethodGet, "/subjects")
	req.Header.Set("Origin", "http://evil.example")
	if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin = %d, want 403", rec.Code)
	}
	req = cookieReq(srv, http.MethodGet, "/subjects")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("Sec-Fetch-Site cross-site = %d, want 403", rec.Code)
	}
	assertCookieSubjects200(t, srv, h)
}

func TestCookieSameSiteOtherPortIs403(t *testing.T) {
	srv, h := newWebServer(t)
	req := cookieReq(srv, http.MethodGet, "/subjects")
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("another local port's page = %d, want 403", rec.Code)
	}
	assertCookieSubjects200(t, srv, h)
}

func TestCookieDNSRebinding403(t *testing.T) {
	srv, h := newWebServer(t)
	evil := "evil.example:7800"
	req := withCookie(tcpReq(http.MethodGet, "/subjects", evil), srv.webSession.Value())
	req.Header.Set("Origin", "http://"+evil)
	if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
		t.Fatalf("rebound Host = Origin = %s: %d, want 403", evil, rec.Code)
	}
	req = withCookie(tcpReq(http.MethodGet, "/subjects", "localhost:7800"), srv.webSession.Value())
	req.Header.Set("Origin", "http://localhost:7800")
	if rec := serveWeb(h, req); rec.Code != http.StatusOK {
		t.Fatalf("localhost:7800 = %d, want 200", rec.Code)
	}
}

// cookieWrite is a cookie request with a body and, when origin is set, an
// Origin header.
func cookieWrite(srv *Server, method, target, body, origin string) *http.Request {
	req := cookieReq(srv, method, target)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

// otherScope is a declared scope that is not the master.
func otherScope(t *testing.T) scope.Scope {
	t.Helper()
	for _, n := range scope.Active().Names() {
		if n != scope.Master() {
			return n
		}
	}
	t.Fatal("the scope table declares no non-master scope: the test would prove nothing")
	return ""
}

func TestCookieWriteAllowlist(t *testing.T) {
	srv, h := newWebServer(t)
	ids := seedWebSubjects(t, srv)
	own := "http://" + testWebAddr
	allowed := []struct{ method, path, body string }{
		{http.MethodPost, "/subjects/ranks", `{"changes":[{"id":"` + ids[0] + `","from":0,"to":1}]}`},
		{http.MethodPost, "/subjects/" + ids[0], `{"status":"parked"}`},
		{http.MethodPost, "/subjects/" + ids[0] + "/launch", `{}`},
		{http.MethodPost, "/subjects/" + ids[0] + "/sdk-sessions", `{}`},
		{http.MethodPost, "/sdk-sessions/sdk_x/requests/r1/answer", `{"behavior":"allow"}`},
		{http.MethodPost, "/sdk-sessions/sdk_x/messages", `{"text":"hi"}`},
		{http.MethodPost, "/sdk-sessions/sdk_x/end", ``},
		{http.MethodPost, "/captures", `{"theme_id":"thm_cka","text":"ck idea"}`},
		{http.MethodDelete, "/captures/cap_unknown", ``},
		{http.MethodPost, "/waits/wait_unknown/answer", `{"choice":"A"}`},
		// an unknown session: the handler's 404, cmux never called
		{http.MethodPost, "/peers/s_unknown/reveal", ``},
	}
	// (b) Origin is required on a write, and must be the daemon's own.
	for _, c := range allowed {
		for _, v := range []struct{ what, origin, fetchSite string }{
			{"no Origin", "", ""},
			{"foreign Origin", "http://evil.example", ""},
			{"same-site page", own, "same-site"},
		} {
			req := cookieWrite(srv, c.method, c.path, c.body, v.origin)
			if v.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", v.fetchSite)
			}
			if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s, %s = %d, want 403: %s", c.method, c.path, v.what, rec.Code, rec.Body.String())
			}
		}
	}
	if caps, _ := srv.store.ListCaptures(store.CaptureFilter{Scope: scope.Any}); len(caps) != 0 {
		t.Fatalf("a refused write created %d captures", len(caps))
	}
	// (a) With the daemon's own Origin, each allowed write reaches its handler.
	for _, c := range allowed {
		req := cookieWrite(srv, c.method, c.path, c.body, own)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := serveWeb(h, req)
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("%s %s with the cookie and its Origin = %d, want the handler's answer: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	// (c) Off the list: 401, the daemon's own Origin notwithstanding.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/themes", `{"name":"ck created","kind":"area","vault_link":"X/ck"}`},
		{http.MethodPost, "/subjects/" + ids[0] + "/log", `{"kind":"note","text":"ck written"}`},
		{http.MethodPost, "/subjects/suggest", `{}`},
		{http.MethodPost, "/subjects/find", `{"query":"x"}`},
		{http.MethodPost, "/subject-finds/sf_0123456789abcdef/pick", `{"subject_id":"x"}`},
		{http.MethodPost, "/subjects/next", `{}`},
		{http.MethodPost, "/captures/promote", `{}`},
		{http.MethodDelete, "/subjects/" + ids[0], ``},
		{http.MethodDelete, "/themes/thm_cka", ``},
		{http.MethodPut, "/peers/s1/standby", `{}`},
		{http.MethodPost, "/web/login-code", ``},
		{http.MethodPost, "/subjects/" + ids[0] + "%2Flaunch", `{}`},
		{http.MethodPost, "/sdk-sessions", `{}`},
		{http.MethodPost, "/sdk-sessions/sdk_x", `{}`},
		{http.MethodPost, "/sdk-sessions/sdk_x/requests/r1", `{}`},
		{http.MethodPost, "/sdk-sessions/sdk_x/requests//answer", `{}`},
		{http.MethodPost, "/sdk-sessions//end", ``},
		{http.MethodDelete, "/sdk-sessions/sdk_x", ``},
		{http.MethodPost, "/subjects", `{}`},
		{http.MethodPost, "/subjects/" + ids[0] + "/phases", `{}`},
		{http.MethodPatch, "/subjects/" + ids[0], `{}`},
		{http.MethodPost, "/waits/x/snooze", `{}`},
		{http.MethodPost, "/waits/x/answer/more", `{}`},
		{http.MethodPost, "/waits//answer", `{}`},
		{http.MethodPost, "/waits/x%2Fy/answer", `{}`},
		{http.MethodPost, "/peers/x/reveal/more", ``},
		{http.MethodPost, "/peers//reveal", ``},
		{http.MethodPost, "/peers/x%2Fy/reveal", ``},
		{http.MethodPost, "/peers/x/wait", `{}`},
		{http.MethodDelete, "/peers/x/reveal", ``},
	} {
		rec := serveWeb(h, cookieWrite(srv, c.method, c.path, c.body, own))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with the cookie = %d, want 401: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if ths, _ := srv.store.ListThemes(scope.Any); len(ths) != len(ids) {
		t.Errorf("a refused write left %d themes, want %d", len(ths), len(ids))
	}
	// (d) The reads the actions need.
	if rec := serveWeb(h, cookieReq(srv, http.MethodGet, "/captures")); rec.Code != http.StatusOK {
		t.Errorf("GET /captures with the cookie = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := serveWeb(h, cookieReq(srv, http.MethodGet, "/launches/x")); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Errorf("GET /launches/x with the cookie = %d, want the handler's answer", rec.Code)
	}
	for _, p := range []string{"/sdk-sessions", "/sdk-sessions/sdk_x", "/sdk-sessions/sdk_x/events"} {
		if rec := serveWeb(h, cookieReq(srv, http.MethodGet, p)); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("GET %s with the cookie = %d, want the handler's answer", p, rec.Code)
		}
	}
	for _, p := range []string{"/events", "/launches/x/y", "/launches/", "/sdk-sessions/", "/sdk-sessions/sdk_x/y", "/sdk-sessions/sdk_x/events/z", "/sdk-sessions/sdk_x/requests/r1/answer"} {
		if rec := serveWeb(h, cookieReq(srv, http.MethodGet, p)); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with the cookie = %d, want 401", p, rec.Code)
		}
	}
	assertCookieSubjects200(t, srv, h)
}

// The browser never picks its scope nor its cross-scope opt-in: the server
// sets the master and, on an allowed write, the opt-in.
func TestCookieWriteScopeIsServerSet(t *testing.T) {
	srv, h := newWebServer(t)
	seedWebSubjects(t, srv) // thm_cka: the master's theme, thm_ckb: another scope's
	other := otherScope(t)
	for _, th := range []string{"thm_cka", "thm_ckb"} {
		req := cookieWrite(srv, http.MethodPost, "/captures", `{"theme_id":"`+th+`","text":"ck idea"}`, "http://"+testWebAddr)
		req.Header.Set(pigtail.HeaderScope, string(other))
		req.Header.Set(pigtail.HeaderCrossScope, "no")
		if rec := serveWeb(h, req); rec.Code != http.StatusCreated {
			t.Errorf("cookie POST /captures on %s naming %s = %d, want 201: %s", th, other, rec.Code, rec.Body.String())
		}
	}
	// The same write by a master Bearer without the opt-in is refused: the
	// 201 above is the server's opt-in, not a lax rule.
	req := tcpReq(http.MethodPost, "/captures", testWebAddr)
	req.Body = io.NopCloser(strings.NewReader(`{"theme_id":"thm_ckb","text":"ck idea"}`))
	req.Header.Set("Authorization", "Bearer "+srv.token)
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	rec := serveWeb(h, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), string(pigtail.ErrScopeMismatch)) {
		t.Fatalf("master Bearer without opt-in on %s = %d %s, want 403 %s", other, rec.Code, rec.Body.String(), pigtail.ErrScopeMismatch)
	}
}

// A cookie write carrying a session header is still frozen: the server drops
// the header, so the browser cannot pass for the maintenance's exempt session.
func TestWebWriteIgnoresSessionHeader(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", dir)
	if err := maintenance.Save(dir, maintenance.State{ID: "mnt_ck", On: true, Freeze: true, Reason: "ck",
		Exempt: "sess_exempt", Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: testWebAddr, LaunchOverrides: noCmuxLaunch}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.buildRouter()
	seedWebSubjects(t, srv)
	body := `{"theme_id":"thm_cka","text":"ck idea"}`
	req := cookieWrite(srv, http.MethodPost, "/captures", body, "http://"+testWebAddr)
	req.Header.Set(pigtail.HeaderSession, "sess_exempt")
	if rec := serveWeb(h, req); rec.Code != http.StatusLocked {
		t.Fatalf("cookie write naming the exempt session under a freeze = %d %s, want 423", rec.Code, rec.Body.String())
	}
	// Positive control: the exempt session itself, by Bearer, writes.
	req = tcpReq(http.MethodPost, "/captures", testWebAddr)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+srv.token)
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	req.Header.Set(pigtail.HeaderSession, "sess_exempt")
	if rec := serveWeb(h, req); rec.Code != http.StatusCreated {
		t.Fatalf("exempt session by Bearer under the freeze = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

func TestCookieAllowlist(t *testing.T) {
	srv, h := newWebServer(t)
	id := seedWebSubjects(t, srv)[0]
	for _, p := range []string{"/subjects", "/subjects?scope=all", "/subjects/" + id, "/subjects/" + id + "/log", "/subjects/" + id + "/outputs",
		"/themes", "/spaces", "/peers", "/health", "/version",
		"/waits", "/waits?subject=" + id + "&closed=2", "/waits?session=s1", "/subjects/" + id + "/outputs/groups"} {
		if rec := serveWeb(h, cookieReq(srv, http.MethodGet, p)); rec.Code != http.StatusOK {
			t.Errorf("GET %s with the cookie = %d, want 200: %s", p, rec.Code, rec.Body.String())
		}
	}
	for _, p := range []string{"/subjects/next", "/subjects/" + id + "/worktree", "/subjects/" + id + "/phases",
		"/subjects/" + id + "/cmux-workspaces", "/backup", "/events", "/workspaces", "/subjects/", "/subjects/" + id + "/log/",
		"/journal", "/messages", "/subjects/" + id + "%2Fworktree",
		"/waits/wait_x", "/waits/", "/subjects/" + id + "/outputs/groups/x", "/subjects/" + id + "/outputs/purge"} {
		if rec := serveWeb(h, cookieReq(srv, http.MethodGet, p)); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with the cookie = %d, want 401", p, rec.Code)
		}
	}
	// The same routes answer a valid Bearer: the 401 above is the allowlist.
	req := tcpReq(http.MethodGet, "/workspaces", testWebAddr)
	req.Header.Set("Authorization", "Bearer "+srv.token)
	if rec := serveWeb(h, req); rec.Code != http.StatusOK {
		t.Fatalf("GET /workspaces with the Bearer = %d", rec.Code)
	}
}

func TestTokenNeverInServedHTML(t *testing.T) {
	srv, h := newWebServer(t)
	if srv.token == "" {
		t.Fatal("no API token loaded: the test would prove nothing")
	}
	check := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(rec.Body.String(), srv.token) {
			t.Errorf("%s: the body carries the API token", what)
		}
		for _, hd := range []string{"Location", "Set-Cookie"} {
			for _, v := range rec.Header().Values(hd) {
				if strings.Contains(v, srv.token) {
					t.Errorf("%s: %s carries the API token", what, hd)
				}
			}
		}
	}
	check("expired page", serveWeb(h, tcpReq(http.MethodGet, "/", testWebAddr)))
	code := issueCode(t, h).Code
	check("login", serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr)))
	check("used login", serveWeb(h, tcpReq(http.MethodGet, "/web/login?code="+code, testWebAddr)))
	check("index (fake front)", serveWeb(h, cookieReq(srv, http.MethodGet, "/")))
	srv2, h2 := newWebServer(t)
	srv2.webFS = webui.FS() // the real embedded front
	rec := serveWeb(h2, withCookie(tcpReq(http.MethodGet, "/", testWebAddr), srv2.webSession.Value()))
	if rec.Code != http.StatusOK {
		t.Fatalf("embedded index = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), srv2.token) {
		t.Error("the embedded index carries the API token")
	}
}

func TestCookieForcesMasterScope(t *testing.T) {
	srv, h := newWebServer(t)
	ids := seedWebSubjects(t, srv)
	other := ""
	for _, n := range scope.Active().Names() {
		if n != scope.Master() {
			other = string(n)
			break
		}
	}
	req := cookieReq(srv, http.MethodGet, "/subjects?scope=all")
	req.Header.Set(pigtail.HeaderScope, other)
	req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
	rec := serveWeb(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /subjects?scope=all naming %s = %d %s", other, rec.Code, rec.Body.String())
	}
	var env struct {
		Data []pigtail.Subject `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v %s", err, rec.Body.String())
	}
	got := map[string]bool{}
	for _, s := range env.Data {
		got[s.ID] = true
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("subject %s missing: the cookie must read as the master (%s), whatever the browser names", id, scope.Master())
		}
	}
}

func TestBearerAndSocketUnchanged(t *testing.T) {
	srv, h := newWebServer(t)
	for _, cookie := range []string{"", srv.webSession.Value(), "invalid-cookie"} {
		req := tcpReq(http.MethodGet, "/workspaces", testWebAddr)
		req.Header.Set("Authorization", "Bearer "+srv.token)
		if cookie != "" {
			withCookie(req, cookie)
		}
		if rec := serveWeb(h, req); rec.Code != http.StatusOK {
			t.Errorf("Bearer, cookie %q: GET /workspaces = %d", cookie, rec.Code)
		}
		req = httptest.NewRequest(http.MethodGet, "/workspaces", nil)
		req.RemoteAddr = ""
		if cookie != "" {
			withCookie(req, cookie)
		}
		if rec := serveWeb(h, req); rec.Code != http.StatusOK {
			t.Errorf("socket, cookie %q: GET /workspaces = %d", cookie, rec.Code)
		}
	}
	req := tcpReq(http.MethodGet, "/workspaces", testWebAddr)
	req.Header.Set("Authorization", "Bearer wrong")
	if rec := serveWeb(h, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong Bearer = %d, want 401", rec.Code)
	}
}

func TestUsedLoginCodeShowsExpired(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: ln.Addr().String()}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.buildRouter()
	ts := httptest.NewUnstartedServer(h)
	_ = ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	defer ts.Close()

	code := issueCode(t, h).Code
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, cookies ...*http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get("/web/login?code=" + code)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
		t.Fatalf("first login = %d, cookies %v", resp.StatusCode, resp.Cookies())
	}
	cookie := resp.Cookies()[0]
	resp = get("/web/login?code=" + code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), "<title>Lien expiré</title>") || len(resp.Cookies()) != 0 {
		t.Fatalf("replayed login = %d, cookies %v:\n%s", resp.StatusCode, resp.Cookies(), body)
	}
	resp = get("/subjects", cookie)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the first login's cookie must read GET /subjects over TCP: %d", resp.StatusCode)
	}
}

// End to end: the cookie cannot close a subject, even by a forged request.
func TestCookieCannotWriteDone(t *testing.T) {
	srv, h := newWebServer(t)
	id := seedWebSubjects(t, srv)[0]
	rec := serveWeb(h, cookieWrite(srv, http.MethodPost, "/subjects/"+id, `{"status":"done"}`, "http://"+testWebAddr))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), string(pigtail.ErrForbiddenForWeb)) {
		t.Fatalf("cookie done = %d %s, want 403 %s", rec.Code, rec.Body.String(), pigtail.ErrForbiddenForWeb)
	}
	if sub, _ := srv.store.GetSubject(id); sub.Status != pigtail.StatusInbox {
		t.Fatalf("subject became %s", sub.Status)
	}
	if rec := serveWeb(h, cookieWrite(srv, http.MethodPost, "/subjects/"+id, `{"status":"parked"}`, "http://"+testWebAddr)); rec.Code != http.StatusOK {
		t.Fatalf("positive control: cookie parked = %d %s", rec.Code, rec.Body.String())
	}
}

// socketJSON calls the router as the unix socket (no token), as the master.
func socketJSON(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, target, strings.NewReader(string(b)))
	req.RemoteAddr = ""
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	return serveWeb(h, req)
}

// seedWebWait registers a master-scope session and declares a wait of
// typ on it; it returns the wait id.
func seedWebWait(t *testing.T, srv *Server, h http.Handler, session string, req pigtail.SessionWaitRequest) string {
	t.Helper()
	if rec := socketJSON(t, h, http.MethodPost, "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: session, Name: "ck-" + session, Scope: string(scope.Master())},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /peers = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socketJSON(t, h, http.MethodPost, "/peers/"+session+"/wait", req); rec.Code != http.StatusOK {
		t.Fatalf("POST wait = %d %s", rec.Code, rec.Body.String())
	}
	recs, err := srv.store.ListOpenWaitRecords(store.WaitRecordFilter{Scope: scope.Any, SessionID: session, IncludeSnoozed: true})
	if err != nil || len(recs) == 0 {
		t.Fatalf("no open wait for %s: %v", session, err)
	}
	return recs[len(recs)-1].ID
}

func TestCookieAnswerDelivers(t *testing.T) {
	srv, h := newWebServer(t)
	id := seedWebWait(t, srv, h, "s-ck", pigtail.SessionWaitRequest{Type: "question", Reason: "ck question"})
	req := cookieWrite(srv, http.MethodPost, "/waits/"+id+"/answer", `{"text":"ck answer"}`, "http://"+testWebAddr)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set(pigtail.HeaderSession, "s-agent") // dropped by the cookie path: the answer is Ada'
	rec := serveWeb(h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie answer = %d %s", rec.Code, rec.Body.String())
	}
	got, err := srv.store.GetWaitRecord(id)
	if err != nil || got.State != pigtail.WaitStateAnswered || got.Answer == nil {
		t.Fatalf("wait after the cookie answer = %+v, %v", got, err)
	}
	// The cookie reads it back in the history.
	rec = serveWeb(h, cookieReq(srv, http.MethodGet, "/waits?session=s-ck&closed=5"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("GET /waits history with the cookie = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCookieAnswerPermissionStillRefused(t *testing.T) {
	srv, h := newWebServer(t)
	id := seedWebWait(t, srv, h, "s-perm", pigtail.SessionWaitRequest{Type: "permission", Reason: "Bash"})
	req := cookieWrite(srv, http.MethodPost, "/waits/"+id+"/answer", `{"choice":"A"}`, "http://"+testWebAddr)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := serveWeb(h, req)
	if rec.Code < 400 || rec.Code == http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "answer it in the terminal") {
		t.Fatalf("cookie answer to a permission = %d %s; want the terminal refusal", rec.Code, rec.Body.String())
	}
	if got, _ := srv.store.GetWaitRecord(id); got == nil || got.State != pigtail.WaitStateOpen {
		t.Fatalf("permission wait = %+v; want still open", got)
	}
}

func TestCookieAnswerFromOutputsOriginIs403(t *testing.T) {
	srv, h := newWebServer(t)
	id := seedWebWait(t, srv, h, "s-out", pigtail.SessionWaitRequest{Type: "question", Reason: "ck"})
	for _, origin := range []string{"http://127.0.0.1:7801", "http://localhost:7801"} {
		req := cookieWrite(srv, http.MethodPost, "/waits/"+id+"/answer", `{"text":"from a served page"}`, origin)
		req.Header.Set("Sec-Fetch-Site", "same-site")
		if rec := serveWeb(h, req); rec.Code != http.StatusForbidden {
			t.Fatalf("answer from %s = %d %s; want 403", origin, rec.Code, rec.Body.String())
		}
	}
	if got, _ := srv.store.GetWaitRecord(id); got == nil || got.State != pigtail.WaitStateOpen {
		t.Fatalf("wait = %+v; want still open", got)
	}
}
