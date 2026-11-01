package main

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOrphanReaperFlags(t *testing.T) {
	for _, c := range []struct {
		args  []string
		mode  string
		after time.Duration
	}{
		{nil, "on", 15 * time.Minute},
		{[]string{"-orphan-reaper=off"}, "off", 15 * time.Minute},
		{[]string{"-orphan-reaper=observe", "-orphan-reap-after=20m"}, "observe", 20 * time.Minute},
	} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		f := registerFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		cfg, err := f.config()
		if err != nil {
			t.Fatal(err)
		}
		if string(cfg.OrphanReaper) != c.mode || cfg.OrphanReapAfter != c.after {
			t.Errorf("%v: mode %q after %v, want %q %v", c.args, cfg.OrphanReaper, cfg.OrphanReapAfter, c.mode, c.after)
		}
	}
	for _, args := range [][]string{
		{"-orphan-reaper=yes"},
		{"-orphan-reap-after=0s"},
		{"-orphan-reap-after=-1m"},
	} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		f := registerFlags(fs)
		if err := fs.Parse(args); err != nil {
			continue // refused at the parse: fine
		}
		if _, err := f.config(); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

// TestLivePassTurnsOrphanReaperOff: the live-pass daemon is the real binary,
// whose -orphan-reaper defaults to "on"; the script must turn it off so a
// pass never reaps the process trees of Ada' own agent shells.
func TestLivePassTurnsOrphanReaperOff(t *testing.T) {
	b, err := os.ReadFile("../../scripts/live-pass.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "args=(-socket")
	if i < 0 {
		t.Fatal("no args=(-socket … block in live-pass.sh")
	}
	block := s[i:]
	if j := strings.Index(block, ")\n"); j >= 0 {
		block = block[:j]
	}
	if !strings.Contains(block, "-orphan-reaper=off") {
		t.Fatalf("live-pass.sh daemon args do not pass -orphan-reaper=off:\n%s", block)
	}
}
