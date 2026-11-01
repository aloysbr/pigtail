package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestCheckPortsReadsEnvLocalAndOwnership(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".env.local"), []byte("PORT=3023\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := ownedListeners
	t.Cleanup(func() { ownedListeners = old })
	ownedListeners = func(port int, dir string) (devserver.Ownership, error) {
		switch port {
		case 3013: // the store's port, held by another worktree (the incident)
			return devserver.Ownership{PIDs: []int{4242}, Foreign: map[int]string{4242: "/elsewhere"}}, nil
		case 3023: // .env.local's port, this worktree's own server
			return devserver.Ownership{PIDs: []int{9}, Owned: []int{9}, Foreign: map[int]string{}}, nil
		}
		return devserver.Ownership{}, nil
	}
	msg := checkPorts([]pigtail.Workspace{{ID: "ws_392f9b", Path: wt, Ports: map[string]int{"dev": 3013}}})()
	info := msg.(portStatusMsg)["ws_392f9b"]
	if info.DevPort != 3023 || !info.DevAlive {
		t.Fatalf("got %+v, want dev :3023 alive (from .env.local, owned)", info)
	}
	ownedListeners = func(int, string) (devserver.Ownership, error) {
		return devserver.Ownership{PIDs: []int{4242}, Foreign: map[int]string{4242: "/elsewhere"}}, nil
	}
	if info := checkPorts([]pigtail.Workspace{{ID: "ws_x", Path: wt, Ports: map[string]int{"dev": 3023}}})().(portStatusMsg)["ws_x"]; info.DevAlive {
		t.Fatal("a foreign listener must not light the worktree's server as alive")
	}
}
