package main

import (
	"flag"
	"testing"
)

// -session-sample-seconds: 30 by default, 0 = off, a negative value refuses.
func TestSessionSampleFlag(t *testing.T) {
	for _, c := range []struct {
		args    []string
		want    int
		wantErr bool
	}{
		{nil, 30, false},
		{[]string{"-session-sample-seconds=0"}, 0, false},
		{[]string{"--session-sample-seconds", "10"}, 10, false},
		{[]string{"-session-sample-seconds=-1"}, 0, true},
	} {
		fs := flag.NewFlagSet("pigtaild", flag.ContinueOnError)
		f := registerFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		cfg, err := f.config()
		if (err != nil) != c.wantErr {
			t.Fatalf("%v: err = %v", c.args, err)
		}
		if err == nil && cfg.SessionSampleSeconds != c.want {
			t.Errorf("%v: SessionSampleSeconds = %d, want %d", c.args, cfg.SessionSampleSeconds, c.want)
		}
	}
}
