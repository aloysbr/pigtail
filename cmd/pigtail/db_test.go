package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The command must refuse while the daemon holds the database open, before
// anything reaches the db tool.
func TestDBRestoreRefusesWhileTheDaemonAnswers(t *testing.T) {
	origUp, origRun := daemonIsUp, runDBTool
	daemonIsUp = func() bool { return true }
	runDBTool = func(*cobra.Command, []string) error { t.Fatal("the db tool ran while the daemon was up"); return nil }
	t.Cleanup(func() { daemonIsUp, runDBTool = origUp, origRun })

	cmd := dbRestoreCmd()
	cmd.SetArgs([]string{"--yes"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "launchctl bootout") {
		t.Fatalf("the refusal must name the stop command, got: %v", err)
	}
}

func TestDBToolArgs(t *testing.T) {
	got := strings.Join(dbToolArgs("restore", "v", "/x/p.db", "-generation", "1", "-yes"), " ")
	if want := "restore -view v -db /x/p.db -generation 1 -yes"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := strings.Join(dbToolArgs("list", "v", ""), " "); got != "list -view v" {
		t.Fatalf("no --db must pass no -db, got %q", got)
	}
}
