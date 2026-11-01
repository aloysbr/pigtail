package daemon

import (
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/websession"
	"github.com/aloysbr/pigtail/internal/webui"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The web front (docs/wire-protocol.md § Web (browser)): `pigtail web` asks
// a single-use code on the socket, the browser trades it at
// /web/login for a host-only cookie carrying the session secret, then
// loads / and /assets/* — each checking the Host header against the
// daemon's own address (DNS rebinding) and the cookie.

// webCookieName is the session cookie set at login.
const webCookieName = "pigtail_web"

// webCookieMaxAge keeps the browser signed in for a year.
const webCookieMaxAge = 365 * 24 * time.Hour

// RegisterWebRoutes mounts the web front's routes. They are public paths
// for authMW (isPublicPath) except the login-code route: each handler
// decides on its own (socket, Host, cookie).
func (s *Server) RegisterWebRoutes(r chi.Router) {
	r.Post("/web/login-code", s.handleWebLoginCode)
	r.Get("/web/login", s.handleWebLogin)
	r.Get("/", s.handleWebRoot)
	r.Get("/assets/*", s.handleWebAsset)
}

// webAllowedHost returns the Host a browser may use to reach the
// web front: the configured HTTP address, or localhost on its port. ok is
// false when r's Host is neither (or HTTP is off).
func (s *Server) webAllowedHost(r *http.Request) (string, bool) {
	addr := s.cfg.HTTPAddr
	if addr == "" {
		return "", false
	}
	host := r.Host
	if strings.EqualFold(host, addr) {
		return addr, true
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		if lh := "localhost:" + port; strings.EqualFold(host, lh) {
			return lh, true
		}
	}
	return "", false
}

// webHostOK is the anti-rebinding check every cookie path runs.
func (s *Server) webHostOK(r *http.Request) bool {
	_, ok := s.webAllowedHost(r)
	return ok
}

// webCookieOK reports whether r carries the session cookie, compared in
// constant time.
func (s *Server) webCookieOK(r *http.Request) bool {
	c, err := r.Cookie(webCookieName)
	if err != nil {
		return false
	}
	return s.webSession.Check(c.Value)
}

func (s *Server) handleWebLoginCode(w http.ResponseWriter, r *http.Request) {
	if !isSocketConn(r) {
		writeError(w, r, http.StatusForbidden, pigtail.ErrUnauthorized,
			"a web front login code is issued on the Unix socket only", nil)
		return
	}
	if s.cfg.HTTPAddr == "" || s.webSession == nil {
		writeError(w, r, http.StatusConflict, pigtail.ErrConflict,
			"the daemon's HTTP listener is off (pigtaild --http \"\"): the web front needs it", nil)
		return
	}
	code, err := s.webCodes.Issue()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "issue a login code: "+err.Error(), nil)
		return
	}
	writeJSON(w, r, http.StatusOK, pigtail.WebLoginCode{Code: code, HTTPAddr: s.cfg.HTTPAddr})
}

func (s *Server) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	if !s.webHostOK(r) {
		webForbidden(w)
		return
	}
	if s.webSession == nil || !s.webCodes.Redeem(r.URL.Query().Get("code")) {
		writeWebExpired(w, http.StatusGone)
		return
	}
	// Secure stays off: plain HTTP on the loopback, a Secure cookie would
	// never be sent back (Secure if TLS one day).
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see above
		Name:     webCookieName,
		Value:    s.webSession.Value(),
		Path:     "/",
		MaxAge:   int(webCookieMaxAge / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// No Domain: host-only.
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleWebRoot(w http.ResponseWriter, r *http.Request) {
	if !s.webHostOK(r) {
		webForbidden(w)
		return
	}
	if !s.webCookieOK(r) {
		writeWebExpired(w, http.StatusOK)
		return
	}
	b, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		http.Error(w, "web front missing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) handleWebAsset(w http.ResponseWriter, r *http.Request) {
	if !s.webHostOK(r) {
		webForbidden(w)
		return
	}
	if !s.webCookieOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := chi.URLParam(r, "*")
	name := "assets/" + rest
	if rest == "" || path.Clean(name) != name || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	f, err := s.webFS.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, fi.ModTime(), rs)
}

func webForbidden(w http.ResponseWriter) {
	http.Error(w, "forbidden host", http.StatusForbidden)
}

// writeWebExpired renders the one « Lien expiré » page: no data, no
// script, inline CSS from the t10 tokens (light and dark).
func writeWebExpired(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, webExpiredHTML)
}

