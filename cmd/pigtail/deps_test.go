package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The pigtail CLI is launched by hooks thousands of times a day; the SQLite
// driver's init alone cost ~5,6 ms of each launch (plan
// 2026-10-28-cli-startup-modernc). Only pigtaild may link it; `pigtail db`
// reaches it through `pigtaild -db-tool`.
func TestCLIDoesNotLinkTheSQLiteDriver(t *testing.T) {
	for _, goos := range []string{"", "linux"} {
		c := exec.Command("go", "list", "-deps", ".") // cwd = cmd/pigtail
		c.Env = os.Environ()
		if goos != "" {
			c.Env = append(c.Env, "GOOS="+goos)
		}
		out, err := c.Output()
		if err != nil {
			t.Fatalf("go list (GOOS=%q): %v", goos, err)
		}
		var bad []string
		sawSelf := false // positive control: the instrument read the package list
		for _, p := range strings.Fields(string(out)) {
			if p == "github.com/aloysbr/pigtail/cmd/pigtail" {
				sawSelf = true
			}
			if strings.HasPrefix(p, "modernc.org/") ||
				p == "github.com/aloysbr/pigtail/internal/daemon/store" ||
				p == "github.com/aloysbr/pigtail/internal/dbtool" {
				bad = append(bad, p)
			}
		}
		if !sawSelf {
			t.Fatalf("go list (GOOS=%q) did not list github.com/aloysbr/pigtail/cmd/pigtail: the test read nothing", goos)
		}
		if len(bad) > 0 {
			t.Fatalf("cmd/pigtail (GOOS=%q) links %d forbidden packages, first: %v", goos, len(bad), bad[:min(5, len(bad))])
		}
	}
}
