package main

import (
	"flag"
	"strings"
	"testing"
)

// sub_x000000000000030 (D5): --cmux-folder-sort, on by default. The helper
// returns the value main() puts in daemon.Config.CmuxFolderSortDisabled as
// is: the inversion of the flag is tested here, not left in main().
func TestCmuxFolderSortFlag(t *testing.T) {
	for _, c := range []struct {
		args         []string
		wantDisabled bool
	}{
		{nil, false}, // absent: sorted, pigtaild's default
		{[]string{"-cmux-folder-sort=false"}, true},
		{[]string{"--cmux-folder-sort=true"}, false},
		{[]string{"--cmux-folder-sort"}, false},
	} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		disabled := cmuxFolderSortDisabledFlag(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if got := disabled(); got != c.wantDisabled {
			t.Errorf("%v: CmuxFolderSortDisabled = %v, want %v", c.args, got, c.wantDisabled)
		}
	}
}

// The help says what =false does: files without sorting, a hand order stays,
// pigtail's own adds go to the bottom (D5-A, D11) — not « every session at
// the bottom ».
func TestCmuxFolderSortFlagHelp(t *testing.T) {
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	cmuxFolderSortDisabledFlag(fs)
	usage := fs.Lookup("cmux-folder-sort").Usage
	for _, want := range []string{"without sorting", "an order set by hand stays", "pigtail's own adds go to the bottom"} {
		if !strings.Contains(usage, want) {
			t.Errorf("help lacks %q: %s", want, usage)
		}
	}
	if strings.Contains(usage, "files each session at the bottom") {
		t.Errorf("help still says every session goes to the bottom: %s", usage)
	}
}
