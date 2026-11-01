package devserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		arg  string
		want Target
	}{
		{"3030", Target{Port: 3030}},
		{"6010", Target{Port: 6010}},
		{"feat-auth", Target{Worktree: "feat-auth"}},
		{"feat-auth:storybook", Target{Worktree: "feat-auth", Server: "storybook"}},
		{"main:dev", Target{Worktree: "main", Server: "dev"}},
		{"", Target{}},                  // empty = unspecified → current worktree at resolution
		{":", Target{Worktree: "", Server: ""}}, // treated as a bare current-worktree target at resolution
	}
	for _, c := range cases {
		got := ParseTarget(c.arg)
		if got != c.want {
			t.Errorf("ParseTarget(%q) = %+v, want %+v", c.arg, got, c.want)
		}
	}
}

// testContext builds a fake container with two worktrees and their .env.local.
func testContext(t *testing.T) *Context {
	t.Helper()
	container := t.TempDir()
	write := func(wt, content string) {
		dir := filepath.Join(container, wt)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("main", "PORT=3000\nSTORYBOOK_PORT=6006\n")
	write("feat-auth", "PORT=3010\nSTORYBOOK_PORT=6010\n")
	return &Context{
		ContainerDir: container,
		WorktreeDir:  filepath.Join(container, "main"),
		WorktreeName: "main",
		Config: &pigtail.ProjectConfig{
			Servers: map[string]pigtail.ServerSpec{
				"dev":       {Command: "npm run dev", PortKind: "dev"},
				"storybook": {Command: "npm run storybook:dev", PortKind: "storybook"},
			},
			Ports: map[string]pigtail.PortSpec{
				"dev":       {Range: [2]int{3000, 3099}, Env: "PORT"},
				"storybook": {Range: [2]int{6006, 6099}, Env: "STORYBOOK_PORT"},
			},
		},
	}
}

func TestResolve_bareColon(t *testing.T) {
	ctx := testContext(t)
	wt, srv, err := ParseTarget(":").Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wt != "main" || srv != "" {
		t.Errorf("got (%q, %q), want (main, \"\") — \":\" behaves like a bare current-worktree target", wt, srv)
	}
}

func TestResolve_port(t *testing.T) {
	ctx := testContext(t)
	wt, srv, err := Target{Port: 6010}.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wt != "feat-auth" || srv != "storybook" {
		t.Errorf("got (%q, %q), want (feat-auth, storybook)", wt, srv)
	}
}

func TestResolve_portUnknown(t *testing.T) {
	ctx := testContext(t)
	_, _, err := Target{Port: 9999}.Resolve(ctx)
	if err == nil {
		t.Fatal("expected error for unknown port")
	}
	// The error must list known allocations to guide the user.
	for _, want := range []string{"feat-auth dev=3010", "feat-auth storybook=6010", "main dev=3000", "main storybook=6006"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list %q, got: %v", want, err)
		}
	}
}

func TestResolve_emptyDefaultsToCurrentWorktree(t *testing.T) {
	ctx := testContext(t)
	wt, srv, err := Target{}.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wt != "main" || srv != "" {
		t.Errorf("got (%q, %q), want (main, \"\")", wt, srv)
	}
}

func TestResolve_worktreeServer(t *testing.T) {
	ctx := testContext(t)
	wt, srv, err := Target{Worktree: "feat-auth", Server: "storybook"}.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wt != "feat-auth" || srv != "storybook" {
		t.Errorf("got (%q, %q), want (feat-auth, storybook)", wt, srv)
	}
}

func TestResolve_unknownWorktree(t *testing.T) {
	ctx := testContext(t)
	_, _, err := Target{Worktree: "nope"}.Resolve(ctx)
	if err == nil {
		t.Fatal("expected error for unknown worktree")
	}
}

func TestResolve_unknownServer(t *testing.T) {
	ctx := testContext(t)
	_, _, err := Target{Worktree: "main", Server: "nope"}.Resolve(ctx)
	if err == nil {
		t.Fatal("expected error for unknown server")
	}
	if !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "storybook") {
		t.Errorf("error should list available servers, got: %v", err)
	}
}

func TestResolve_portAmbiguous(t *testing.T) {
	ctx := testContext(t)
	// Third worktree reusing main's dev port — a real-world collision.
	dir := filepath.Join(ctx.ContainerDir, "feat-dup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("PORT=3000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Target{Port: 3000}.Resolve(ctx)
	if err == nil {
		t.Fatal("expected ambiguity error for duplicated port")
	}
	for _, want := range []string{"feat-dup:dev", "main:dev", "worktree:server"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}
