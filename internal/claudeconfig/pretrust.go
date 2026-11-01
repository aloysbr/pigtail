// Package claudeconfig holds the one write pigtail makes to a Claude Code
// config file ($CLAUDE_CONFIG_DIR/.claude.json): accepting folder trust for a
// repository, inherited from an ancestor directory the human already trusted
// in the same cell, strictly below $HOME (never $HOME, never above it).
// Everything else in the file is Claude's and is carried through untouched.
package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the config file pigtail writes in a cell's CLAUDE_CONFIG_DIR.
const FileName = ".claude.json"

// Outcome is what PreTrust did, or why it did nothing.
type Outcome string

const (
	Written           Outcome = "written"
	AlreadyTrusted    Outcome = "already_trusted"
	NoTrustedAncestor Outcome = "no_trusted_ancestor"
	NotEligible       Outcome = "not_eligible"
	Failed            Outcome = "failed"
)

// Result reports a PreTrust: the key it acts on, the trusted ancestor it
// inherited from when it wrote, and the reason when it did not.
type Result struct {
	Outcome  Outcome `json:"outcome"`
	Key      string  `json:"key,omitempty"`
	Ancestor string  `json:"ancestor,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

// Request names the cell config dir, the key to trust (the repository's main
// checkout), the worktree being launched in, and $HOME (the walk's bound).
// Every path must be absolute.
type Request struct{ ConfigDir, Key, Worktree, Home string }

// defaultProject is Claude Code's own default project entry (2.1.289): Claude
// reads projects[key] ?? defaults without merging, so an entry pigtail
// creates must carry every default.
var defaultProject = map[string]any{
	"allowedTools": []any{}, "mcpContextUris": []any{}, "mcpServers": map[string]any{},
	"enabledMcpjsonServers": []any{}, "disabledMcpjsonServers": []any{},
	"hasTrustDialogAccepted":              false,
	"hasClaudeMdExternalIncludesApproved": false, "hasClaudeMdExternalIncludesWarningShown": false,
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// within reports whether p is strictly below dir.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// candidates lists the ancestors of each start strictly below home, nearest
// first, without duplicates (D3: home and above never count).
func candidates(home string, starts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range starts {
		for d := filepath.Dir(s); within(d, home); d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

type file struct {
	top      map[string]json.RawMessage
	projects map[string]json.RawMessage
}

func load(path string) (*file, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &file{}
	if err := json.Unmarshal(b, &f.top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.top == nil { // the file is the JSON literal null
		f.top = map[string]json.RawMessage{}
	}
	if raw, ok := f.top["projects"]; ok {
		if err := json.Unmarshal(raw, &f.projects); err != nil {
			return nil, fmt.Errorf("parse %s projects: %w", path, err)
		}
	}
	if f.projects == nil { // absent, or "projects": null
		f.projects = map[string]json.RawMessage{}
	}
	return f, nil
}

func (f *file) trusted(dir string) bool {
	var e struct {
		T *bool `json:"hasTrustDialogAccepted"`
	}
	raw, ok := f.projects[dir]
	return ok && json.Unmarshal(raw, &e) == nil && e.T != nil && *e.T
}

func (f *file) firstTrusted(dirs []string) string {
	for _, d := range dirs {
		if f.trusted(d) {
			return d
		}
	}
	return ""
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// setTrust flips the key's boolean, keeping the entry's other fields, or
// creates the entry from Claude's defaults.
func (f *file) setTrust(key string) ([]byte, error) {
	entry := map[string]json.RawMessage{}
	// An entry that is null reads as absent, as Claude's `projects[key] ?? defaults` does.
	if raw, ok := f.projects[key]; ok && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("parse project %s: %w", key, err)
		}
		if entry == nil {
			entry = map[string]json.RawMessage{}
		}
	} else {
		for k, v := range defaultProject {
			b, _ := json.Marshal(v)
			entry[k] = b
		}
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	e, err := marshal(entry)
	if err != nil {
		return nil, err
	}
	f.projects[key] = e
	p, err := marshal(f.projects)
	if err != nil {
		return nil, err
	}
	f.top["projects"] = p
	return marshal(f.top)
}

// Test seams for the failure paths taken while the lock is held.
var (
	lockedHook  = func(path string) {} // runs right after the lock is taken
	writeConfig = writeAtomic
)

// PreTrust accepts folder trust for req.Key in req.ConfigDir when an ancestor
// of the key or of the worktree, strictly below req.Home, is already trusted
// there.
// It never fails a launch: every problem is an Outcome with a Reason.
func PreTrust(req Request) Result {
	r := Result{Key: req.Key}
	no := func(o Outcome, format string, a ...any) Result {
		r.Outcome, r.Reason = o, fmt.Sprintf(format, a...)
		return r
	}
	for _, p := range []string{req.ConfigDir, req.Key, req.Worktree, req.Home} {
		if !filepath.IsAbs(p) {
			return no(NotEligible, "path %q is not absolute", p)
		}
	}
	home := resolved(req.Home)
	key, wt := filepath.Clean(req.Key), filepath.Clean(req.Worktree)
	if !within(key, home) || !within(wt, home) {
		return no(NotEligible, "%s or %s is not strictly below $HOME (%s)", key, wt, home)
	}
	if _, err := os.Stat(filepath.Join(req.ConfigDir, ".config.json")); err == nil {
		return no(NotEligible, "%s holds a legacy .config.json, which Claude reads instead of %s", req.ConfigDir, FileName)
	}
	if os.Getenv("CLAUDE_CODE_CUSTOM_OAUTH_URL") != "" {
		return no(NotEligible, "CLAUDE_CODE_CUSTOM_OAUTH_URL is set: Claude uses another config file name")
	}
	path := filepath.Join(req.ConfigDir, FileName)
	dirs := candidates(home, key, wt)
	f, err := load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return no(NoTrustedAncestor, "no %s in %s", FileName, req.ConfigDir)
	case err != nil:
		return no(Failed, "%v", err)
	case f.trusted(key):
		r.Outcome = AlreadyTrusted
		return r
	case f.firstTrusted(dirs) == "":
		return no(NoTrustedAncestor, "no directory between %s and $HOME (excluded) is trusted in %s", filepath.Dir(key), req.ConfigDir)
	}
	release, err := acquire(path + ".lock")
	if err != nil {
		return no(Failed, "%v", err)
	}
	defer release()
	lockedHook(path)
	if f, err = load(path); err != nil { // re-read under the lock: Claude may have written since
		return no(Failed, "%v", err)
	}
	if f.trusted(key) {
		r.Outcome = AlreadyTrusted
		return r
	}
	if r.Ancestor = f.firstTrusted(dirs); r.Ancestor == "" {
		return no(NoTrustedAncestor, "no directory between %s and $HOME (excluded) is trusted in %s", filepath.Dir(key), req.ConfigDir)
	}
	b, err := f.setTrust(key)
	if err != nil {
		return no(Failed, "%v", err)
	}
	if err := writeConfig(path, b); err != nil {
		return no(Failed, "write %s: %v", path, err)
	}
	r.Outcome = Written
	return r
}