const webExpiredHTML = `<!doctype html>
<html lang="fr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Lien expiré</title>
<style>
:root{color-scheme:light dark;--bg:#f5f5f7;--card:#ffffff;--border:#e3e3e8;--text:#1d1d1f;--text-2:#6e6e73;--code-bg:#ebebef;
--font-sans:-apple-system,BlinkMacSystemFont,"SF Pro Text",Inter,system-ui,sans-serif;--font-mono:ui-monospace,"SF Mono",Menlo,monospace}
@media (prefers-color-scheme: dark){:root{--bg:#1c1c1e;--card:#2c2c2e;--border:#38383a;--text:#f5f5f7;--text-2:#a1a1a6;--code-bg:#3a3a3c}}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:0 16px;background:var(--bg);color:var(--text);font:13px/1.45 var(--font-sans);-webkit-font-smoothing:antialiased}
main{max-width:420px;background:var(--card);border:1px solid var(--border);border-radius:10px;padding:24px}
h1{margin:0 0 8px;font-size:17px;font-weight:600}
p{margin:0 0 16px;color:var(--text-2)}
pre{margin:0;padding:8px 12px;border-radius:6px;background:var(--code-bg);font:13px/1.45 var(--font-mono);color:var(--text)}
code{font-family:var(--font-mono);font-size:1em}
</style>
</head>
<body>
<main>
<h1>Lien expiré</h1>
<p>Relance <code>pigtail web</code> dans un terminal pour te reconnecter.</p>
<pre>pigtail web</pre>
</main>
</body>
</html>
`

// newWebState loads the session secret (only with HTTP on: no
// listener, no browser) and the code set.
func newWebState(cfg Config) (*websession.Session, *websession.Codes, fs.FS, error) {
	codes := websession.NewCodes(websession.CodeTTL, time.Now)
	if cfg.HTTPAddr == "" {
		return nil, codes, webui.FS(), nil
	}
	sess, err := websession.LoadSession(cfg.WebSessionPath)
	if err != nil {
		return nil, nil, nil, err
	}
	return sess, codes, webui.FS(), nil
}

// webAPIAllowed is the exact allowlist of API routes the cookie may
// read (GET): the backlog, its captures, a launch, the SDK sessions, the waits (GET /waits and
// its query, never GET /waits/{id}) and a subject's output groups, nothing
// else (no /subjects/next, worktree, phases, /backup, /events,
// /workspaces). Writes have their own list (webWriteAllowed). An escaped path (RawPath set) is
// refused outright: chi routes on it, the allowlist reads the decoded one,
// and no id needs escaping.
func webAPIAllowed(r *http.Request) bool {
	if r.URL.RawPath != "" {
		return false
	}
	switch p := r.URL.Path; p {
	case "/health", "/version", "/subjects", "/themes", "/spaces", "/peers", "/captures", "/waits":
		return true
	default:
		if id, ok := oneSegment(p, "/launches/"); ok && id != "" {
			return true
		}
		if sdkReadAllowed(p) {
			return true
		}
		rest, ok := strings.CutPrefix(p, "/subjects/")
		if !ok {
			return false
		}
		parts := strings.Split(rest, "/")
		if parts[0] == "" || parts[0] == "next" {
			return false
		}
		switch len(parts) {
		case 1:
			return true
		case 2:
			return parts[1] == "log" || parts[1] == "outputs"
		case 3:
			return parts[1] == "outputs" && parts[2] == "groups"
		}
		return false
	}
}

