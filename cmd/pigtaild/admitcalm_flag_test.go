package main

import (
	"flag"
	"testing"
	"time"
)

func TestAdmitCompressCalmFlags(t *testing.T) {
	for _, c := range []struct {
		args           []string
		window, starve time.Duration
	}{
		{nil, time.Minute, 0},
		{[]string{"-admit-compress-calm-seconds=0"}, -time.Second, 0},
		{[]string{"-admit-compress-calm-seconds=90", "-admit-compress-calm-max-starve-seconds=600"}, 90 * time.Second, 10 * time.Minute},
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
		if cfg.AdmitCompressCalmWindow != c.window || cfg.AdmitCompressCalmMaxStarve != c.starve {
			t.Errorf("%v: window %v starve %v, want %v %v", c.args, cfg.AdmitCompressCalmWindow, cfg.AdmitCompressCalmMaxStarve, c.window, c.starve)
		}
	}
}
