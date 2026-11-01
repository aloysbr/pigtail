package devserver

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// testContextWithServer builds a Context for worktree wt under container
// with one server, dev (port kind dev, env PORT), and writes env as wt's
// .env.local.
func testContextWithServer(t *testing.T, container, wt, env string) *Context {
	t.Helper()
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".env.local"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Context{
		ContainerDir: container,
		WorktreeDir:  wt,
		WorktreeName: filepath.Base(wt),
		Config: &pigtail.ProjectConfig{
			Servers: map[string]pigtail.ServerSpec{"dev": {Command: "true", PortKind: "dev"}},
			Ports:   map[string]pigtail.PortSpec{"dev": {Range: pigtail.PortRange{3000, 3099}}},
		},
	}
}

// The incident's other half: ws_392f9b's .env.local named 3013, which
// ws_efb2e9's server held. `pigtail stop` in ws_392f9b killed it.
func TestStopNeverKillsForeignListener(t *testing.T) {
	container := t.TempDir()
	mine, theirs := filepath.Join(container, "feat-y"), filepath.Join(container, "feat-x")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n") // pigtail.yaml with server dev → kind dev, and mine/.env.local
	oldBusy, oldCwds, oldKill, oldOut := portBusy, processCwds, killPIDsFn, out
	t.Cleanup(func() { portBusy, processCwds, killPIDsFn, out = oldBusy, oldCwds, oldKill, oldOut })
	portBusy = func(int) (bool, []int, error) { return true, []int{4242}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{4242: theirs}, nil }
	var killed [][]int
	killPIDsFn = func(p []int) { killed = append(killed, p) }
	var buf bytes.Buffer
	out = &buf

	if err := Stop(ctx, "feat-y", ""); err != nil {
		t.Fatal(err)
	}
	if len(killed) != 0 {
		t.Fatalf("killed %v: a foreign listener", killed)
	}
	if !strings.Contains(buf.String(), "kept dev (:3013) — held by another worktree ("+theirs+", pid 4242)") {
		t.Fatalf("output %q does not say why", buf.String())
	}
}

func TestStopKillsOnlyOwnedPids(t *testing.T) {
	container := t.TempDir()
	mine := filepath.Join(container, "feat-y")
	ctx := testContextWithServer(t, container, mine, "PORT=3013\n")
	oldBusy, oldCwds, oldKill := portBusy, processCwds, killPIDsFn
	t.Cleanup(func() { portBusy, processCwds, killPIDsFn = oldBusy, oldCwds, oldKill })
	portBusy = func(int) (bool, []int, error) { return true, []int{9, 4242}, nil }
	processCwds = func([]int) (map[int]string, error) { return map[int]string{9: mine}, nil }
	var killed [][]int
	killPIDsFn = func(p []int) { killed = append(killed, p) }
	if err := Stop(ctx, "feat-y", ""); err != nil {
		t.Fatal(err)
	}
	if len(killed) != 1 || !reflect.DeepEqual(killed[0], []int{9}) {
		t.Fatalf("killed %v, want [[9]] (4242 has no known cwd)", killed)
	}
}
