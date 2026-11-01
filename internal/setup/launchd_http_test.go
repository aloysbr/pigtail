package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- --http ------------------------------------------------------------------
//
// The template always wrote `--http ""`, and --http sat in nonTuningFlags, so
// it was read and thrown away. The live plist asks for 127.0.0.1:7800 (the
// pigtaild default, kept explicit): the next `pigtail setup` would have
// switched the HTTP API off. These tests pin the fix, and pin that a carried
// value can never widen the listen address beyond loopback.

func newParams() PlistParams {
	return PlistParams{PigtaildPath: "/p/pigtaild", ConfigDir: "/c", Path: "/bin"}
}

// The loss, as it was: a plist asking for the HTTP API came back without it.
func TestPreserveTuningKeepsHandSetHTTP(t *testing.T) {
	for _, spelling := range [][]string{
		{"--http", "127.0.0.1:7800"},
		{"--http=127.0.0.1:7800"},
		{"-http=127.0.0.1:7800"},
		{"-http", "127.0.0.1:7800"},
	} {
		path := writePlistArgs(t, append([]string{"--socket", "/c/pigtail.sock"}, spelling...)...)
		params, err := PreserveTuning(path, newParams())
		if err != nil {
			t.Fatalf("%v: %v", spelling, err)
		}
		plist, err := GeneratePlist(params)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plist, "<string>--http</string>\n    <string>127.0.0.1:7800</string>") {
			t.Errorf("%v: setup dropped the hand-set HTTP address:\n%s", spelling, plist)
		}
	}
}

func TestGeneratePlistHTTPDefaultUnchanged(t *testing.T) {
	plist, err := GeneratePlist(newParams())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plist, "<string>--http</string>\n    <string></string>") {
		t.Fatalf("without an existing plist, --http must stay empty as before:\n%s", plist)
	}
}

// An explicit empty value (API off) is a choice and stays empty.
func TestReadPlistTuning_emptyHTTPStaysEmpty(t *testing.T) {
	got, err := ReadPlistTuning(writePlistArgs(t, "--http", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTP == nil || *got.HTTP != "" {
		t.Fatalf("HTTP = %v, want explicit empty", got.HTTP)
	}
}

func TestPreserveTuningRoundTripsHTTP(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7800", "localhost:7801", "[::1]:7802", ""} {
		a := addr
		p := newParams()
		p.HTTP = &a
		first, err := GeneratePlist(p)
		if err != nil {
			t.Fatalf("%q: %v", addr, err)
		}
		path := filepath.Join(t.TempDir(), "sh.pigtail.daemon.plist")
		if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
			t.Fatal(err)
		}
		params, err := PreserveTuning(path, newParams())
		if err != nil {
			t.Fatalf("%q: %v", addr, err)
		}
		second, err := GeneratePlist(params)
		if err != nil {
			t.Fatal(err)
		}
		if second != first {
			t.Fatalf("%q: round trip changed the plist:\n--- first\n%s\n--- second\n%s", addr, first, second)
		}
	}
}

// Never widen the listen address: a non-loopback value is refused, both
// when read back and when asked of GeneratePlist directly.
func TestHTTPRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{":7800", "0.0.0.0:7800", "192.168.1.10:7800", "[::]:7800", "example.com:7800", "127.0.0.1"} {
		_, err := ReadPlistTuning(writePlistArgs(t, "--http", addr))
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("read %q: want a loopback refusal, got %v", addr, err)
		}
		a := addr
		p := newParams()
		p.HTTP = &a
		if _, err := GeneratePlist(p); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("generate %q: want a loopback refusal, got %v", addr, err)
		}
	}
}
