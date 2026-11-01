package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
)

// The daemon reads scopes.yaml where the CLI does (setup.ConfigDir), not
// next to its socket: under --socket both must still see the same table.
func TestDaemonScopeTableFollowsConfigDirNotSocket(t *testing.T) {
	t.Cleanup(scope.Use(scope.Builtin()))
	xdg, sockDir := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", scope.TableFile), []byte(scopetest.ThreeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	// A decoy next to the socket must be ignored.
	if err := os.WriteFile(filepath.Join(sockDir, scope.TableFile), []byte("default: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := loadDaemonScopeTable()
	if err != nil || !tbl.Has("acme") {
		t.Fatalf("daemon table: has acme=%v err=%v — want the one in setup.ConfigDir()", tbl.Has("acme"), err)
	}
}

// An invalid spaces.yaml does not stop pigtaild: like the scope table, one log
// line and the built-in space table (plan, Task 2).
func TestDaemonSpaceTableInvalidFallsBackToBuiltin(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", "spaces.yaml"), []byte("spaces: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs []string
	tbl := loadDaemonSpaceTable(scope.Builtin(), func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	builtin, err := space.Builtin(scope.Builtin())
	if err != nil {
		t.Fatal(err)
	}
	if tbl == nil || !reflect.DeepEqual(tbl.Names(), builtin.Names()) {
		t.Fatalf("invalid spaces.yaml: table %v, want the built-in one %q", tbl, builtin.Names())
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "spaces.yaml") {
		t.Fatalf("logs = %q, want one line naming spaces.yaml", logs)
	}

	// A valid file is read, silently.
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", "spaces.yaml"), []byte("spaces:\n- {name: Solo, scope: "+string(scope.Default())+"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs = nil
	tbl = loadDaemonSpaceTable(scope.Builtin(), func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if tbl == nil || !reflect.DeepEqual(tbl.Names(), []string{"Solo"}) || len(logs) != 0 {
		t.Fatalf("valid file: table %v, logs %q", tbl, logs)
	}
}
