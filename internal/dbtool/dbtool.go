// Package dbtool is pigtaild's backup tool behind `pigtail db list|restore`: it reads the
// daemon's database backups and puts one back in place. It runs inside
// `pigtaild -db-tool`, never in the pigtail CLI binary, so the CLI does not
// link the SQLite driver for this one command.
//
// The caller (cmd/pigtail, `pigtail db`) has resolved the caller's scope view
// from its Claude cell and, for restore, established that no daemon answers;
// store.Restore states the same contract.
package dbtool

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
)

// Exit codes. Never 2: Go's flag package exits 2 on an unknown flag, which,
// with the text « flag provided but not defined », is how an older pigtaild
// answers -db-tool, and the CLI reads code 2 and that text that way.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 64
)

// IO is the process's terminal, injected so tests drive every path.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	IsTTY  func() bool
	Now    func() time.Time
}

// Run executes one db-tool command: args[0] is list or restore, the rest are
// its flags. It returns the process exit code; errors are printed as cobra
// printed them before the move ("Error: <msg>"), once.
func Run(args []string, o IO) int {
	if len(args) == 0 {
		return usageErr(o, errors.New("db-tool: want list or restore"))
	}
	switch args[0] {
	case "list":
		return runList(args[1:], o)
	case "restore":
		return runRestore(args[1:], o)
	}
	return usageErr(o, fmt.Errorf("db-tool: unknown command %q (want list or restore)", args[0]))
}

func usageErr(o IO, err error) int {
	fmt.Fprintf(o.Stderr, "Error: %v\n", err)
	return ExitUsage
}

func fail(o IO, err error) int {
	fmt.Fprintf(o.Stderr, "Error: %v\n", err)
	return ExitError
}

// parseView reads -view. Empty is refused, never defaulted: a missing view
// must not widen what a non-master cell is shown.
func parseView(v string) (scope.Scope, error) {
	if strings.TrimSpace(v) == "" {
		return "", errors.New("-view is required (the caller's scope; pigtail db passes it)")
	}
	return scope.Parse(v)
}

func newFlags(name string, o IO) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(o.Stderr)
	return fs
}

func runList(args []string, o IO) int {
	fs := newFlags("db-tool list", o)
	view := fs.String("view", "", "the caller's scope")
	dbPath := fs.String("db", "", "database path")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 0 {
		return usageErr(o, fmt.Errorf("db-tool list: unexpected argument %q", fs.Arg(0)))
	}
	v, err := parseView(*view)
	if err != nil {
		return usageErr(o, err)
	}
	resolved := ResolveDBPath(*dbPath)
	rows := store.Inspect(resolved, store.BackupGenerations)
	fmt.Fprint(o.Stdout, formatGenerations(rows, o.Now(), v))
	// Three healthy generations say a bad write cannot hurt you, and
	// nothing about losing the disk. Read from the daemon's own report
	// rather than probed: this command runs with the daemon stopped,
	// and touching a hung mount is how a read-only command hangs.
	fmt.Fprint(o.Stdout, formatOffsite(store.ReadOffsiteState(resolved), o.Now()))
	return ExitOK
}

