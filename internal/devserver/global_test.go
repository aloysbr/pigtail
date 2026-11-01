package devserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// writeProject creates a fake project container: pigtail.yaml + worktrees
// with .env.local files. ports maps worktree name → "PORT=...\n..." content.
func writeProject(t *testing.T, name string, ports map[string]string) pigtail.Project {
	t.Helper()
	root := t.TempDir()
	yaml := `version: 1
name: ` + name + `
ports:
  dev: [3000, 3099]
  storybook: [6006, 6099]
`
	if err := os.WriteFile(filepath.Join(root, "pigtail.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	for wt, env := range ports {
		dir := filepath.Join(root, wt)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return pigtail.Project{Name: name, RootPath: root}
}

func TestResolveGlobal_portUnique(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\nSTORYBOOK_PORT=6006\n"}),
		writeProject(t, "beta", map[string]string{"main": "PORT=3050\nSTORYBOOK_PORT=6050\n"}),
	}
	ctx, wt, srv, err := ResolveGlobal(projects, Target{Port: 3050})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Config.Name != "beta" || wt != "main" || srv != "dev" {
		t.Errorf("got (%s, %q, %q), want (beta, main, dev)", ctx.Config.Name, wt, srv)
	}
}

func TestResolveGlobal_portAmbiguousAcrossProjects(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\n"}),
		writeProject(t, "beta", map[string]string{"main": "PORT=3000\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{Port: 3000})
	if err == nil {
		t.Fatal("expected cross-project ambiguity error")
	}
	for _, want := range []string{"alpha/main:dev", "beta/main:dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestResolveGlobal_portUnknownListsAllProjects(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{Port: 9999})
	if err == nil {
		t.Fatal("expected unknown-port error")
	}
	if !strings.Contains(err.Error(), "alpha/main dev=3000") {
		t.Errorf("error should list cross-project allocations, got: %v", err)
	}
}

func TestResolveGlobal_worktreeUnique(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"feat-x": "PORT=3001\n"}),
		writeProject(t, "beta", map[string]string{"feat-y": "PORT=3002\n"}),
	}
	ctx, wt, srv, err := ResolveGlobal(projects, Target{Worktree: "feat-y", Server: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Config.Name != "beta" || wt != "feat-y" || srv != "dev" {
		t.Errorf("got (%s, %q, %q), want (beta, feat-y, dev)", ctx.Config.Name, wt, srv)
	}
}

func TestResolveGlobal_worktreeAmbiguous(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\n"}),
		writeProject(t, "beta", map[string]string{"main": "PORT=3050\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{Worktree: "main"})
	if err == nil {
		t.Fatal("expected cross-project worktree ambiguity error")
	}
	for _, want := range []string{"alpha/main", "beta/main"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestResolveGlobal_worktreeUnknown(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{Worktree: "nope"})
	if err == nil {
		t.Fatal("expected unknown-worktree error")
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("error should name searched projects, got: %v", err)
	}
}

func TestResolveGlobal_staleProjectSkipped(t *testing.T) {
	stale := pigtail.Project{Name: "gone", RootPath: filepath.Join(t.TempDir(), "missing")}
	projects := []pigtail.Project{
		stale,
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\n"}),
	}
	ctx, wt, srv, err := ResolveGlobal(projects, Target{Port: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Config.Name != "alpha" || wt != "main" || srv != "dev" {
		t.Errorf("stale project should be skipped, got (%s, %q, %q)", ctx.Config.Name, wt, srv)
	}
}

func TestResolveGlobal_noProjects(t *testing.T) {
	_, _, _, err := ResolveGlobal(nil, Target{Port: 3000})
	if err == nil {
		t.Fatal("expected error with no registered projects")
	}
}

func TestResolveGlobal_emptyTarget(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"main": "PORT=3000\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{})
	if err == nil {
		t.Fatal("expected error: no current worktree outside a project")
	}
}

func TestResolveGlobal_worktreeExistsServerUnknown(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"feat-x": "PORT=3001\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{Worktree: "feat-x", Server: "devv"})
	if err == nil {
		t.Fatal("expected unknown-server error")
	}
	if !strings.Contains(err.Error(), `unknown server "devv"`) || !strings.Contains(err.Error(), "storybook") {
		t.Errorf("error should be an unknown-server error listing available servers, got: %v", err)
	}
}

func TestResolveGlobal_portAmbiguousWithinProject(t *testing.T) {
	projects := []pigtail.Project{
		writeProject(t, "alpha", map[string]string{"wt1": "PORT=3000\n", "wt2": "PORT=3000\n"}),
	}
	_, _, _, err := ResolveGlobal(projects, Target{Port: 3000})
	if err == nil {
		t.Fatal("expected intra-project ambiguity error")
	}
	if !strings.Contains(err.Error(), "worktree:server") {
		t.Errorf("intra-project hint should suggest worktree:server, got: %v", err)
	}
}
