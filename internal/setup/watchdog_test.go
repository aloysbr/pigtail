package setup

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ctxKey struct{}

func TestKickstartArgs(t *testing.T) {
	u, _ := user.Current()
	var rec []recorded
	var got context.Context
	run := func(ctx context.Context, name string, args ...string) error {
		got = ctx
		rec = append(rec, recorded{name, args})
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), ctxKey{}, 1), time.Minute)
	defer cancel()
	if err := Kickstart(ctx, run); err != nil {
		t.Fatal(err)
	}
	want := []recorded{{"launchctl", []string{"kickstart", "-k", "gui/" + u.Uid + "/sh.pigtail.daemon"}}}
	if !reflect.DeepEqual(rec, want) {
		t.Fatalf("got %v", rec)
	}
	if got.Value(ctxKey{}) != 1 {
		t.Fatal("ctx not passed through")
	}
}

func TestWatchdogPlist(t *testing.T) {
	p, err := GenerateWatchdogPlist("/u/.local/bin/pigtail", "/c")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"<string>sh.pigtail.watchdog</string>",
		"<key>StartInterval</key>\n  <integer>60</integer>",
		"<key>RunAtLoad</key>\n  <false/>",
		"<string>Background</string>",
		"<string>/u/.local/bin/pigtail</string>\n    <string>daemon</string>\n    <string>watchdog</string>\n    <string>--db</string>\n    <string>/c/pigtail.db</string>",
		"<string>/c/watchdog.log</string>",
	} {
		if !strings.Contains(p, w) {
			t.Errorf("plist lacks %q:\n%s", w, p)
		}
	}
}

func TestWatchdogLabelNotMethods(t *testing.T) {
	if WatchdogLabel() != "sh.pigtail.watchdog" || strings.Contains(WatchdogLabel(), "tools") || WatchdogLabel() == Label() {
		t.Fatalf("label = %q", WatchdogLabel())
	}
}

func TestResolvePigtailPathNotExecutable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pigtail")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := ResolvePigtailPath(); got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if got, want := ResolvePigtailPath(), filepath.Join(home, ".local", "bin", "pigtail"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInstallWatchdogStep(t *testing.T) {
	noSleep(t)
	t.Setenv("HOME", t.TempDir())
	var rec []recorded
	if err := InstallWatchdogStep(fakeRunner(&rec), "/b/pigtail", "/c"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(WatchdogPlistPath()); err != nil {
		t.Fatal(err)
	}
	if v := verbs(rec); !reflect.DeepEqual(v, []string{"bootout", "bootstrap"}) || bootstrapPlist(rec) != WatchdogPlistPath() {
		t.Fatalf("rec = %v", rec)
	}
}

func TestTeardownWatchdogStep(t *testing.T) {
	noSleep(t)
	t.Setenv("HOME", t.TempDir())
	var rec []recorded
	if err := InstallWatchdogStep(fakeRunner(&rec), "/b/pigtail", "/c"); err != nil {
		t.Fatal(err)
	}
	rec = nil
	if err := TeardownWatchdogStep(fakeRunner(&rec)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(WatchdogPlistPath()); !os.IsNotExist(err) {
		t.Fatalf("plist still there: %v", err)
	}
	if len(rec) != 1 || rec[0].args[0] != "bootout" {
		t.Fatalf("rec = %v", rec)
	}
	if err := TeardownWatchdogStep(fakeRunner(&rec)); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

// bootstrapPlist is the plist of the first recorded bootstrap.
func bootstrapPlist(rec []recorded) string {
	for _, r := range rec {
		if r.args[0] == "bootstrap" {
			return r.args[2]
		}
	}
	return ""
}
