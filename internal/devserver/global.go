package devserver

// This file implements global target resolution across daemon-registered
// projects: it lets `pigtail start/stop <target>` work from a cwd outside
// any project container by consulting the registry instead of walking up
// to a pigtail.yaml.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// globalMatch is one (project, worktree, server) candidate.
type globalMatch struct {
	ctx      *Context
	worktree string
	server   string
}

func (m globalMatch) label() string {
	s := m.ctx.Config.Name + "/" + m.worktree
	if m.server != "" {
		s += ":" + m.server
	}
	return s
}

// ResolveGlobal resolves a target across all registered projects. It is the
// out-of-project counterpart of Target.Resolve: there is no "current
// worktree", so an empty target is an error, and port/worktree lookups span
// every project. Stale registrations (RootPath gone or no pigtail.yaml) are
// skipped. For port targets the matched server name is returned (never "").
func ResolveGlobal(projects []pigtail.Project, t Target) (*Context, string, string, error) {
	ctxs := loadProjectContexts(projects)
	if len(ctxs) == 0 {
		return nil, "", "", fmt.Errorf("no registered projects with a readable pigtail.yaml (is pigtaild running and the project initialized?)")
	}
	if t.Port > 0 {
		return resolveGlobalPort(ctxs, t.Port)
	}
	if t.Worktree == "" {
		return nil, "", "", fmt.Errorf("no current worktree outside a project — pass a port, worktree, or worktree:server target")
	}
	return resolveGlobalWorktree(ctxs, t)
}

// loadProjectContexts builds a devserver Context per registered project,
// silently skipping stale registrations.
func loadProjectContexts(projects []pigtail.Project) []*Context {
	var out []*Context
	for _, p := range projects {
		cfg, err := pigtail.LoadProjectConfig(p.RootPath)
		if err != nil {
			continue // stale registration — not this feature's job to clean
		}
		out = append(out, &Context{
			ContainerDir: p.RootPath,
			WorktreeDir:  filepath.Join(p.RootPath, cfg.MainWorktree),
			// WorktreeName is informational only here — global resolution
			// never uses an implicit "current worktree" default.
			WorktreeName: cfg.MainWorktree,
			Config:       cfg,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Config.Name < out[j].Config.Name })
	return out
}

// resolveGlobalPort finds the (project, worktree, server) owning a port.
func resolveGlobalPort(ctxs []*Context, port int) (*Context, string, string, error) {
	var matches []globalMatch
	var known []string
	for _, ctx := range ctxs {
		entries, err := os.ReadDir(ctx.ContainerDir)
		if err != nil {
			continue
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
					matches = append(matches, globalMatch{ctx, e.Name(), name})
					continue
				}
				known = append(known, fmt.Sprintf("%s/%s %s=%d", ctx.Config.Name, e.Name(), name, p))
			}
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ctx, matches[0].worktree, matches[0].server, nil
	case 0:
		sort.Strings(known)
		return nil, "", "", fmt.Errorf("port %d not allocated in any registered project — known: %s", port, strings.Join(known, ", "))
	default:
		labels := matchLabels(matches)
		hint := "cd into the right project to address it"
		if sameProject(matches) {
			hint = "address it as worktree:server from inside the project"
		}
		return nil, "", "", fmt.Errorf("port %d is ambiguous across %s — %s", port, strings.Join(labels, ", "), hint)
	}
}

// resolveGlobalWorktree finds the project owning a worktree name.
func resolveGlobalWorktree(ctxs []*Context, t Target) (*Context, string, string, error) {
	var matches []globalMatch
	var names []string
	var serverErr error
	for _, ctx := range ctxs {
		names = append(names, ctx.Config.Name)
		if _, err := os.Stat(filepath.Join(ctx.ContainerDir, t.Worktree, ".env.local")); err != nil {
			continue
		}
		if t.Server != "" {
			if _, ok := ctx.Config.Servers[t.Server]; !ok {
				serverErr = fmt.Errorf("unknown server %q in project %s (available: %s)", t.Server, ctx.Config.Name, strings.Join(serverNames(ctx.Config), ", "))
				continue
			}
		}
		matches = append(matches, globalMatch{ctx, t.Worktree, t.Server})
	}
	switch len(matches) {
	case 1:
		return matches[0].ctx, matches[0].worktree, matches[0].server, nil
	case 0:
		if serverErr != nil {
			return nil, "", "", serverErr
		}
		sort.Strings(names)
		return nil, "", "", fmt.Errorf("worktree %q not found in any registered project (searched: %s)", t.Worktree, strings.Join(names, ", "))
	default:
		labels := matchLabels(matches)
		return nil, "", "", fmt.Errorf("worktree %q exists in several projects: %s — cd into the right project to address it", t.Worktree, strings.Join(labels, ", "))
	}
}

// sameProject reports whether all matches belong to one project.
func sameProject(matches []globalMatch) bool {
	for _, m := range matches[1:] {
		if m.ctx != matches[0].ctx {
			return false
		}
	}
	return true
}

func matchLabels(matches []globalMatch) []string {
	labels := make([]string, 0, len(matches))
	for _, m := range matches {
		labels = append(labels, m.label())
	}
	sort.Strings(labels)
	return labels
}
