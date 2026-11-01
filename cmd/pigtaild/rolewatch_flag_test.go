package main

import (
	"flag"
	"testing"
)

func TestRoleWatchFlagDefaultOn(t *testing.T) {
	for _, c := range []struct {
		args     []string
		disabled bool
	}{{nil, false}, {[]string{"-role-watch=false"}, true}, {[]string{"-role-watch"}, false}} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		f := registerFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		cfg, err := f.config()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RoleWatchDisabled != c.disabled {
			t.Errorf("%v: RoleWatchDisabled = %v, want %v", c.args, cfg.RoleWatchDisabled, c.disabled)
		}
	}
	if code, out, errOut := checkArgsOut([]string{"-role-watch=false"}); code != 0 {
		t.Fatalf("check-args = %d %q %q", code, out, errOut)
	}
}
