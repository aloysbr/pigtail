package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// standbyReaders: where each Standby read may appear outside tests
// (§ Prédicat Standby). A listed file that does not exist yet fails nothing.
var standbyReaders = map[string][]string{
	"GetStandby(": {"internal/daemon/handlers/peers_standby.go"},
	"ListStandby(": {
		"internal/daemon/handlers/peers_standby.go", // syncStandbyMarkers
		"internal/daemon/handlers/relay_standby.go", // the compaction
		"internal/daemon/cmuxsync_wiring.go",        // the arrangement
	},
	"ListStandbyWorkspaces(":         {"internal/daemon/cmuxsync/runner.go"},
	"standbyMarkerDir(":              {"cmd/pigtail/standby_marker.go", "cmd/pigtail/cmux_check.go"},
	"filepath.Join(standbyMarkerDir": {},
}

var standbyReaderDirs = []string{"cmd/pigtail", "internal/daemon/handlers", "internal/daemon/cmuxsync", "internal/daemon"}

// standbyReadViolations scans the .go files (tests excluded) of each dir
// under root, not recursively, for a Standby read outside its allowed files.
func standbyReadViolations(t *testing.T, root string, dirs []string) (violations []string, scanned int) {
	t.Helper()
	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			scanned++
			rel := dir + "/" + name
			b, err := os.ReadFile(filepath.Join(root, dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for word, allowed := range standbyReaders {
				if strings.Contains(string(b), word) && !slices.Contains(allowed, rel) {
					violations = append(violations, rel+" spells "+word)
				}
			}
		}
	}
	return violations, scanned
}

func moduleRootOf(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

// One Standby predicate (Review Focus 8): the daemon reads the table through
// inStandby, the hooks and the CLI through the marker; a new automaton that
// reads the state another way fails here.
func TestStandbyReadOnlyThroughPredicate(t *testing.T) {
	t.Parallel()
	v, scanned := standbyReadViolations(t, moduleRootOf(t), standbyReaderDirs)
	if scanned == 0 {
		t.Fatal("scanned no Go file")
	}
	for _, s := range v {
		t.Errorf("%s: only the Standby predicate may (§ Prédicat Standby)", s)
	}
	// Positive control: a file outside the lists that reads the table is seen.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x", "auto.go"), []byte("package x\nfunc f(){ st.ListStandby() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, n := standbyReadViolations(t, root, []string{"x"}); n != 1 || len(v) != 1 || !strings.Contains(v[0], "ListStandby(") {
		t.Fatalf("positive control: %v (%d scanned)", v, n)
	}
}
