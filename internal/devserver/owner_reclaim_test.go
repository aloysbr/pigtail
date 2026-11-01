//go:build unix

package devserver

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestStopAllReclaimsDeadSession is the end-to-end property this lot exists
// for: a server claimed by a session that no longer exists is stopped, and
// said so, while a live session's server next door is spared.
//
// This test stubs hostname and pidAlive — package vars declared only in
// internal/devserver/session.go, build tag unix; session_other.go's !unix
// build has no such vars to stub. That is the actual reason this lives in
// its own //go:build unix file rather than in owner_test.go: referencing
// hostname/pidAlive from an untagged file would fail to compile on !unix.
// (owner_test.go itself is not a portability model to follow here — it
// already uses syscall.SysProcAttr{Setpgid: true}, which has no Windows
// equivalent, despite carrying no build tag of its own.)
func TestStopAllReclaimsDeadSession(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	portDead := freeTestPort(t)
	portLive := freeTestPort(t)
	dirDead := mkFakeWorktreeDir(t, container, "wt-dead", portDead)
	dirLive := mkFakeWorktreeDir(t, container, "wt-live", portLive)

	if err := writeOwner(dirDead, "dev", "sid:9999@laptop"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(dirLive, "dev", "sid:4711@laptop"); err != nil {
		t.Fatal(err)
	}

	origHostname, origPidAlive := hostname, pidAlive
	defer func() { hostname, pidAlive = origHostname, origPidAlive }()
	hostname = func() (string, error) { return "laptop", nil }
	pidAlive = func(pid int) bool { return pid == 4711 }

	spawnPortListener(t, portDead, dirDead)
	spawnPortListener(t, portLive, dirLive)
	waitPortBusyForTest(t, portDead)
	waitPortBusyForTest(t, portLive)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-mine")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		busy, _, _ := IsPortBusy(portDead)
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a server whose session is dead must be reclaimed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if busy, _, _ := IsPortBusy(portLive); !busy {
		t.Fatal("a live session's server must be spared")
	}
	if !strings.Contains(buf.String(), "sid:9999@laptop") {
		t.Fatalf("reclaiming must be announced, got:\n%s", buf.String())
	}
}

// TestStopAllEverywhereAnnouncesLiveSeizure is finding 2 of the 2026-07-27
// final review: under ScopeEverywhere, a server claimed by ANOTHER LIVE
// session must be announced just as loudly as a dead-owner reclaim — more
// so, since taking a live session's server is the more consequential kill.
// Before the fix, triageScope only ever populated `reclaimed` for a dead
// owner, so a live seizure under --everywhere printed nothing at all: the
// output read as though dead owners were the only thing --everywhere took.
func TestStopAllEverywhereAnnouncesLiveSeizure(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	portLive := freeTestPort(t)
	dirLive := mkFakeWorktreeDir(t, container, "wt-live", portLive)

	if err := writeOwner(dirLive, "dev", "sid:4711@laptop"); err != nil {
		t.Fatal(err)
	}

	origHostname, origPidAlive := hostname, pidAlive
	defer func() { hostname, pidAlive = origHostname, origPidAlive }()
	hostname = func() (string, error) { return "laptop", nil }
	pidAlive = func(pid int) bool { return pid == 4711 } // alive: it is a live session

	spawnPortListener(t, portLive, dirLive)
	waitPortBusyForTest(t, portLive)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-mine")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeEverywhere); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		busy, _, _ := IsPortBusy(portLive)
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("--everywhere must stop even a live session's server")
		}
		time.Sleep(50 * time.Millisecond)
	}

	report := buf.String()
	if !strings.Contains(report, "sid:4711@laptop") {
		t.Fatalf("taking a live session's server must name its owner, got:\n%s", report)
	}
	if strings.Contains(report, "session gone") {
		t.Fatalf("a live seizure must not be worded like a dead-owner reclaim, got:\n%s", report)
	}
}
