package cmux

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSendTextArgsRaw(t *testing.T) {
	got := sendTextArgs("UUID-1", "\x15")
	if !reflect.DeepEqual(got, []string{"send", "--surface", "UUID-1", "\x15"}) {
		t.Fatalf("args = %q (no `\\n` appended: SendLine does that)", got)
	}
}

func TestPingNeedsCLI(t *testing.T) {
	t.Setenv(EnvBundledCLI, "")
	t.Setenv("PATH", t.TempDir())
	if err := Ping(); err == nil {
		t.Fatal("no cmux CLI must be an error")
	}
}

// pigtail cmux check: a cmux that takes the call and never answers must not
// hold the check (and its lock) forever — every call it makes is bounded:
// send and ping by the write bound, read-screen by the listing bound.
func TestCheckCallsAreBounded(t *testing.T) {
	// One process that takes the call and never answers, as the cmux binary
	// is (exec: no shell child left holding the output pipe).
	p := filepath.Join(t.TempDir(), "cmux")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, p)
	defer SetCallTimeoutForTest(200 * time.Millisecond)()
	defer SetListingTimeoutForTest(300 * time.Millisecond)()
	for name, call := range map[string]func() error{
		"ping":        Ping,
		"send":        func() error { return SendText("C0FFEE00-0000-4000-8000-000000000001", "x") },
		"read-screen": func() error { _, err := ReadScreen("C0FFEE00-0000-4000-8000-000000000001"); return err },
	} {
		start := time.Now()
		err := call()
		if took := time.Since(start); err == nil || took > 2*time.Second {
			t.Errorf("%s: %v after %s; want an error within its bound", name, err, took)
		}
	}
}

// SendText validates its arguments as the sidebar does: the surface is a
// UUID, the text is never empty nor readable by cmux as a flag. Nothing runs
// on a refusal (the CLI path points at nothing runnable).
func TestSendTextValidates(t *testing.T) {
	t.Setenv(EnvBundledCLI, filepath.Join(t.TempDir(), "absent"))
	t.Setenv("PATH", t.TempDir())
	const ok = "C0FFEE00-0000-4000-8000-000000000001"
	for _, c := range [][2]string{{"--surface", "x"}, {"surface:1", "x"}, {"", "x"}, {ok, "--help"}, {ok, ""}} {
		if err := SendText(c[0], c[1]); !errors.Is(err, ErrInvalidArg) {
			t.Errorf("SendText(%q, %q) = %v; want ErrInvalidArg", c[0], c[1], err)
		}
	}
}
