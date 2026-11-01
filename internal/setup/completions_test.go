package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallZshCompletions_writesFile(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".config", "pigtail")

	err := InstallZshCompletions(configDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	compFile := filepath.Join(configDir, "completions", "_pigtail")
	content, err := os.ReadFile(compFile)
	if err != nil {
		t.Fatalf("completion file not created: %v", err)
	}
	if len(content) == 0 {
		t.Error("completion file is empty")
	}
}

func TestZshrcSnippet(t *testing.T) {
	s := ZshrcSnippet("/fake/.config/pigtail")
	if s == "" {
		t.Fatal("empty snippet")
	}
	if !strings.Contains(s, "fpath=") {
		t.Error("missing fpath")
	}
	if !strings.Contains(s, "compinit") {
		t.Error("missing compinit")
	}
}

func TestZshrcNeedsUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	zshrc := filepath.Join(tmpDir, ".zshrc")

	// Non-existent file: needs update
	needs, err := ZshrcNeedsUpdate(zshrc, "/fake")
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("expected needs update for non-existent file")
	}

	// File without marker: needs update
	os.WriteFile(zshrc, []byte("export PATH=/usr/bin"), 0o644)
	needs, err = ZshrcNeedsUpdate(zshrc, "/fake")
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("expected needs update for file without marker")
	}

	// File with marker: no update needed
	os.WriteFile(zshrc, []byte("# pigtail completions\nfpath=..."), 0o644)
	needs, err = ZshrcNeedsUpdate(zshrc, "/fake")
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("expected no update for file with marker")
	}
}

func TestZshrcSnippet_includesPtAlias(t *testing.T) {
	s := ZshrcSnippet("/fake/.config/pigtail")
	if !strings.Contains(s, "alias pt=pigtail") {
		t.Error("snippet should define the pt alias")
	}
	if !strings.Contains(s, "compdef pt=pigtail") {
		t.Error("snippet should wire pt completions")
	}
	// compdef requires compinit to have run first.
	if strings.Index(s, "compinit") > strings.Index(s, "compdef pt=pigtail") {
		t.Error("compdef must come after compinit in the snippet")
	}
}

func TestRemoveFromZshrc_removesPtAlias(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), ".zshrc")
	pre := "export FOO=bar\n"
	if err := os.WriteFile(tmp, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendToZshrc(tmp, "/fake/.config/pigtail"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFromZshrc(tmp); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "alias pt=") || strings.Contains(string(content), "compdef pt=") {
		t.Errorf("pt alias lines should be removed, got:\n%s", content)
	}
	if !strings.Contains(string(content), "export FOO=bar") {
		t.Error("unrelated lines must be preserved")
	}
}

func TestAppendAndRemoveFromZshrc(t *testing.T) {
	tmpDir := t.TempDir()
	zshrc := filepath.Join(tmpDir, ".zshrc")
	os.WriteFile(zshrc, []byte("export PATH=/usr/bin\n"), 0o644)

	// Append
	err := AppendToZshrc(zshrc, "/fake/.config/pigtail")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(zshrc)
	if !strings.Contains(string(content), zshrcMarker) {
		t.Error("marker not appended")
	}

	// Remove
	err = RemoveFromZshrc(zshrc)
	if err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(zshrc)
	if strings.Contains(string(content), zshrcMarker) {
		t.Error("marker not removed")
	}
	if !strings.Contains(string(content), "export PATH") {
		t.Error("original content was removed")
	}
}
