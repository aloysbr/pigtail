package staticsrv

// The outputs server (docs/wire-protocol.md § Static servers, « Outputs
// server »): pigtaild's permanent listener that serves, read-only, the files
// registered in the subject outputs registry and nothing else. A mockup,
// report or html output inside a round folder (`tN`, pigtail.TourDir) opens
// that round folder; any other one opens its file alone (its folder's
// index.html alone when it is registered as a folder). A capture, pdf or file
// output opens the file alone. The URL path is the file's absolute path, so a
// round's relative links (`../t2/`) work when that round is registered too.

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ServedEntry is one registered output the outputs server may open.
type ServedEntry struct {
	Kind string
	Path string
}

// folderKinds open their round folder (their file alone outside a round);
// fileKinds open their file alone. Any
// other kind (server, pr, clip, link) is never served.
var (
	folderKinds = []string{"mockup", "report", "html"}
	fileKinds   = []string{"capture", "pdf", "file"}
)

// Serves reports whether the outputs server opens an output of kind.
func Serves(kind string) bool {
	return slices.Contains(folderKinds, kind) || slices.Contains(fileKinds, kind)
}

// OutputsIndex is the set of folders and files the outputs server may open,
// every path resolved through its symlinks.
type OutputsIndex struct {
	roots []string
	files map[string]bool
	// indexDirs are folders opened only through their own index.html (a
	// registered index.html outside a round): the folder URL, nothing else.
	indexDirs map[string]bool
}

// buildOutputsIndex resolves entries into an index. A folder equal to a
// denied root, or holding a .git, is refused and logged, never served.
func buildOutputsIndex(entries []ServedEntry, denied []string, logf func(string, ...any)) *OutputsIndex {
	idx := &OutputsIndex{files: map[string]bool{}, indexDirs: map[string]bool{}}
	deniedReal := map[string]bool{}
	for _, d := range denied {
		if d == "" {
			continue
		}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			deniedReal[r] = true
		}
		deniedReal[filepath.Clean(d)] = true
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Path == "" || !filepath.IsAbs(e.Path) {
			continue
		}
		switch {
		case slices.Contains(folderKinds, e.Kind):
			// container: the folder the output lives in (itself when it is
			// registered as a folder) — refused whole when too wide.
			container, file := filepath.Dir(e.Path), e.Path
			if info, err := os.Stat(e.Path); err == nil && info.IsDir() {
				container, file = e.Path, filepath.Join(e.Path, "index.html")
			}
			real, err := filepath.EvalSymlinks(container)
			if err != nil {
				continue
			}
			if deniedReal[real] || deniedReal[filepath.Clean(container)] {
				if !seen[real] {
					logf("outputs: refused root %s: too wide (home or a scope's launch dir)", real)
				}
				seen[real] = true
				continue
			}
			if _, err := os.Lstat(filepath.Join(real, ".git")); err == nil {
				if !seen[real] {
					logf("outputs: refused root %s: holds a .git", real)
				}
				seen[real] = true
				continue
			}
			if _, ok := pigtail.TourDir(filepath.Base(container)); ok {
				if !seen[real] {
					idx.roots = append(idx.roots, real)
				}
				seen[real] = true
				continue
			}
			// Outside a round: the file alone.
			freal, err := filepath.EvalSymlinks(file)
			if err != nil {
				continue
			}
			idx.files[freal] = true
			if filepath.Base(freal) == "index.html" {
				idx.indexDirs[filepath.Dir(freal)] = true
			}
		case slices.Contains(fileKinds, e.Kind):
			if real, err := filepath.EvalSymlinks(e.Path); err == nil {
				idx.files[real] = true
				if filepath.Base(real) == "index.html" { // its URL is the folder (ServedPath)
					idx.indexDirs[filepath.Dir(real)] = true
				}
			}
		}
	}
	return idx
}

// allows reports whether the resolved path real is a registered file or lies
// inside a registered round folder.
func (idx *OutputsIndex) allows(real string) bool {
	return idx.files[real] || idx.indexDirs[real] || idx.insideRoot(real)
}

