package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

// A broken scopes.yaml warns on every call with one generic sentence: the
// deny-all table is active, so no cell is the master, and ParseTable's error
// (scope names, vault roots) is printed to none (D19), the former master
// cell included. A valid file is read silently.
func TestLoadScopeTableWarnsInEveryCell(t *testing.T) {
	t.Cleanup(scope.Use(scope.Builtin()))
	dir := t.TempDir()
	twoMasters := "default: globex\nscopes:\n  perso: {vault_roots: [Perso/], sees_all: true}\n" +
		"  globex: {vault_roots: [Globex/], aliases: [gx]}\n  acme: {vault_roots: [ACME/], sees_all: true}\n"
	for _, body := range []string{"default: [\n", twoMasters} {
		if err := os.WriteFile(filepath.Join(dir, scope.TableFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var errw bytes.Buffer
		loadScopeTable(dir, &errw)
		out := errw.String()
		if out != invalidTableWarning+"\n" {
			t.Errorf("stderr = %q, want the generic warning alone", out)
		}
		for _, name := range []string{"perso", "globex", "gx", "acme", "Perso/", "Globex/", "ACME/", "sees_all"} {
			if strings.Contains(out, name) {
				t.Errorf("stderr names %q: %q", name, out)
			}
		}
		if scope.Active().Source() != scope.DenySource {
			t.Errorf("invalid file: active table %q, want the deny-all one", scope.Active().Source())
		}
	}
	if err := os.WriteFile(filepath.Join(dir, scope.TableFile), []byte(scopetest.ThreeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	var errw bytes.Buffer
	loadScopeTable(dir, &errw)
	if errw.Len() != 0 || !scope.Active().Has("acme") {
		t.Fatalf("valid file: stderr=%q has acme=%v", errw.String(), scope.Active().Has("acme"))
	}
}

// An invalid scopes.yaml leaves the CLI on the deny-all table, never on the
// builtin (whose one scope is the master): every scoped call refuses, the
// CLI still runs (doctor answers) and says why on stderr.
func TestCLIInvalidScopeTableDeniesScopedCalls(t *testing.T) {
	t.Cleanup(scope.Use(scope.Builtin()))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scope.TableFile), []byte("default: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errw bytes.Buffer
	loadScopeTable(dir, &errw)
	if !strings.Contains(errw.String(), "scopes.yaml invalid") || strings.Contains(errw.String(), "built-in") {
		t.Fatalf("stderr = %q, want the invalid-file warning, never « built-in »", errw.String())
	}
	tb := scope.Active()
	if tb == scope.Builtin() {
		t.Fatal("the builtin table is active after an invalid scopes.yaml")
	}
	for _, n := range scope.Builtin().Names() {
		if tb.SeesAll(n) {
			t.Fatalf("%q sees all on the CLI's table after an invalid file", n)
		}
	}
	if _, err := scope.ParseFor("", ""); err == nil {
		t.Fatal("ParseFor accepted a call on the deny-all table")
	}
	if _, err := scope.FromHeader(""); err == nil {
		t.Fatal("a caller naming no scope got one on the deny-all table")
	}
}
