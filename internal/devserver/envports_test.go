package devserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestWorkspacePortsReadsEnvLocal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("PORT=3013\nSB=6010\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &pigtail.ProjectConfig{Ports: map[string]pigtail.PortSpec{
		"dev":       {Range: pigtail.PortRange{3000, 3099}},
		"storybook": {Range: pigtail.PortRange{6006, 6099}, Env: "SB"},
	}}
	got := WorkspacePorts(dir, cfg, map[string]int{"dev": 3000, "storybook": 6006})
	if got["dev"] != 3013 || got["storybook"] != 6010 {
		t.Fatalf("got %v, want .env.local values dev=3013 storybook=6010", got)
	}
}

func TestServersForFallsBackToDB(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("PORT=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &pigtail.ProjectConfig{Ports: map[string]pigtail.PortSpec{"dev": {Range: pigtail.PortRange{3000, 3099}}}}
	if got := WorkspacePorts(dir, cfg, map[string]int{"dev": 3000}); got["dev"] != 3000 {
		t.Fatalf("malformed value: got %v, want the store's 3000", got)
	}
	if got := WorkspacePorts(filepath.Join(dir, "gone"), nil, map[string]int{"dev": 3000}); got["dev"] != 3000 {
		t.Fatalf("no file, no config: got %v, want 3000", got)
	}
}
