package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/cliexec"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// exitOrphanSkipped is 128+SIGHUP: the session that asked for the command
	// was already gone when its turn came, so it never ran.
	exitOrphanSkipped = 129

	// journalSendBudget bounds the journal write of a stop: the tree is
	// already told to stop, this is only the record.
	journalSendBudget = 2 * time.Second

	journalSummaryRunes = 200
)

// Seams, scripted by tests.
var (
	// openTTY opens the controlling terminal; it fails without one.
	openTTY = func() (interface{ Close() error }, error) {
		return os.OpenFile("/dev/tty", os.O_RDWR, 0)
	}
	// armOrphanWatch arms the orphan probe (nil: not armed).
	armOrphanWatch = cliexec.ArmOrphanWatch
)

// journalSender writes one journal entry. nil means "no journal".
type journalSender func(context.Context, pigtail.JournalEntry) error

// hasControllingTTY reports whether this process has a controlling terminal.
// Agent mode is its absence: the shell of an agent's Bash tool, launchd and
// cron have none, while a human at a terminal — and a `git push` run by him,
// whose pre-push hook has a pipe for stdin and a tty for stderr — do. The
// descriptor opened to ask is closed again.
func hasControllingTTY() bool {
	f, err := openTTY()
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// childGuard decides how the admitted command's tree is held. Only an agent
// (no controlling terminal), not nested under another admit (whose lease
// already covers it), gets the group, the orphan probe and the ceiling:
// everything else keeps the historical behaviour, the zero Opts.
//
// maxRun is --max-run when maxRunSet (0 lifts the ceiling), else the class's
// RunCeiling. watch is the armed probe, nil when it was not armed.
func childGuard(agent, nested bool, class string, maxRun time.Duration, maxRunSet bool, watch func() bool) cliexec.Opts {
	if !agent || nested {
		return cliexec.Opts{}
	}
	if !maxRunSet {
		maxRun = heavycmd.RunCeiling(heavycmd.Class(class))
	}
	return cliexec.Opts{Group: true, MaxRun: maxRun, Tree: processTree, Orphaned: watch}
}

// processTree lists the descendants of root, by ppid. A failed or empty `ps`
// gives no descendants: the stop then relies on the process group alone.
func processTree(root int) []int {
	parents, err := devserver.ProcessParents()
	if err != nil {
		return nil
	}
	return devserver.Descendants(parents, []int{root})
}

// guardMode reads, once and in this order, what childGuard needs: the mode
// and — before anything waits and before any signal.Notify — the armed probe.
func guardMode() (agent, nested bool, watch func() bool) {
	agent = !hasControllingTTY()
	nested = os.Getenv(envAdmitLease) != ""
	if agent && !nested {
		watch = armOrphanWatch()
	}
	return agent, nested, watch
}

// oneLine makes s a single line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }), " ")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n-1]) + "…"
	}
	return s
}

// cutRunes cuts s to at most n bytes without splitting a rune.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// newOnStop builds the cliexec OnStop of a run: one stderr line and one
// journal entry per stop. The journal write is bounded; a failure is a
// second stderr line, never silent.
func newOnStop(stderr io.Writer, who string, send journalSender, class, label, cwd string, limit time.Duration) func(reason string) {
	return func(reason string) {
		kind, msg := "admit-orphan-killed", fmt.Sprintf("orphaned (its session ended) — stopped the process tree of %s", oneLine(label, 80))
		if reason == "ceiling" {
			kind = "admit-ceiling-killed"
			msg = fmt.Sprintf("%s ran past its %s ceiling — stopped its process tree (exit 124; --max-run raises or lifts it)", oneLine(label, 80), limit)
		}
		record(stderr, who, send, kind, msg, class, label, cwd)
	}
}

// record prints msg on stderr and journals the event.
func record(stderr io.Writer, who string, send journalSender, kind, msg, class, label, cwd string) {
	fmt.Fprintf(stderr, "pigtail %s: %s\n", who, msg)
	if send == nil {
		return
	}
	summary := oneLine(label, journalSummaryRunes)
	if summary == "" {
		summary = class
	}
	e := pigtail.JournalEntry{
		ID:        fmt.Sprintf("%s:%d:%d", kind, os.Getpid(), time.Now().UnixNano()),
		Timestamp: time.Now(),
		Kind:      kind,
		Summary:   summary,
		Source: cutRunes("file:"+strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, cwd), pigtail.JournalSourceMax),
	}
	if err := e.Validate(); err != nil {
		fmt.Fprintf(stderr, "pigtail %s: journal: %v\n", who, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), journalSendBudget)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- send(ctx, e) }()
	select {
	case err := <-res:
		if err != nil {
			fmt.Fprintf(stderr, "pigtail %s: journal: %v\n", who, err)
		}
	case <-ctx.Done():
		fmt.Fprintf(stderr, "pigtail %s: journal: %v\n", who, ctx.Err())
	}
}

// skipOrphaned reports and journals a run that never started because its
// session was already gone, and returns the exit code to leave with.
func skipOrphaned(stderr io.Writer, who string, send journalSender, class, label, cwd string) int {
	record(stderr, who, send, "admit-orphan-skipped",
		fmt.Sprintf("its session is already gone — not starting %s (exit %d)", oneLine(label, 80), exitOrphanSkipped),
		class, label, cwd)
	return exitOrphanSkipped
}

// journalFrom turns the scoped client a command built in its own RunE
// (newScopedClient may only be called there) into a journalSender. A client
// that could not be built cuts the journal — the error then shows at the
// first write — never the command.
func journalFrom(c *pigtail.Client, err error, crossScope bool) journalSender {
	if err != nil {
		return func(context.Context, pigtail.JournalEntry) error { return err }
	}
	if crossScope {
		c = c.WithCrossScopeWrite()
	}
	return func(ctx context.Context, e pigtail.JournalEntry) error {
		_, aerr := c.AddJournal(ctx, e)
		return aerr
	}
}
