package frontapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRun answers lsappinfo calls from a table, never the real binary.
func fakeRun(t *testing.T, answers map[string]string) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != lsappinfo {
			t.Fatalf("ran %q, want the absolute %s", name, lsappinfo)
		}
		key := strings.Join(args, " ")
		out, ok := answers[key]
		if !ok {
			t.Fatalf("unexpected call %q", key)
		}
		return []byte(out), nil
	}
}

func stubDarwin(t *testing.T) {
	t.Helper()
	saved := goos
	goos = "darwin"
	t.Cleanup(func() { goos = saved })
}

func stubRun(t *testing.T, r func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	t.Helper()
	saved := run
	run = r
	t.Cleanup(func() { run = saved })
}

// Real outputs (measured 2026-10-20): `lsappinfo front` then
// `lsappinfo info -only bundleid <ASN>`.
func TestFrontAppParsesBundleID(t *testing.T) {
	stubDarwin(t)
	stubRun(t, fakeRun(t, map[string]string{
		"front":                                  "ASN:0x0-0x793b934:\n",
		"info -only bundleid ASN:0x0-0x793b934:": `"CFBundleIdentifier"="com.cmuxterm.app"` + "\n",
	}))
	got, err := BundleID(context.Background())
	if err != nil || got != "com.cmuxterm.app" {
		t.Fatalf("BundleID = %q, %v", got, err)
	}
	if b, err := ParseBundleID(`"CFBundleIdentifier"="com.apple.Safari"`); err != nil || b != "com.apple.Safari" {
		t.Fatalf("ParseBundleID = %q, %v", b, err)
	}
}

func TestFrontAppEmptyOutputIsError(t *testing.T) {
	stubDarwin(t)
	for _, cs := range []map[string]string{
		{"front": ""},
		{"front": "[ NULL ]\n"},
		{"front": "ASN:0x0-0x1:\n", "info -only bundleid ASN:0x0-0x1:": ""},
		{"front": "ASN:0x0-0x1:\n", "info -only bundleid ASN:0x0-0x1:": "[ NULL ]\n"},
		{"front": "ASN:0x0-0x1:\n", "info -only bundleid ASN:0x0-0x1:": `"CFBundleIdentifier"=""`},
	} {
		stubRun(t, fakeRun(t, cs))
		if got, err := BundleID(context.Background()); err == nil {
			t.Fatalf("%v: BundleID = %q, want an error", cs, got)
		}
	}
}

func TestFrontAppTimeoutIsError(t *testing.T) {
	stubDarwin(t)
	saved := callTimeout
	callTimeout = 20 * time.Millisecond
	t.Cleanup(func() { callTimeout = saved })
	stubRun(t, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	_, err := BundleID(context.Background())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the call was not bounded")
	}
}

func TestFrontAppNonDarwinIsError(t *testing.T) {
	saved := goos
	goos = "linux"
	t.Cleanup(func() { goos = saved })
	stubRun(t, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("lsappinfo ran off darwin")
		return nil, nil
	})
	if _, err := BundleID(context.Background()); err == nil {
		t.Fatal("want an error off darwin")
	}
}
