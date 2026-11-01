package main

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// -usage-state-dir: empty → the spawn registry's state dir (where
// usage-refresh.json always lived); set → its value. The daemon package never
// derives it itself, so a test Server reaches no real state.
func TestUsageStateDirDefault(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, filepath.Dir(spawnreg.DefaultPath())},
		{[]string{"-usage-state-dir=/tmp/pgt-usage"}, "/tmp/pgt-usage"},
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
		if cfg.UsageStateDir != c.want {
			t.Errorf("%v: UsageStateDir = %q, want %q", c.args, cfg.UsageStateDir, c.want)
		}
	}
}
