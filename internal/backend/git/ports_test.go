package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestWritePortsRewritesOnlyTheGivenKinds(t *testing.T) {
	dir := t.TempDir()
	orig := "# keep me\nexport PORT=3013 # dev\nSTORYBOOK_PORT=6006\nAPI_URL=http://x\n"
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Backend{}
	ws := &pigtail.Workspace{Path: dir}
	restore, err := b.WritePorts(ws, map[string]int{"dev": 3023}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".env.local"))
	want := "# keep me\nexport PORT=3023\nSTORYBOOK_PORT=6006\nAPI_URL=http://x\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	read, err := b.ReadPorts(ws, []string{"dev", "storybook"}, nil)
	if err != nil || read["dev"] != 3023 || read["storybook"] != 6006 {
		t.Fatalf("read %v, %v", read, err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if back, _ := os.ReadFile(filepath.Join(dir, ".env.local")); string(back) != orig {
		t.Fatalf("restore gave\n%s\nwant the original bytes\n%s", back, orig)
	}
}

func TestWritePortsRestoreRemovesAFileThatDidNotExist(t *testing.T) {
	dir := t.TempDir()
	restore, err := (&Backend{}).WritePorts(&pigtail.Workspace{Path: dir}, map[string]int{"dev": 3023}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env.local")); !os.IsNotExist(err) {
		t.Fatalf("restore left a file behind: %v", err)
	}
}

func TestReadPortsRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("PORT=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Backend{}).ReadPorts(&pigtail.Workspace{Path: dir}, []string{"dev"}, nil); err == nil {
		t.Fatal("PORT=abc must be an error, not a silent skip")
	}
}
