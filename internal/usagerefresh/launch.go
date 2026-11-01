// Package usagerefresh runs claude -p /usage on one Claude cell, isolated
// from the calling session: the real claude binary (never the cmux shim, which
// injects hooks and a session id even in -p), a cleaned environment, an empty
// temporary working directory, a deadline. /usage is a local Claude Code
// command: no model call, no token; Claude Code rewrites the cell's usage
// cache. pigtail itself never calls the network (D3 of E1b).
package usagerefresh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrClaudeNotFound = errors.New("claude not found on PATH (cmux shim skipped)")

// Args: print mode, no hooks or plugins (--safe-mode), no transcript, no MCP server.
var Args = []string{"-p", "--safe-mode", "--no-session-persistence", "--strict-mcp-config", "/usage"}

// CleanPath drops the cmux shim's entries (its own rule: the shim root, any
// */cmux-cli-shims/* entry) and every relative entry.
func CleanPath(path, shimRoot string) string {
	var keep []string
	for _, e := range filepath.SplitList(path) {
		if e == "" || !filepath.IsAbs(e) || (shimRoot != "" && e == shimRoot) ||
			strings.Contains(e, "/cmux-cli-shims/") || strings.HasSuffix(e, "/cmux-cli-shims") {
			continue
		}
		keep = append(keep, e)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// ResolveClaude is the first executable regular file named claude on cleanPath.
func ResolveClaude(cleanPath string) (string, error) {
	for _, dir := range filepath.SplitList(cleanPath) {
		p := filepath.Join(dir, "claude")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", ErrClaudeNotFound
}

var dropPrefixes = []string{"CLAUDE", "CMUX_", "ANTHROPIC_", "PIGTAIL_"}

// Env is environ without the calling session's Claude, cmux, Anthropic and
// pigtail variables nor PATH, plus the cleaned PATH, the cell's config dir and
// the cmux hooks switch.
func Env(environ []string, configDir, cleanPath string) []string {
	out := make([]string, 0, len(environ)+3)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		drop := k == "PATH"
		for _, p := range dropPrefixes {
			drop = drop || strings.HasPrefix(k, p)
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+cleanPath, "CLAUDE_CONFIG_DIR="+configDir, "CMUX_CLAUDE_HOOKS_DISABLED=1")
}

type ExitError struct {
	Code   int
	Stderr string // last non-empty stderr line, 200 characters at most
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("claude exited %d", e.Code)
	}
	return fmt.Sprintf("claude exited %d: %s", e.Code, e.Stderr)
}

type Launcher struct {
	Bin     string
	BinErr  error
	Environ []string
	Path    string
}

// New resolves the binary once, from environ's PATH minus the cmux shim.
func New(environ []string) *Launcher {
	get := func(k string) string {
		for _, kv := range environ {
			if key, v, ok := strings.Cut(kv, "="); ok && key == k {
				return v
			}
		}
		return ""
	}
	p := CleanPath(get("PATH"), get("CMUX_CLAUDE_WRAPPER_SHIM_ROOT"))
	bin, err := ResolveClaude(p)
	return &Launcher{Bin: bin, BinErr: err, Environ: environ, Path: p}
}

// Launch runs claude -p /usage on configDir and waits for it (or ctx).
func (l *Launcher) Launch(ctx context.Context, configDir string) error {
	if l.BinErr != nil {
		return l.BinErr
	}
	dir, err := os.MkdirTemp("", "pigtail-usage-refresh-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, l.Bin, Args...)
	cmd.Dir, cmd.Env = dir, Env(l.Environ, configDir, l.Path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, io.Discard, &limited{buf: &stderr, max: 64 << 10}
	cmd.WaitDelay = 2 * time.Second // a child holding the pipes never blocks us
	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Stderr: lastLine(stderr.String())}
	}
	return err
}

// limited keeps the last max bytes of stderr: the reason quotes its last
// non-empty line (S8), so the tail is what matters, never the head.
type limited struct {
	buf *bytes.Buffer
	max int
}

func (w *limited) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if over := w.buf.Len() - w.max; over > 0 {
		w.buf.Next(over)
	}
	return len(p), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(l); len(r) > 200 {
		l = string(r[:200])
	}
	return l
}
