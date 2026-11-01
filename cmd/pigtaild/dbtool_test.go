package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/dbtool"
	"github.com/aloysbr/pigtail/internal/scope"
)

func TestDBToolModeIgnoresEverythingElse(t *testing.T) {
	for _, args := range [][]string{nil, {"-version"}, {"-db", "x"}, {"db-tool"}} {
		handled, _ := dbToolMode(args, dbtool.IO{}, func() (*scope.Table, error) { t.Fatal("table loaded"); return nil, nil })
		if handled {
			t.Fatalf("%v handled as -db-tool", args)
		}
	}
}

func TestDBToolModeRunsTheEngineWithTheScopeTable(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BackupRotated(store.BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	tbl, _, err := scope.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loaded := false
	var out, errOut bytes.Buffer
	o := dbtool.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut,
		IsTTY: func() bool { return false }, Now: time.Now}
	for _, flagName := range []string{"-db-tool", "--db-tool"} {
		out.Reset()
		handled, code := dbToolMode([]string{flagName, "list", "-view", string(tbl.Master()), "-db", db}, o,
			func() (*scope.Table, error) { loaded = true; return tbl, nil })
		if !handled || code != dbtool.ExitOK || !loaded {
			t.Fatalf("%s: handled %v code %d loaded %v stderr %q", flagName, handled, code, loaded, errOut.String())
		}
		if !strings.Contains(out.String(), "GENERATION") {
			t.Fatalf("%s: stdout %q", flagName, out.String())
		}
	}
}
