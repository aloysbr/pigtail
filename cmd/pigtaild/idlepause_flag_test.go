package main

import (
	"flag"
	"strings"
	"testing"
)

// sub_x000000000000036 (T-b): -idle-session-pause-seconds, 3600 by default,
// 0 = off; the old -idle-session-close-seconds is read as an alias, and the
// two set to different values refuse to start.
func TestIdleSessionPauseFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
		err  bool
	}{
		{nil, 3600, false},
		{[]string{"-idle-session-pause-seconds=0"}, 0, false},
		{[]string{"--idle-session-pause-seconds", "5400"}, 5400, false},
		{[]string{"-idle-session-close-seconds=7200"}, 7200, false},
		{[]string{"-idle-session-close-seconds=7200", "-idle-session-pause-seconds=7200"}, 7200, false},
		{[]string{"-idle-session-close-seconds=0", "-idle-session-pause-seconds=3600"}, 0, true},
	} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		f := registerFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		cfg, err := f.config()
		if c.err {
			if err == nil || !strings.Contains(err.Error(), "idle-session-pause-seconds") || !strings.Contains(err.Error(), "idle-session-close-seconds") {
				t.Errorf("%v: err = %v, want one naming both flags", c.args, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if cfg.IdleSessionPauseSeconds != c.want {
			t.Errorf("%v: IdleSessionPauseSeconds = %d, want %d", c.args, cfg.IdleSessionPauseSeconds, c.want)
		}
	}
}