func runRestore(args []string, o IO) int {
	fs := newFlags("db-tool restore", o)
	view := fs.String("view", "", "the caller's scope")
	dbPath := fs.String("db", "", "database path")
	generation := fs.Int("generation", 0, "which generation to restore (0 = newest)")
	dryRun := fs.Bool("dry-run", false, "show what would happen without making changes")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 0 {
		return usageErr(o, fmt.Errorf("db-tool restore: unexpected argument %q", fs.Arg(0)))
	}
	v, err := parseView(*view)
	if err != nil {
		return usageErr(o, err)
	}
	db := ResolveDBPath(*dbPath)
	now := o.Now()

	rows := store.Inspect(db, store.BackupGenerations)
	var target *store.GenerationInfo
	for i := range rows {
		if rows[i].Generation == *generation {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		return fail(o, fmt.Errorf("generation %d does not exist; run `pigtail db list` to see what does", *generation))
	}

	fmt.Fprint(o.Stdout, formatGenerations(rows, now, v))
	fmt.Fprintf(o.Stdout, "\nWould restore generation .%d (%s old, %s).\n",
		target.Generation, shortDuration(now.Sub(target.ModTime)), humanBytes(target.SizeBytes))
	fmt.Fprintf(o.Stdout, "The current database would move to %s\n", store.PreRestorePath(db, now))

	if *dryRun {
		fmt.Fprintln(o.Stdout, "\n(dry run — no changes made)")
		return ExitOK
	}
	if !*yes {
		if !o.IsTTY() {
			return fail(o, fmt.Errorf("not a terminal: re-run with --yes to restore generation .%d non-interactively", *generation))
		}
		if !confirmRestore(o, *generation) {
			return fail(o, errors.New("aborted"))
		}
	}

	res, err := store.Restore(db, *generation, now)
	if err != nil {
		return fail(o, err)
	}
	fmt.Fprintf(o.Stdout, "\n✓ restored %s → %s\n", res.From, db)
	fmt.Fprintf(o.Stdout, "  previous database kept at %s\n", res.PreRestorePath)
	for _, s := range res.SidecarsMoved {
		fmt.Fprintf(o.Stdout, "  stale sidecar moved to %s\n", s)
	}
	fmt.Fprintln(o.Stdout, "\nStart the daemon again:")
	fmt.Fprintf(o.Stdout, "  launchctl bootstrap gui/$(id -u) %s\n", setup.PlistPath())
	return ExitOK
}

// confirmRestore asks for an explicit yes. Default-no: a bare Enter must not
// replace a database. The prompt is what keeps --yes an explicit choice for
// scripts rather than the only way to use the command at all.
func confirmRestore(o IO, generation int) bool {
	fmt.Fprintf(o.Stdout, "Replace the current database with generation .%d? [y/N] ", generation)
	line, err := bufio.NewReader(o.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// ResolveDBPath answers where the database lives. The daemon defaults it
// beside its socket in the config dir; the flag exists for a daemon started
// with a non-default -db.
func ResolveDBPath(override string) string {
	if override != "" {
		return override
	}
	return filepath.Join(setup.ConfigDir(), "pigtail.db")
}

// formatGenerations renders the listing. Unknown counts print as a dash rather
// than 0: "no workspaces" and "we could not read it" must not look alike.
//
// Subjects are counted within view: a non-master caller gets its own scope's
// count alone (not the total — the difference would leak the others'); the
// master gets the total and one column per scope (subjectColumns).
func formatGenerations(rows []store.GenerationInfo, now time.Time, view scope.Scope) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	all := scope.SeesAll(view)
	cols := subjectColumns(rows)
	header := "GENERATION\tAGE\tSIZE\tINTEGRITY\tWORKSPACES\tPROJECTS\tSUBJECTS"
	if all {
		for _, c := range cols {
			header += "\t" + strings.ToUpper(c)
		}
	}
	fmt.Fprintln(w, header)
	for _, r := range rows {
		name := fmt.Sprintf(".%d", r.Generation)
		age := shortDuration(now.Sub(r.ModTime))
		if r.Generation == store.LiveGeneration {
			name, age = "live", "—"
		}
		integrity := "FAILED"
		switch {
		case r.IntegrityOK && r.HasMigrations:
			integrity = "ok"
		case r.IntegrityOK:
			integrity = "no schema"
		}
		subjects := humanCount(countOf(r, string(view)))
		if all {
			subjects = humanCount(totalOf(r))
			for _, c := range cols {
				subjects += "\t" + humanCount(countOf(r, c))
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			name, age, humanBytes(r.SizeBytes), integrity,
			humanCount(r.Workspaces), humanCount(r.Projects), subjects)
	}
	_ = w.Flush()
	return b.String()
}

// subjectColumns: the declared scopes, then any other scope a generation
// holds — an undeclared scope shows up rather than vanishing from the counts.
func subjectColumns(rows []store.GenerationInfo) []string {
	seen := map[string]bool{}
	var cols []string
	for _, n := range scope.Active().Names() {
		seen[string(n)] = true
		cols = append(cols, string(n))
	}
	var extra []string
	for _, r := range rows {
		for k := range r.SubjectsByScope {
			if !seen[k] {
				seen[k] = true
				extra = append(extra, k)
			}
		}
	}
	sort.Strings(extra)
	return append(cols, extra...)
}

func countOf(r store.GenerationInfo, sc string) int {
	if r.SubjectsByScope == nil {
		return -1
	}
	return r.SubjectsByScope[sc]
}

func totalOf(r store.GenerationInfo) int {
	if r.SubjectsByScope == nil {
		return -1
	}
	n := 0
	for _, v := range r.SubjectsByScope {
		n += v
	}
	return n
}

func humanCount(n int) string {
	if n < 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// shortDuration: a copy of the CLI's (cmd/pigtail/servers.go) and of store.OffsiteShortDuration, pinned by TestShortDurationParity.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
