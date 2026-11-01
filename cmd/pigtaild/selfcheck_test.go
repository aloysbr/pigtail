package main

import (
	"fmt"
	"testing"
)

// The line is logged verbatim when the platform has one, and nothing is
// logged otherwise (linux).
func TestLogFootprintSelfCheck(t *testing.T) {
	var got []string
	logf := func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }

	logFootprintSelfCheck(func() (string, bool) { return "sysmem: self footprint=36MiB rss=34MiB (footprint reader ok)", true }, logf)
	if len(got) != 1 || got[0] != "sysmem: self footprint=36MiB rss=34MiB (footprint reader ok)" {
		t.Fatalf("logged %q", got)
	}

	got = nil
	logFootprintSelfCheck(func() (string, bool) { return "", false }, logf)
	if len(got) != 0 {
		t.Fatalf("logged %q on a platform without a line", got)
	}
}
