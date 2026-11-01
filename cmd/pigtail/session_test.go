package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
)

func TestInventoryClassifiesByHostingConfigDir(t *testing.T) {
	t.Parallel()
	cfgDir := t.TempDir()
	work, max := t.TempDir(), t.TempDir()
	body := "cells:\n  t: { config_dir: " + work + ", scope: globex }\n  mt: { config_dir: " + max + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}

	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	dir := filepath.Join(work, "projects", "-w")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	binding := func(ref string) (string, error) {
		switch ref {
		case "surface:3":
			// The escaped '\'' quoting around the id is cmux's own — it is
			// how `cmux surface resume show` actually wraps a /bin/sh -c
			// payload. See internal/cmux's uuidRe / realBinding.
			return `cd -- '/w' 2>/dev/null || /bin/sh -c 'claude --resume'\''` + id + `'\''`, nil
		default:
			return "", nil // a surface with no session at all
		}
	}

	rows, ambiguous := inventory([]string{"surface:3", "surface:9"}, binding, tbl)
	if len(ambiguous) != 0 {
		t.Fatalf("ambiguous = %v, want none", ambiguous)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (a bindingless surface is not a session)", len(rows))
	}
	if rows[0].SessionID != id {
		t.Fatalf("SessionID = %q", rows[0].SessionID)
	}
	if rows[0].CellID != "t" {
		t.Fatalf("CellID = %q, want t (the config dir hosting the transcript)", rows[0].CellID)
	}
	if rows[0].Cwd != "/w" {
		t.Fatalf("Cwd = %q", rows[0].Cwd)
	}
}

func TestInventorySkipsSessionWithNoTranscript(t *testing.T) {
	t.Parallel()
	cfgDir, work := t.TempDir(), t.TempDir()
	body := "cells:\n  t: { config_dir: " + work + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, _ := claudeprofile.Load(cfgDir)

	binding := func(string) (string, error) {
		return `cd -- '/w' && /bin/sh -c 'claude --resume'\''00000000-0000-0000-0000-000000000000'\''`, nil
	}
	rows, ambiguous := inventory([]string{"surface:1"}, binding, tbl)
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0 (no transcript in any known cell)", len(rows))
	}
	if len(ambiguous) != 0 {
		t.Fatalf("a session with no transcript at all is not ambiguous, got %v", ambiguous)
	}
}

// TestInventoryReportsASessionHostedByTwoCells covers the state eight
// hand-migrated sessions are in on this machine today: the transcript exists
// in two config dirs at once. The row must not be silently dropped (which
// would make a live pane invisible), and no cell may be guessed for it — the
// surface comes back as ambiguous instead, named.
func TestInventoryReportsASessionHostedByTwoCells(t *testing.T) {
	t.Parallel()
	cfgDir := t.TempDir()
	work, max := t.TempDir(), t.TempDir()
	body := "cells:\n  t: { config_dir: " + work + ", scope: globex }\n  mt: { config_dir: " + max + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}

	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	for _, cfg := range []string{work, max} {
		dir := filepath.Join(cfg, "projects", "-w")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	binding := func(string) (string, error) { return realBindingFor(id), nil }

	rows, ambiguous := inventory([]string{"surface:1"}, binding, tbl)
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none: no cell can be picked for a session hosted twice", rows)
	}
	if len(ambiguous) != 1 {
		t.Fatalf("ambiguous = %v, want exactly one entry", ambiguous)
	}
	msg := ambiguous[0].Error()
	for _, want := range []string{"surface:1", work, max} {
		if !strings.Contains(msg, want) {
			t.Fatalf("ambiguity %q should name %q", msg, want)
		}
	}
}

// newTableWithTranscript builds a one-cell claudeprofile.Table (cell "t")
// whose config dir already has a transcript for id under the "/w" slug — the
// fixture shared by the listSessions tests below.
func newTableWithTranscript(t *testing.T, id string) *claudeprofile.Table {
	t.Helper()
	cfgDir, work := t.TempDir(), t.TempDir()
	body := "cells:\n  t: { config_dir: " + work + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "projects", "-w")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tbl
}

// realBindingFor renders a resume binding for id in the same escaped-quote
// shape `cmux surface resume show` actually produces (see uuidRe in
// internal/cmux).
func realBindingFor(id string) string {
	return `cd -- '/w' 2>/dev/null || /bin/sh -c 'claude --resume'\''` + id + `'\''`
}

// TestListSessionsWarnsOnUnreadableSurfaceButKeepsOthers covers the
// behaviour that has no other test: a surface whose binding lookup errors
// must be named in the warning, and must not suppress the rows produced by
// surfaces that were read successfully. This is the exact failure this
// command exists to prevent — a pane's session going invisible — so it needs
// to be exercised directly rather than trusted to "it compiled".
func TestListSessionsWarnsOnUnreadableSurfaceButKeepsOthers(t *testing.T) {
	t.Parallel()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	tbl := newTableWithTranscript(t, id)

	binding := func(ref string) (string, error) {
		switch ref {
		case "surface:1":
			return "", fmt.Errorf("cmux surface resume show surface:1: exit status 1 (output: unknown surface)")
		case "surface:2":
			return realBindingFor(id), nil
		default:
			t.Fatalf("unexpected ref %q", ref)
			return "", nil
		}
	}

	var out, warn bytes.Buffer
	listSessions([]string{"surface:1", "surface:2"}, binding, tbl, nil, &out, &warn)

	// The FULL id, not a prefix: `move --session` accepts nothing else, so a
	// truncated id would mean `ls` and `move` — the two subcommands this
	// feature ships — do not compose.
	if !strings.Contains(out.String(), id) {
		t.Fatalf("out = %q, want the readable surface's row with the full id %s", out.String(), id)
	}
	if strings.Contains(out.String(), "no Claude session found") {
		t.Fatalf("out = %q, a failure on one surface must not suppress the others", out.String())
	}
	if !strings.Contains(warn.String(), "surface:1") {
		t.Fatalf("warn = %q, want it to name the unreadable surface surface:1", warn.String())
	}
}

// TestListSessionsPrintsNoSessionFoundWhenEmpty covers the stdout branch
// through the same function used for the warning path, with no lookup
// failures at all.
func TestListSessionsPrintsNoSessionFoundWhenEmpty(t *testing.T) {
	t.Parallel()
	cfgDir := t.TempDir()
	body := "cells:\n  t: { config_dir: " + t.TempDir() + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}

	binding := func(string) (string, error) { return "", nil } // every surface bindingless

	var out, warn bytes.Buffer
	listSessions([]string{"surface:1"}, binding, tbl, nil, &out, &warn)

	if got := out.String(); got != "no Claude session found\n" {
		t.Fatalf("out = %q, want the no-session message", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("warn = %q, want empty when every binding lookup succeeded", warn.String())
	}
}
