package main

import (
	"flag"
	"testing"
)

func TestAdmitDiskFloorFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{nil, 20},
		{[]string{"-admit-disk-floor-gb=0"}, 0},
		{[]string{"-admit-disk-floor-gb=50"}, 50},
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
		if cfg.AdmitDiskFloorGB != c.want {
			t.Errorf("%v: AdmitDiskFloorGB = %d, want %d", c.args, cfg.AdmitDiskFloorGB, c.want)
		}
	}
}

func TestDiskAlertFlag(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{nil, 40},
		{[]string{"-disk-alert-gb=0"}, -1},
		{[]string{"-disk-alert-gb=-1"}, -1},
		{[]string{"-disk-alert-gb=60"}, 60},
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
		if cfg.DiskAlertGB != c.want {
			t.Errorf("%v: DiskAlertGB = %d, want %d", c.args, cfg.DiskAlertGB, c.want)
		}
	}
}
