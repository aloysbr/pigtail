package testrun

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skippedDirs are never walked when expanding a workspace glob.
//
// Not an optimisation: node_modules holds a monorepo's entire dependency tree,
// and a vendored package's own vite.config.ts matches `**/*/vite.config.ts`
// exactly as a project's does. Walking in would both cost a full dependency
// scan and hand vitest projects the repo never declared. dist and coverage are
// the same story for generated copies of the repo's own configs.
var skippedDirs = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"coverage":     true,
	".git":         true,
	".next":        true,
	".nx":          true,
	".turbo":       true,
	".cache":       true,
	"build":        true,
	"out":          true,
}

// ExpandGlobs resolves a workspace file's patterns into absolute config paths,
// the way vitest's own workspace resolution does: a pattern may name a config
// file or a directory that holds one.
//
// It is a deliberate reimplementation in Go rather than a call out to node.
// The list it returns is what `--explain` prints and what the wrappers are
// generated from, so it has to exist before anything is written to disk — and
// being a pure function of a tree, every glob question it answers is a table
// test instead of a surprise at run time.
//
// Results are absolute, deduplicated (two patterns may match one file; vitest
// would still run that project once) and sorted, so a generated config is
// byte-identical between runs.
//
// A pattern matching nothing is not an error: it is a workspace file pointing
// somewhere this checkout does not have, which the caller reports as a named
// skip. Only an unreadable tree is an error.
func ExpandGlobs(root string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	matchers := make([][]string, 0, len(patterns))
	for _, p := range patterns {
		clean := strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(p)), "./")
		clean = strings.TrimSuffix(clean, "/")
		if clean == "" {
			continue
		}
		matchers = append(matchers, strings.Split(clean, "/"))
	}
	if len(matchers) == 0 {
		return nil, nil
	}

	found := map[string]struct{}{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is not a reason to fail the
			// run: skip it and keep looking. The arbiter must never be why
			// tests do not start.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // deliberate: an unreadable entry is skipped, not fatal
		}
		if path == root {
			return nil
		}
		if d.IsDir() && skippedDirs[d.Name()] {
			return fs.SkipDir
		}

		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil //nolint:nilerr // outside the tree we were asked about
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")

		for _, m := range matchers {
			if !matchSegments(m, segs) {
				continue
			}
			if d.IsDir() {
				// A directory entry: vitest looks inside it for a config, in
				// the same preference order it uses everywhere else.
				if cfg := configInDir(path); cfg != "" {
					found[cfg] = struct{}{}
				}
				break
			}
			if isConfigName(d.Name()) {
				found[path] = struct{}{}
			}
			break
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	out := make([]string, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// matchSegments reports whether path segments match pattern segments, with
// "**" spanning zero or more segments and "*"/"?" confined to one.
//
// "**" matching *zero* segments matters: `**/vitest.config.ts` is how a repo
// says "anywhere, including the root", and treating it as "at least one level
// down" would silently drop the root project.
func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchSegments(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pattern[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], path[1:])
}

// configInDir returns the config vitest would pick inside dir, or "".
func configInDir(dir string) string {
	for _, name := range vitestConfigNames {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// isConfigName reports whether a matched file is a config vitest would accept
// as a project. A glob like `packages/*/*` can match a README; taking it as a
// project config would fail the run at load time.
func isConfigName(name string) bool {
	for _, n := range vitestConfigNames {
		if n == name {
			return true
		}
	}
	return false
}
