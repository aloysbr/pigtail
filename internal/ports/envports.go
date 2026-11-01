package ports

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/aloysbr/pigtail/internal/envlocal"
)

// EnvSource is one live workspace's .env.local and the variables in it that
// carry ports. `pigtail dev` launches on what this file says, not on the
// store, so a port a hand edit put here is taken even if the store disagrees.
type EnvSource struct {
	Dir  string   // worktree directory holding .env.local
	Vars []string // e.g. PORT, STORYBOOK_PORT
}

// EnvLister lists the .env.local files to honour on every Allocate.
type EnvLister interface {
	EnvSources() ([]EnvSource, error)
}

// WithEnvSources makes Allocate skip every port named in a live .env.local.
func WithEnvSources(l EnvLister) Option { return func(a *Allocator) { a.env = l } }

// envPorts reads every source's port variables. A missing file or a value
// that is not a positive integer is skipped: it names no port.
func envPorts(srcs []EnvSource) map[int]bool {
	namers := envPortNamers(srcs)
	out := make(map[int]bool, len(namers))
	for p := range namers {
		out[p] = true
	}
	return out
}

// envPortNamers lists, for each port, every Dir whose .env.local names it, in
// source order.
func envPortNamers(srcs []EnvSource) map[int][]string {
	out := map[int][]string{}
	for _, s := range srcs {
		b, err := os.ReadFile(filepath.Join(s.Dir, ".env.local"))
		if err != nil {
			continue
		}
		seen := map[int]bool{}
		for _, v := range s.Vars {
			val, ok := envlocal.Lookup(b, v)
			if !ok {
				continue
			}
			if p, err := strconv.Atoi(val); err == nil && p > 0 && !seen[p] {
				seen[p] = true
				out[p] = append(out[p], s.Dir)
			}
		}
	}
	return out
}
