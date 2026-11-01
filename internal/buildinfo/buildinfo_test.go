package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setFields(t *testing.T, b, c, d string) {
	t.Helper()
	ob, oc, od := Branch, Commit, Dirty
	Branch, Commit, Dirty = b, c, d
	t.Cleanup(func() { Branch, Commit, Dirty = ob, oc, od })
}

// A1: an unstamped build says unknown, never an empty field nor dirty=true;
// a detached HEAD (rev-parse --abbrev-ref answers HEAD) reads detached.
func TestLineRendersUnknownAndDetached(t *testing.T) {
	setFields(t, "", "", "")
	if got, want := Line("pigtaild", "dev"), "pigtaild dev branch=unknown commit=unknown dirty=unknown"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
	setFields(t, "HEAD", "0123456789abcdef0123456789abcdef01234567", "false")
	if got, want := Line("pigtail", "v1"), "pigtail v1 branch=detached commit=0123456789abcdef0123456789abcdef01234567 dirty=false"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
	setFields(t, "main", "abc", "true")
	if got, want := Line("pigtail", ""), "pigtail unknown branch=main commit=abc dirty=true"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
}

// Both binaries get the stamp: the three -X live in LDFLAGS_CLIENT, which
// LDFLAGS_DAEMON extends.
func TestMakefileStampsBuildinfo(t *testing.T) {
	b, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	var client string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "LDFLAGS_CLIENT") {
			client = l
		}
	}
	if client == "" {
		t.Fatal("no LDFLAGS_CLIENT line in the Makefile")
	}
	for _, f := range []string{"Branch", "Commit", "Dirty"} {
		want := "-X github.com/aloysbr/pigtail/internal/buildinfo." + f + "="
		if !strings.Contains(client, want) {
			t.Errorf("LDFLAGS_CLIENT lacks %q: %s", want, client)
		}
	}
	if !strings.Contains(string(b), "LDFLAGS_DAEMON := $(LDFLAGS_CLIENT)") {
		t.Error("LDFLAGS_DAEMON no longer extends LDFLAGS_CLIENT")
	}
}

// A1: a build outside git (a copy of the sources without .git) leaves the
// three fields empty — unknown once printed — and never DIRTY=true.
func TestMakefileOutsideGitLeavesFieldsUnknown(t *testing.T) {
	for _, tool := range []string{"make", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	b, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "Makefile"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", "-s", "-C", tmp, "print-buildinfo")
	// A minimal environment: no GIT_DIR/GIT_WORK_TREE inherited from a hook,
	// no BRANCH/COMMIT/DIRTY nor MAKEFLAGS from a parent make (make check).
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + tmp,
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(tmp),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make print-buildinfo: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "BRANCH= COMMIT= DIRTY=" {
		t.Fatalf("outside git: %q, want %q", got, "BRANCH= COMMIT= DIRTY=")
	}
}
