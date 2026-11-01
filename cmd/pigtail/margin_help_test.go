package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeusage"
)

// TestHelpCitesNoFixedMargins: the margin is each account's ceiling
// (select_pct), never a fixed 90/95 written in a help text.
func TestHelpCitesNoFixedMargins(t *testing.T) {
	root := newRootCmd()
	var walk func(c *cobra.Command)
	sawPick := false
	walk = func(c *cobra.Command) {
		text := c.Short + "\n" + c.Long
		for _, bad := range []string{"95%", "< 90%", ">= 90%"} {
			if strings.Contains(text, bad) {
				t.Errorf("%q help cites the fixed margin %q", c.CommandPath(), bad)
			}
		}
		if c.CommandPath() == "pigtail usage pick" {
			sawPick = true
			if !strings.Contains(c.Long, claudeusage.CeilingHelp) {
				t.Errorf("usage pick help does not cite CeilingHelp:\n%s", c.Long)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	if !sawPick {
		t.Fatal("pigtail usage pick not found in the command tree")
	}
}

func TestCeilingHelpMatchesDefault(t *testing.T) {
	t.Parallel()
	want := fmt.Sprintf("default %d%%", int(claudeusage.DefaultSelectPct))
	if !strings.Contains(claudeusage.CeilingHelp, want) {
		t.Fatalf("CeilingHelp %q does not say %q", claudeusage.CeilingHelp, want)
	}
}
