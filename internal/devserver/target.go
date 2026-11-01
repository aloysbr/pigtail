package devserver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Target is a parsed start/stop positional argument.
type Target struct {
	Port     int    // > 0 when the argument was numeric
	Worktree string // worktree name (empty = current worktree)
	Server   string // server name from the worktree:server form (empty = all servers for stop, default server for start)
}

// ParseTarget classifies a positional argument: a purely numeric argument is
// a port, "worktree:server" addresses one server explicitly, anything else
// is a worktree name. No guessing between server and worktree names.
func ParseTarget(arg string) Target {
	if port, err := strconv.Atoi(arg); err == nil {
		return Target{Port: port}
	}
	if wt, srv, ok := strings.Cut(arg, ":"); ok {
		return Target{Worktree: wt, Server: srv}
	}
	return Target{Worktree: arg}
}

// Resolve maps a Target to a concrete (worktree, server) pair.
// server == "" means the whole worktree (all servers for stop, the default
// server for start). Port resolution reads .env.local files, so it works
// even when the server is not running.
func (t Target) Resolve(ctx *Context) (worktree, server string, err error) {
	if t.Port > 0 {
		return resolvePort(ctx, t.Port)
	}
	wt := t.Worktree
	if wt == "" {
		wt = ctx.WorktreeName
	}
	if _, statErr := os.Stat(filepath.Join(ctx.ContainerDir, wt, ".env.local")); statErr != nil {
		return "", "", fmt.Errorf("unknown worktree %q (no .env.local in %s)", wt, filepath.Join(ctx.ContainerDir, wt))
	}
	if t.Server != "" {
		if _, ok := ctx.Config.Servers[t.Server]; !ok {
			return "", "", fmt.Errorf("unknown server %q (available: %s)", t.Server, strings.Join(serverNames(ctx.Config), ", "))
		}
	}
	return wt, t.Server, nil
}

// resolvePort finds which (worktree, server) owns a port by scanning every
// worktree's .env.local. Unknown ports fail with the known allocations so
// the user can self-correct. Duplicate allocations across worktrees are
// rejected as ambiguous.
func resolvePort(ctx *Context, port int) (string, string, error) {
	var known []string
	var matches []string
	entries, err := os.ReadDir(ctx.ContainerDir)
	if err != nil {
		return "", "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtDir := filepath.Join(ctx.ContainerDir, e.Name())
		if _, err := os.Stat(filepath.Join(wtDir, ".env.local")); err != nil {
			continue
		}
		for name, spec := range ctx.Config.Servers {
			envVar, ok := envVarFor(ctx.Config, spec)
			if !ok {
				continue
			}
			p, err := ReadPort(wtDir, envVar)
			if err != nil {
				continue
			}
			if p == port {
				matches = append(matches, e.Name()+":"+name)
				continue
			}
			known = append(known, fmt.Sprintf("%s %s=%d", e.Name(), name, p))
		}
	}
	switch len(matches) {
	case 1:
		wt, srv, _ := strings.Cut(matches[0], ":")
		return wt, srv, nil
	case 0:
		sort.Strings(known)
		return "", "", fmt.Errorf("port %d not allocated — known: %s", port, strings.Join(known, ", "))
	default:
		sort.Strings(matches)
		return "", "", fmt.Errorf("port %d is ambiguous across %s — address it as worktree:server", port, strings.Join(matches, ", "))
	}
}

// serverNames returns the configured server names, sorted.
func serverNames(cfg *pigtail.ProjectConfig) []string {
	names := make([]string, 0, len(cfg.Servers))
	for k := range cfg.Servers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
