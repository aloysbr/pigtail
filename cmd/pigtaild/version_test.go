package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// A3: pigtaild -h names the one-shot modes, which flag.Parse never sees.
func TestUsageCitesBothModes(t *testing.T) {
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	fs.Bool("demo", false, "a flag")
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	usage(fs)()
	out := buf.String()
	for _, want := range []string{"-version", "-check-args", "-db-tool", "-demo"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}
}

// A1: -version (or --version) first prints the build line and exits 0;
// anything else is left to the real start.
func TestVersionModePrintsLine(t *testing.T) {
	for _, arg := range []string{"-version", "--version"} {
		var out bytes.Buffer
		handled, code := versionMode([]string{arg}, &out)
		if !handled || code != 0 {
			t.Fatalf("versionMode(%s) = (%v, %d), want (true, 0)", arg, handled, code)
		}
		if !strings.HasPrefix(out.String(), "pigtaild ") || !strings.Contains(out.String(), "commit=") {
			t.Fatalf("versionMode(%s) printed %q", arg, out.String())
		}
	}
	var out bytes.Buffer
	if handled, code := versionMode([]string{"-socket", "x"}, &out); handled || code != 0 {
		t.Fatalf("versionMode(-socket x) = (%v, %d), want (false, 0)", handled, code)
	}
	if handled, _ := versionMode(nil, &out); handled {
		t.Fatal("versionMode(no args) handled")
	}
	if out.Len() != 0 {
		t.Fatalf("an unhandled call printed %q", out.String())
	}
}
