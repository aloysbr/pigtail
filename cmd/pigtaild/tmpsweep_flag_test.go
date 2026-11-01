package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

func TestTmpSweepFlags(t *testing.T) {
	for _, c := range []struct {
		args []string
		mode string
	}{
		{nil, "on"},
		{[]string{"-tmp-sweep=off"}, "off"},
		{[]string{"-tmp-sweep=observe"}, "observe"},
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
		if string(cfg.TmpSweep) != c.mode {
			t.Errorf("%v: TmpSweep %q, want %q", c.args, cfg.TmpSweep, c.mode)
		}
	}
	fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
	f := registerFlags(fs)
	if err := fs.Parse([]string{"-tmp-sweep=yes"}); err == nil {
		if _, err := f.config(); err == nil {
			t.Error("-tmp-sweep=yes accepted")
		}
	}
}

// TestLivePassTurnsTmpSweepOff: the live-pass daemon is the real binary, whose
// -tmp-sweep defaults to "on"; the script must turn it off.
func TestLivePassTurnsTmpSweepOff(t *testing.T) {
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
	if !strings.Contains(block, "-tmp-sweep=off") {
		t.Fatalf("live-pass.sh daemon args do not pass -tmp-sweep=off:\n%s", block)
	}
}

// TestDaemonChildTurnsTmpSweepOff: startDaemonChild runs the real run() with
// the flags' defaults; a test daemon must never sweep the real $TMPDIR.
func TestDaemonChildTurnsTmpSweepOff(t *testing.T) {
	got := daemonChildArgs([]string{"-socket", "x"})
	if len(got) == 0 || got[0] != "-tmp-sweep=off" {
		t.Fatalf("daemonChildArgs = %v, want -tmp-sweep=off first", got)
	}
	if again := daemonChildArgs(got); strings.Count(strings.Join(again, " "), "-tmp-sweep=") != 1 {
		t.Errorf("flag duplicated: %v", again)
	}
}
