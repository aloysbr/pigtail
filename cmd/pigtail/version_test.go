package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/buildinfo"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func runVersion(t *testing.T, fetch func() (pigtail.VersionInfo, error), args ...string) (string, string, error) {
	t.Helper()
	cmd := versionCmdWith(fetch)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// A1: `pigtail version` prints the client's build identity, not only its
// version.
func TestVersionCmdPrintsBuildInfo(t *testing.T) {
	ob, oc, od, ov := buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty, Version
	t.Cleanup(func() { buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty, Version = ob, oc, od, ov })
	noFetch := func() (pigtail.VersionInfo, error) {
		t.Fatal("version without --daemon called the daemon")
		return pigtail.VersionInfo{}, nil
	}

	buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty, Version = "feat/x", "abc123", "true", "v9"
	out, _, err := runVersion(t, noFetch)
	if err != nil || out != "pigtail v9 branch=feat/x commit=abc123 dirty=true\n" {
		t.Fatalf("version = %q, %v", out, err)
	}
	buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty = "", "", ""
	out, _, err = runVersion(t, noFetch)
	if err != nil || out != "pigtail v9 branch=unknown commit=unknown dirty=unknown\n" {
		t.Fatalf("unstamped version = %q, %v", out, err)
	}
}

// A2: `pigtail version --daemon` prints the daemon's line from /version; an
// unreachable daemon exits 1 with the cause on stderr.
func TestVersionCmdDaemonFlag(t *testing.T) {
	t.Parallel()
	ok := func() (pigtail.VersionInfo, error) {
		return pigtail.VersionInfo{DaemonVersion: "v1", DaemonBranch: "main", DaemonCommit: "abc", DaemonDirty: "false"}, nil
	}
	out, _, err := runVersion(t, ok, "--daemon")
	if err != nil || out != "pigtaild v1 branch=main commit=abc dirty=false\n" {
		t.Fatalf("version --daemon = %q, %v", out, err)
	}

	old := func() (pigtail.VersionInfo, error) { // a daemon older than the stamp
		return pigtail.VersionInfo{DaemonVersion: "v0.2.0-3-gabc1234"}, nil
	}
	out, _, err = runVersion(t, old, "--daemon")
	if err != nil || out != "pigtaild v0.2.0-3-gabc1234 branch=unknown commit=unknown dirty=unknown\n" {
		t.Fatalf("version --daemon (old daemon) = %q, %v", out, err)
	}

	down := func() (pigtail.VersionInfo, error) {
		return pigtail.VersionInfo{}, errors.New("dial unix /x/p.sock: connect: no such file or directory")
	}
	out, errOut, err := runVersion(t, down, "--daemon")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("unreachable daemon: err = %v (exit %d), want exit 1", err, exitCode(err))
	}
	if out != "" || !strings.Contains(errOut, "no such file or directory") {
		t.Fatalf("unreachable daemon: stdout %q, stderr %q", out, errOut)
	}
}
