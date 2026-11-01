package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/usersettings"
	"github.com/spf13/cobra"
)

var citedRE = regexp.MustCompile("`pigtail ([^`]*)`")

// citedCommands returns each `pigtail …` span of text, as words.
func citedCommands(text string) [][]string {
	var out [][]string
	for _, m := range citedRE.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.Fields(m[1]))
	}
	return out
}

// checkCitedCommand resolves a cited command in the cobra tree: the longest
// run of leading words that are subcommands, and every --flag cited on it.
func checkCitedCommand(t *testing.T, root *cobra.Command, words []string) {
	t.Helper()
	checkCitedCommandFn(t, root, words)
}

type errorfer interface{ Errorf(string, ...any) }

type fakeT struct{ failed bool }

func (f *fakeT) Errorf(string, ...any) { f.failed = true }

func checkCitedCommandFn(t errorfer, root *cobra.Command, words []string) {
	cur := root
	var path []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "{") || strings.HasPrefix(w, "\"") || strings.HasPrefix(w, "[") {
			break
		}
		next := findSub(cur, w)
		if next == nil {
			break
		}
		cur, path = next, append(path, w)
	}
	if len(path) == 0 {
		t.Errorf("cited command %q: no such command", strings.Join(words, " "))
		return
	}
	// `pigtail session wait <type>` etc.: a leftover plain word must be a positional, never a missing subcommand.
	if cur.HasSubCommands() && cur.Runnable() == false {
		t.Errorf("cited command %q stops at a group (%s)", strings.Join(words, " "), strings.Join(path, " "))
	}
	for _, w := range words {
		if !strings.HasPrefix(w, "--") {
			continue
		}
		name := strings.TrimPrefix(strings.SplitN(w, "=", 2)[0], "--")
		if cur.Flags().Lookup(name) == nil && cur.InheritedFlags().Lookup(name) == nil && root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("cited command %q: flag --%s not on %q", strings.Join(words, " "), name, strings.Join(path, " "))
		}
	}
}

func findSub(c *cobra.Command, name string) *cobra.Command {
	for _, s := range c.Commands() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func TestDefaultPromptCommandsExist(t *testing.T) {
	cmds := citedCommands(usersettings.DefaultPromptTemplate)
	if len(cmds) < 3 {
		t.Fatalf("default prompt cites %d commands, want at least 3", len(cmds))
	}
	root := newRootCmd()
	for _, words := range cmds {
		checkCitedCommand(t, root, words)
	}
}

func TestCitedCommandCheckerRejectsUnknown(t *testing.T) {
	root := newRootCmd()
	for _, bad := range []string{"nope thing", "subject output add --nosuchflag x", "subject output"} {
		sub := &fakeT{}
		checkCitedCommandFn(sub, root, strings.Fields(bad))
		if !sub.failed {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
