package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// cliMarginSinks are the CLI's launch margin trace sinks (sub_x000000000000011):
// spawns.jsonl (its own margin entry) and the journal through journal; a
// failed trace is a stderr warning, never a block.
func cliMarginSinks(journal func(*pigtail.JournalEntry) error, errw io.Writer) launchmargin.Sinks {
	return launchmargin.Sinks{
		Spawns:  spawnreg.Open(spawnreg.DefaultPath()),
		Journal: journal,
		Logf:    func(f string, a ...any) { fmt.Fprintf(errw, "warning: "+f+"\n", a...) },
	}
}

// addMarginJournal writes a launch margin row through c, the command's own
// scoped client (built in its RunE: TestScopedCommandsDeclareAccess), Scope
// left empty: the caller's own scope (a foreign one would be refused, 403).
func addMarginJournal(c *pigtail.Client, e *pigtail.JournalEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.AddJournal(ctx, *e)
	return err
}

// marginViewer is the caller the guard bounds its texts for (D10): the scope
// resolveScope gives this invocation (its cell's, else the default scope —
// fail-closed), master when that scope sees all.
func marginViewer(tbl *claudeprofile.Table, getenv func(string) string) launchmargin.Viewer {
	sc, err := resolveScope(tbl, claudeConfigDir(getenv), "")
	if err != nil {
		return launchmargin.Viewer{}
	}
	return launchmargin.Viewer{Master: scope.SeesAll(scope.Scope(sc)), Scope: sc}
}

// marginJournal is the journal write of spawn and respawn (a seam: a test
// makes it fail to prove a failed trace never blocks a launch).
var marginJournal = addMarginJournal

// recordLaunchMargin traces the guard's decision of a real spawn or respawn
// (never a dry run): when the plan went through, or when the guard itself
// refused it (exit 3). A plan failing on another check traces nothing — the
// decision was never applied. c is the command's own scoped client.
func recordLaunchMargin(c *pigtail.Client, dec *launchmargin.Decision, subjectID string, planErr error, errw io.Writer) {
	if dec == nil || (planErr != nil && dec.Action != launchmargin.ActionRefused) {
		return
	}
	d := *dec
	d.SubjectID = subjectID
	launchmargin.Record(cliMarginSinks(func(e *pigtail.JournalEntry) error { return marginJournal(c, e) }, errw), d)
}

// printLaunchMargin prints the daemon's launch margin decision of a dispatch
// or a run (switched, forced, unmeasured) as a stderr warning.
func printLaunchMargin(errw io.Writer, m *pigtail.LaunchMargin) {
	if m != nil && m.Warning != "" {
		fmt.Fprintf(errw, "warning: %s\n", m.Warning)
	}
}

// launchMarginExit maps the daemon's LAUNCH_OVER_MARGIN refusal to exit 3,
// like spawn and usage pick; any other error passes unchanged.
func launchMarginExit(err error) error {
	var api *pigtail.APIError
	if errors.As(err, &api) && api.Code == pigtail.ErrLaunchOverMargin {
		return withExitCode(3, err)
	}
	return err
}
