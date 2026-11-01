package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -cmux-claude-code-fix (a bool, on by default in pigtaild) switches the
// temporary claude_code patch off without switching the sidebar loop off. An
// explicit off written in the plist must survive `pigtail setup`, and a plist
// carrying it must not make setup abort as an unknown flag.

func TestReadPlistTuning_cmuxClaudeCodeFix(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want bool
	}{
		{"--cmux-claude-code-fix", true},
		{"-cmux-claude-code-fix=false", false},
		{"--cmux-claude-code-fix=0", false},
	} {
		got, err := ReadPlistTuning(writePlistArgs(t, tc.arg, "--max-servers", "8"))
		if err != nil {
			t.Fatalf("%s: %v", tc.arg, err)
		}
		if got.CmuxClaudeCodeFix == nil || *got.CmuxClaudeCodeFix != tc.want {
			t.Errorf("%s = %v, want %v", tc.arg, got.CmuxClaudeCodeFix, tc.want)
		}
		if got.MaxServers == nil || *got.MaxServers != 8 {
			t.Errorf("%s swallowed the next flag", tc.arg)
		}
	}
}

func TestGeneratePlistCmuxClaudeCodeFix(t *testing.T) {
	plist, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "claude-code-fix") {
		t.Fatalf("unset: the flag must be omitted so pigtaild's default applies:\n%s", plist)
	}
	off := false
	first, err := GeneratePlist(PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin", CmuxClaudeCodeFix: &off})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "<string>--cmux-claude-code-fix=false</string>") {
		t.Fatalf("an explicit off must be emitted:\n%s", first)
	}
	path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	params, err := PreserveTuning(path, PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePlist(params)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("round trip changed the plist:\n--- first\n%s\n--- second\n%s", first, second)
	}
}
