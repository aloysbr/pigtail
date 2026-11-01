package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/store"
)

func TestDBCommandsWorkThroughPigtaildWithTheDaemonStopped(t *testing.T) {
	bins := t.TempDir()
	for _, b := range []struct{ out, pkg string }{{"pigtail", "."}, {"pigtaild", "../pigtaild"}} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bins, b.out), b.pkg).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", b.pkg, err, out)
		}
	}
	home := t.TempDir()
	db := filepath.Join(home, "pigtail.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BackupRotated(store.BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteOffsiteState(db, store.OffsiteState{DestDir: filepath.Join(home, "offsite")}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (stdout, stderr string, code int) {
		c := exec.Command(filepath.Join(bins, "pigtail"), args...)
		c.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "cfg"),
			"PATH=/usr/bin:/bin", "PIGTAIL_SOCKET=" + filepath.Join(home, "no-daemon.sock")}
		var o, e bytes.Buffer
		c.Stdout, c.Stderr = &o, &e
		c.Stdin = strings.NewReader("") // else the child gets /dev/null, a char device that isStdinTTY (cmd/pigtail/dev.go:373, os.ModeCharDevice) takes for a TTY
		err := c.Run()
		code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return o.String(), e.String(), code
	}

	if out, errOut, code := run("db", "list", "--db", db); code != 0 || !strings.Contains(out, "GENERATION") || !strings.Contains(out, "live") || !strings.Contains(out, "offsite:") {
		t.Fatalf("db list: code %d\n%s\n%s", code, out, errOut)
	}
	before := e2eDirNames(t, home)
	if out, errOut, code := run("db", "restore", "--generation", "0", "--dry-run", "--yes", "--db", db); code != 0 || !strings.Contains(out, "(dry run — no changes made)") {
		t.Fatalf("dry run: code %d\n%s\n%s", code, out, errOut)
	}
	if _, errOut, code := run("db", "restore", "--generation", "0", "--db", db); code != 1 || strings.Count(errOut, "Error:") != 1 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("no TTY, no --yes: code %d stderr %q", code, errOut)
	}
	if after := e2eDirNames(t, home); after != before {
		t.Fatalf("a dry run or a refusal modified the directory:\nbefore %s\nafter  %s", before, after)
	}
	if out, errOut, code := run("db", "restore", "--generation", "0", "--yes", "--db", db); code != 0 || !strings.Contains(out, "✓ restored") {
		t.Fatalf("restore: code %d\n%s\n%s", code, out, errOut)
	}
	if m, _ := filepath.Glob(db + ".pre-restore-*"); len(m) == 0 {
		t.Fatal("the previous database was not kept aside")
	}
}

func e2eDirNames(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
