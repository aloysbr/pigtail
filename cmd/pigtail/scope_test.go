package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

func cellTable(t *testing.T) *claudeprofile.Table {
	t.Helper()
	dir := t.TempDir()
	body := "cells:\n  t: { config_dir: /cfg/work, scope: globex }\n  p: { config_dir: /cfg/perso, scope: perso }\n"
	if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func TestResolveScope(t *testing.T) {
	t.Parallel()
	tbl := cellTable(t)
	cases := []struct {
		name      string
		cells     *claudeprofile.Table
		configDir string
		override  string
		want      string
		errPart   string
	}{
		{"work cell", tbl, "/cfg/work", "", "globex", ""},
		{"perso cell", tbl, "/cfg/perso", "", "perso", ""},
		{"bare dir is work", tbl, "/home/.claude", "", "globex", ""},
		{"no table is work", nil, "/cfg/perso", "", "globex", ""},
		// From a cell, --scope never changes identity: it only narrows the view.
		{"perso cell, work view keeps perso identity", tbl, "/cfg/perso", "gx", "perso", ""},
		{"perso cell, all keeps perso", tbl, "/cfg/perso", "all", "perso", ""},
		{"work cell, work override", tbl, "/cfg/work", "globex", "globex", ""},
		{"work cell cannot widen", tbl, "/cfg/work", "perso", "", "globex cell"},
		{"work cell cannot ask for all", tbl, "/cfg/work", "all", "", "globex cell"},
		{"invalid override", tbl, "/cfg/perso", "work", "", "invalid"},
		// Outside any cell (a human terminal), --scope is the identity.
		{"bare dir may say perso", tbl, "/home/.claude", "perso", "perso", ""},
		{"bare dir, all is perso", tbl, "/home/.claude", "all", "perso", ""},
		{"bare dir, work", tbl, "/home/.claude", "gx", "globex", ""},
	}
	for _, tc := range cases {
		got, err := resolveScope(tc.cells, tc.configDir, tc.override)
		if tc.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errPart) {
				t.Errorf("%s: err = %v, want one mentioning %q", tc.name, err, tc.errPart)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// TestResolveScopeAliasAndTable: the alias work is the globex scope on
// both sides (cell YAML, override); outside any cell all is the table's master,
// whatever it is called.
func TestResolveScopeAliasAndTable(t *testing.T) {
	cells := func(t *testing.T, body string) *claudeprofile.Table {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		tbl, err := claudeprofile.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return tbl
	}
	const body = "cells:\n  t: { config_dir: /cfg/t, scope: globex }\n  s: { config_dir: /cfg/s, scope: globex }\n  p: { config_dir: /cfg/p, scope: perso }\n  a: { config_dir: /cfg/a, scope: acme }\n"
	cases := []struct {
		name, table, configDir, override, want, errPart string
		errNot                                          []string
	}{
		{"work cell (YAML)", "", "/cfg/t", "", "globex", "", nil},
		{"globex cell, work override", "", "/cfg/s", "gx", "globex", "", nil},
		{"globex cell cannot widen", "", "/cfg/s", "perso", "", "runs on a globex cell", nil},
		{"acme cell cannot ask for globex", "three", "/cfg/a", "globex", "", "acme cell", nil},
		{"perso cell, work view", "", "/cfg/p", "gx", "perso", "", nil},
		{"no cell, work", "", "/home/.claude", "gx", "globex", "", nil},
		{"no cell, all is the master", "", "/home/.claude", "all", "perso", "", nil},
		{"no cell, acme", "three", "/home/.claude", "acme", "acme", "", nil},
		{"no cell, all is the renamed master", "renamed", "/home/.claude", "all", "root", "", nil},
		// D19: from a non-master cell, the error names its own scope only.
		{"globex cell, unknown override", "three", "/cfg/s", "nope", "", "invalid", []string{"acme", "perso"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.table {
			case "three":
				scopetest.Three(t)
			case "renamed":
				scopetest.RenamedMaster(t)
			}
			b := body
			if tc.table == "" {
				b = strings.Replace(body, "  a: { config_dir: /cfg/a, scope: acme }\n", "", 1)
			}
			got, err := resolveScope(cells(t, b), tc.configDir, tc.override)
			if tc.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errPart) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.errPart)
				}
				for _, n := range tc.errNot {
					if strings.Contains(err.Error(), n) {
						t.Fatalf("err = %v names %q", err, n)
					}
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("= %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestClaudeConfigDir(t *testing.T) {
	t.Parallel()
	env := map[string]string{"CLAUDE_CONFIG_DIR": "/cfg/perso"}
	if got := claudeConfigDir(func(k string) string { return env[k] }); got != "/cfg/perso" {
		t.Fatalf("claudeConfigDir = %q", got)
	}
	home, _ := os.UserHomeDir()
	if got := claudeConfigDir(func(string) string { return "" }); got != filepath.Join(home, ".claude") {
		t.Fatalf("claudeConfigDir default = %q", got)
	}
}

func TestParseSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	cases := map[string]time.Time{
		"24h":           now.Add(-24 * time.Hour),
		"90m":           now.Add(-90 * time.Minute),
		"7d":            now.AddDate(0, 0, -7),
		"1700000000000": time.UnixMilli(1700000000000),
		"2026-09-22":    time.Date(2026, 9, 22, 0, 0, 0, 0, time.Local),
	}
	for in, want := range cases {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "yesterday", "-3d", "3x"} {
		if _, err := parseSince(in, now); err == nil {
			t.Errorf("parseSince(%q) accepted", in)
		}
	}
}
