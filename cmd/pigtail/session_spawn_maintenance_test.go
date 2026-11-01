package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

func TestSpawnRefusedUnderFreeze(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", dir)
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_s", On: true, Freeze: true, Reason: "bascule", Exempt: "sid-lead", Since: time.Now()})
	req := spawnRequest{SubjectID: "sub_x", Prompt: "go"}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-other")
	_, err := planSpawn(context.Background(), nil, nil, req)
	if err == nil || !strings.Contains(err.Error(), "MAINTENANCE_FROZEN") || !strings.Contains(err.Error(), "bascule") ||
		!strings.Contains(err.Error(), "nothing launched") {
		t.Fatalf("non-exempt spawn: %v", err)
	}
	if exitCode(err) != exitTempFail {
		t.Fatalf("freeze exits %d", exitCode(err))
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-lead")
	t.Setenv(cmux.EnvWorkspaceID, "") // the next guard (not in a cmux pane) answers: the freeze let it through
	if _, err := planSpawn(context.Background(), nil, nil, req); err == nil || strings.Contains(err.Error(), "MAINTENANCE_FROZEN") {
		t.Fatalf("exempt spawn must pass the freeze: %v", err)
	}
}

func TestSpawnUnreadableStateRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", dir)
	osWriteFileT(t, dir+"/"+maintenance.FileName, "{")
	if err := spawnFrozen(func(k string) string { return map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}[k] }); err == nil ||
		!strings.Contains(err.Error(), "MAINTENANCE_FROZEN") || !strings.Contains(err.Error(), "unreadable") ||
		!strings.Contains(err.Error(), "pigtail maintenance off") {
		t.Fatalf("got %v", err)
	}
}

func osWriteFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnOwnerPassesFreeze(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_s", On: true, Freeze: true, Reason: "bascule", Owner: "sid-owner", OwnerLabel: "Lead", OwnerScope: "globex", Since: time.Now()}) // read outside any cell: the default scope (D23)
	as := func(sid string) func(string) string {
		return func(k string) string {
			return map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir, "CLAUDE_CODE_SESSION_ID": sid}[k]
		}
	}
	if err := spawnFrozen(as("sid-owner")); err != nil {
		t.Fatalf("owner must pass the freeze: %v", err)
	}
	if err := spawnFrozen(as("sid-other")); err == nil || !strings.Contains(err.Error(), "MAINTENANCE_FROZEN") ||
		!strings.Contains(err.Error(), "set by session Lead") || !strings.Contains(err.Error(), "nothing launched") {
		t.Fatalf("other session: %v", err)
	}
}

// D4: the refused spawn marks its caller for the sidebar's "Blocked · freeze".
func TestSpawnFrozenMarksTheCaller(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := maintenance.State{ID: "mnt_s", On: true, Freeze: true, Reason: "bascule", Since: time.Now()}
	_ = maintenance.Save(dir, st)
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir, "CLAUDE_CODE_SESSION_ID": "s2"}
	if err := spawnFrozen(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "MAINTENANCE_FROZEN") {
		t.Fatalf("got %v", err)
	}
	if got := maintenance.FrozenSessions(dir, st); len(got) != 1 || !got["s2"] {
		t.Fatalf("frozen = %v", got)
	}
}

// D23: the spawn refusal names the owner as the caller's cell may see it.
func TestSpawnFrozenNamesOwnerPerCell(t *testing.T) {
	scopetest.Three(t)
	for _, c := range []struct {
		cell  string
		title bool
	}{{"s", false}, {"m", true}, {"", false}} {
		dirs := withCellScopes(t, map[string]string{"s": "globex", "m": "perso", "a": "acme"}, c.cell)
		cfg := dirs[c.cell]
		if c.cell == "" {
			cfg = t.TempDir()
		}
		dir := t.TempDir()
		_ = maintenance.Save(dir, maintenance.State{ID: "mnt_s", On: true, Freeze: true, Reason: "bascule", Since: time.Now(),
			Owner: "0ae10000-0000-4000-8000-000000000001", OwnerLabel: "chantier racine", OwnerScope: "perso"})
		getenv := func(k string) string {
			return map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir, "CLAUDE_CODE_SESSION_ID": "sid-other", "CLAUDE_CONFIG_DIR": cfg}[k]
		}
		err := spawnFrozen(getenv)
		if err == nil || !strings.Contains(err.Error(), "MAINTENANCE_FROZEN") {
			t.Fatalf("cell %q: %v", c.cell, err)
		}
		if c.title != strings.Contains(err.Error(), "set by session chantier racine") || (!c.title && !strings.Contains(err.Error(), "0ae10000")) {
			t.Fatalf("cell %q: %v", c.cell, err)
		}
	}
}
