package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
)

// withCellScopes is withCells for any cell table: cells maps a cell id to its
// scope as the YAML says it (an alias such as work included). The session runs
// on the cell named session ("" = outside any cell). It returns each cell's
// config dir.
func withCellScopes(t *testing.T, cells map[string]string, session string) map[string]string {
	t.Helper()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	body := "cells:\n"
	for id, sc := range cells {
		dirs[id] = filepath.Join(xdg, "cell-"+id)
		body += "  " + id + ": { config_dir: " + dirs[id] + ", scope: " + sc + " }\n"
	}
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "no-cell")
	if session != "" {
		dir = dirs[session]
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv(envScope, "")
	prev := flagScope
	flagScope = ""
	t.Cleanup(func() { flagScope = prev })
	return dirs
}

type cellRow struct {
	Scope, Account, Home string
	Rank                 int
}

// withCellRows writes a cell table with account, rank and cwd_home, creates
// each config dir, and runs the session on the cell named session.
func withCellRows(t *testing.T, rows map[string]cellRow, session string) map[string]string {
	t.Helper()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	body := "cells:\n"
	for id, r := range rows {
		dirs[id] = filepath.Join(xdg, "cell-"+id)
		if err := os.MkdirAll(dirs[id], 0o700); err != nil {
			t.Fatal(err)
		}
		line := "  " + id + ": { config_dir: " + dirs[id] + ", scope: " + r.Scope + ", rank: " + strconv.Itoa(r.Rank)
		if r.Account != "" {
			line += ", account: " + r.Account
		}
		if r.Home != "" {
			line += ", cwd_home: " + r.Home
		}
		body += line + " }\n"
	}
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CLAUDE_CONFIG_DIR", dirs[session])
	t.Setenv(envScope, "")
	prev := flagScope
	flagScope = ""
	t.Cleanup(func() { flagScope = prev })
	return dirs
}