// insideRoot reports whether real lies inside (or is) a registered folder;
// a registered file alone never opens a folder.
func (idx *OutputsIndex) insideRoot(real string) bool {
	for _, r := range idx.roots {
		if real == r || strings.HasPrefix(real, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// open resolves the URL path p (a file's absolute path) to the file or folder
// the outputs server serves for it; ok false for anything it answers 404.
func (idx *OutputsIndex) open(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") { // ".", "..", dotfiles
			return "", false
		}
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(filepath.FromSlash(p)))
	if err != nil || !idx.allows(real) {
		return "", false
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		// No listing: a folder opens only through its own index.html, which
		// must itself stay inside a registered folder or be registered.
		ireal, err := filepath.EvalSymlinks(filepath.Join(real, "index.html"))
		if err != nil || (!idx.insideRoot(ireal) && !idx.files[ireal]) {
			return "", false
		}
		if st, err := os.Stat(ireal); err != nil || st.IsDir() {
			return "", false
		}
	}
	return real, true
}

// Opener snapshots the current index and returns a predicate telling whether
// the outputs server opens the file p at its URL (ServedPath(p)): the source
// of served_url, so a row the server refuses never carries a dead link.
func (h *OutputsHandler) Opener() (func(p string) bool, error) {
	idx, err := h.index()
	if err != nil {
		return nil, err
	}
	return func(p string) bool {
		if p == "" || !filepath.IsAbs(p) {
			return false
		}
		q := filepath.Clean(p)
		if filepath.Base(q) == "index.html" {
			q = filepath.Dir(q) // the URL ServedPath gives: the folder
		}
		_, ok := idx.open(filepath.ToSlash(q))
		return ok
	}, nil
}

// OutputsHandler serves the registered outputs. The index is rebuilt when the
// source's fingerprint changes (the source is read at each request).
type OutputsHandler struct {
	src    func() (string, []ServedEntry, error)
	hosts  []string
	csp    string
	denied []string
	logf   func(string, ...any)

	mu  sync.Mutex
	fp  string
	idx *OutputsIndex
}

// NewOutputsHandler builds the outputs handler. hosts are the accepted Host
// headers; frameAncestors the origins allowed to frame a served page (empty
// → 'none'); deniedRoots the folders never opened whole (each scope's launch
// dir, from the loaded scope table) — $HOME is always denied too.
func NewOutputsHandler(src func() (fingerprint string, entries []ServedEntry, err error), hosts []string, frameAncestors []string, deniedRoots []string) *OutputsHandler {
	fa := strings.Join(frameAncestors, " ")
	if fa == "" {
		fa = "'none'"
	}
	denied := append([]string(nil), deniedRoots...)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		denied = append(denied, home)
	}
	return &OutputsHandler{
		src:    src,
		hosts:  append([]string(nil), hosts...),
		csp:    "frame-ancestors " + fa,
		denied: denied,
		logf:   log.Printf,
	}
}

// index returns the current index, rebuilt under the mutex when the
// fingerprint moved.
func (h *OutputsHandler) index() (*OutputsIndex, error) {
	fp, entries, err := h.src()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idx == nil || fp != h.fp {
		h.idx = buildOutputsIndex(entries, h.denied, h.logf)
		h.fp = fp
	}
	return h.idx, nil
}

func (h *OutputsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", h.csp)
	hdr.Set("Cache-Control", "no-cache")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hdr.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Host == "" || !slices.Contains(h.hosts, r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idx, err := h.index()
	if err != nil {
		http.Error(w, "outputs unavailable", http.StatusServiceUnavailable)
		return
	}
	real, ok := idx.open(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, real)
}

// ServedPath is the outputs server's URL path of the file p: `…/index.html`
// shortened to `…/` (saves the 301 http.ServeFile answers), each segment
// escaped.
func ServedPath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	dir := false
	if strings.HasSuffix(p, "/index.html") {
		p = strings.TrimSuffix(p, "index.html")
		dir = true
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	out := "/" + strings.Join(segs, "/")
	if dir && out != "/" {
		out += "/"
	}
	return out
}
