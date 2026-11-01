package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/devserver"
)

// The toggle on a port whose owner cannot be determined stops nothing and
// says the owner is unknown — not another worktree.
func TestToggleServerOwnerUnknown(t *testing.T) {
	container := t.TempDir()
	yaml := "version: 1\nname: t\nbackend: git\nmain_worktree: main\nports:\n  dev: [3000, 3099]\n"
	if err := os.WriteFile(filepath.Join(container, "pigtail.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(container, "wt")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".env.local"), []byte(fmt.Sprintf("PORT=%d\n", 3042)), 0o600); err != nil {
		t.Fatal(err)
	}
	old := ownedListeners
	t.Cleanup(func() { ownedListeners = old })
	ownedListeners = func(int, string) (devserver.Ownership, error) {
		return devserver.Ownership{PIDs: []int{9}, ProbeErr: errors.New("lsof cwd: EMFILE")}, nil
	}

	msg := execToggleServer(nil, wtDir, "dev", "ws1")()
	done, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("toggle returned %#v", msg)
	}
	if strings.Contains(string(done), "another worktree") ||
		!strings.Contains(string(done), "owner unknown (cwd probe failed: lsof cwd: EMFILE; pid 9)") ||
		!strings.Contains(string(done), "not stopped") {
		t.Fatalf("toggle said %q, want the owner unknown and nothing stopped", done)
	}
}
