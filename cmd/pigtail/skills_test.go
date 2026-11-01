package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/skills"
)

func runSkills(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"skills"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestSkillsInstall(t *testing.T) {
	dir := testutil.MkdirTemp(t, "skills-")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	target := filepath.Join(dir, "skills", "pigtail", "SKILL.md")

	if out, err := runSkills(t, "install"); err != nil || !strings.Contains(out, "installed") {
		t.Fatalf("absent: %q %v", out, err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, skills.Pigtail) {
		t.Fatal("installed file differs from the embedded one")
	}
	if out, err := runSkills(t, "install"); err != nil || !strings.Contains(out, "already up to date") {
		t.Fatalf("identical: %q %v", out, err)
	}
	if err := os.WriteFile(target, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkills(t, "install"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("different without --force: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "mine" {
		t.Fatal("a different file was overwritten without --force")
	}
	if _, err := runSkills(t, "install", "--force"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, skills.Pigtail) {
		t.Fatal("--force did not overwrite")
	}
}

func TestSkillsShow(t *testing.T) {
	out, err := runSkills(t, "show")
	if err != nil || out != string(skills.Pigtail) {
		t.Fatalf("show: %v", err)
	}
}

func TestSkillCitedCommandsExist(t *testing.T) {
	cmds := citedCommands(string(skills.Pigtail))
	if len(cmds) < 6 {
		t.Fatalf("SKILL.md cites %d commands", len(cmds))
	}
	root := newRootCmd()
	for _, words := range cmds {
		checkCitedCommand(t, root, words)
	}
}

func TestSkillIsNeutral(t *testing.T) {
	low := strings.ToLower(string(skills.Pigtail))
	for _, w := range []string{"duetto", "alice", "tenantcorp", "methods", "--step"} {
		if strings.Contains(low, w) {
			t.Errorf("SKILL.md mentions %q", w)
		}
	}
	if !strings.HasPrefix(string(skills.Pigtail), "---\nname: pigtail\ndescription: ") {
		t.Error("frontmatter missing name/description")
	}
}