// webWriteAllowed is the exact allowlist of the cookie's writes (method
// and decoded path): a rank change, a subject's status (the handler limits
// the fields, handlers.IsWeb), a launch, a capture and its undo, and the
// answer to a wait (POST /waits/{id}/answer: webAsMaster drops the
// session header, the handler still refuses a permission or a dialog), and
// « Aller à la session » (POST /peers/{session}/reveal: its cmux tab to the
// front, the handler checks the session's scope). Nothing else writes; an
// escaped path is refused like on the read list.
func webWriteAllowed(r *http.Request) bool {
	if r.URL.RawPath != "" {
		return false
	}
	p := r.URL.Path
	switch r.Method {
	case http.MethodPost:
		if p == "/subjects/ranks" || p == "/captures" || sdkWriteAllowed(p) {
			return true
		}
		if rest, ok := strings.CutPrefix(p, "/waits/"); ok {
			id, ok := strings.CutSuffix(rest, "/answer")
			return ok && id != "" && !strings.Contains(id, "/")
		}
		if rest, ok := strings.CutPrefix(p, "/peers/"); ok {
			id, ok := strings.CutSuffix(rest, "/reveal")
			return ok && id != "" && !strings.Contains(id, "/")
		}
		rest, ok := strings.CutPrefix(p, "/subjects/")
		if !ok {
			return false
		}
		parts := strings.Split(rest, "/")
		switch {
		case parts[0] == "" || parts[0] == "next" || parts[0] == "suggest" || parts[0] == "find" || parts[0] == "ranks":
			return false
		case len(parts) == 1:
			return true
		case len(parts) == 2:
			return parts[1] == "launch" || parts[1] == "sdk-sessions"
		}
		return false
	case http.MethodDelete:
		id, ok := oneSegment(p, "/captures/")
		return ok && id != "" && id != "promote"
	}
	return false
}

// sdkReadAllowed: the SDK sessions' reads (GET /sdk-sessions, /{id},
// /{id}/events), the exact shapes and nothing under them.
func sdkReadAllowed(p string) bool {
	if p == "/sdk-sessions" {
		return true
	}
	rest, ok := strings.CutPrefix(p, "/sdk-sessions/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		return parts[0] != ""
	case 2:
		return parts[0] != "" && parts[1] == "events"
	}
	return false
}

// sdkWriteAllowed: the SDK sessions' writes — a reply, an end, the answer of
// a request (POST /sdk-sessions/{id}/messages|end|requests/{rid}/answer).
func sdkWriteAllowed(p string) bool {
	rest, ok := strings.CutPrefix(p, "/sdk-sessions/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		return false
	}
	switch len(parts) {
	case 2:
		return parts[1] == "messages" || parts[1] == "end"
	case 4:
		return parts[1] == "requests" && parts[2] != "" && parts[3] == "answer"
	}
	return false
}

// oneSegment returns the rest of p after prefix when it is one path segment.
func oneSegment(p, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(p, prefix)
	if !ok || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// webOriginOK runs the anti-rebinding and anti-CSRF checks of the
// cookie path: the Host is the daemon's own; an Origin is http:// + that
// allowed host (never the request's Host, which a rebinding page chooses) —
// required when requireOrigin (a write: SameSite=Strict plus a same-origin
// Origin), tolerated absent on a read; a Sec-Fetch-Site, when sent, is
// same-origin or none.
func (s *Server) webOriginOK(r *http.Request, requireOrigin bool) bool {
	host, ok := s.webAllowedHost(r)
	if !ok {
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" && requireOrigin {
		return false
	}
	if o != "" && !strings.EqualFold(o, "http://"+host) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// webAsMaster returns r reading as the master scope (D1), marked as the
// web front's (handlers.WithWeb): the server sets X-Pigtail-Scope, drops
// the client's cross-scope opt-in and session header (FreezeMW would exempt
// a named session), and sets the opt-in itself on an allowed write only —
// the browser never picks its scope.
func webAsMaster(r *http.Request, write bool) *http.Request {
	r = r.Clone(handlers.WithWeb(r.Context()))
	r.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	r.Header.Del(pigtail.HeaderCrossScope)
	r.Header.Del(pigtail.HeaderSession)
	if write {
		r.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
	}
	return r
}
