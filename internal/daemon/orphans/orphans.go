// Package orphans stops the process trees left behind by a dead Claude Code
// session. The Bash tool of Claude Code runs each command under a shell that
// sources a shell snapshot; that shell is the leader of its own group, has no
// controlling terminal and never execs. When its claude dies, the shell is
// reparented to launchd (ppid 1) and keeps whatever it started alive — a
// vitest, a chromium, a dev loop. The Reaper finds those shells, waits until
// one has been orphaned for a while (counted from the daemon's first sight
// of it), then stops its whole tree the way internal/cliexec stops one: TERM
// to every group of the tree, KILL after a grace to the captured pids still
// there unchanged.
package orphans

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/cliexec"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Mode is what the Reaper does with a tree past its threshold.
type Mode string

const (
	ModeOn      Mode = "on"      // stop the tree, journal orphan-tree-reaped
	ModeObserve Mode = "observe" // journal orphan-tree-would-reap, never signal
	ModeOff     Mode = "off"     // the daemon runs no Reaper
)

// ParseMode reads a -orphan-reaper value.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeOn, ModeObserve, ModeOff:
		return m, nil
	}
	return "", fmt.Errorf("orphan reaper mode %q: want on, observe or off", s)
}

const (
	// DefaultAfter is how long a shell must be seen orphaned before its tree
	// is stopped.
	DefaultAfter = 15 * time.Minute
	// DefaultInterval paces the sweeps.
	DefaultInterval = time.Minute
	// DefaultGrace is how long a stopped tree has to honour SIGTERM.
	DefaultGrace = 10 * time.Second
	// signatureWindow is the window of the positive control: claude alive
	// and no snapshot shell seen in it means the signature went blind.
	signatureWindow = 24 * time.Hour
	// summaryCommandMax bounds the root command in a journal summary.
	summaryCommandMax = 120
)

// agentShellRE is the signature of a Claude Code Bash-tool shell. Anchored:
// a command whose arguments merely contain this text is not one.
var agentShellRE = regexp.MustCompile(`^/bin/(zsh|bash) -c source \S+/shell-snapshots/snapshot-(zsh|bash)-\S+\.sh `)

// IsAgentShell reports whether command is a Claude Code Bash-tool shell.
func IsAgentShell(command string) bool { return agentShellRE.MatchString(command) }

// isClaude reports whether command's argv[0] is named claude.
func isClaude(command string) bool {
	f := strings.Fields(command)
	return len(f) > 0 && filepath.Base(f[0]) == "claude"
}

// key identifies an orphan root: a reused pid with another command is
// another root.
type key struct {
	pid     int
	command string
}

type sighting struct {
	first   time.Time
	handled bool // reaped (or journalled in observe): never again
}

// Reaper sweeps the process table for orphaned agent shells. Its fields are
// seams; New fills the real ones.
type Reaper struct {
	Mode     Mode
	After    time.Duration
	Interval time.Duration
	Grace    time.Duration

	Procs   func() ([]Proc, error)
	Signal  func(pid int, sig syscall.Signal) error // pid < 0: a group
	Now     func() time.Time
	Sleep   func(time.Duration)
	Journal func(pigtail.JournalEntry) error // nil: no journal
	Logf    func(format string, args ...any)

	seen map[key]*sighting

	windowStart          time.Time
	sawSnapshot, sawLive bool
}

// New returns a Reaper on the real process table and signals.
func New(mode Mode, after time.Duration) *Reaper {
	if after <= 0 {
		after = DefaultAfter
	}
	return &Reaper{
		Mode:     mode,
		After:    after,
		Interval: DefaultInterval,
		Grace:    DefaultGrace,
		Procs:    ListProcs,
		Signal:   syscall.Kill,
		Now:      time.Now,
		Sleep:    time.Sleep,
		Logf:     log.Printf,
		seen:     map[key]*sighting{},
	}
}

// Run logs its start, then sweeps every Interval until ctx is done.
func (r *Reaper) Run(ctx context.Context) {
	r.Logf("orphan reaper: mode %s, stops an orphaned agent shell's tree after %s, sweeps every %s",
		r.Mode, r.After, r.Interval)
	if ctx.Err() != nil {
		return
	}
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	r.Tick(r.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(r.Now())
		}
	}
}

// victim is a pid captured at the stop, with what it was then.
type victim struct {
	pid, pgid int
	command   string
}

// Tick runs one sweep at now.
func (r *Reaper) Tick(now time.Time) {
	if r.Mode == ModeOff {
		return
	}
	if r.seen == nil {
		r.seen = map[key]*sighting{}
	}
	procs, err := r.Procs()
	if err != nil {
		// No listing is no « every root vanished »: the counts stay.
		r.Logf("orphan reaper: %v", err)
		return
	}
	r.control(now, procs)

	byPID := make(map[int]Proc, len(procs))
	parents := make(map[int]int, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
		parents[p.PID] = p.PPID
	}

	current := map[key]bool{}
	var due []key
	for _, p := range procs {
		if p.PPID != 1 || p.PID != p.PGID || !IsAgentShell(p.Command) {
			continue
		}
		k := key{p.PID, p.Command}
		current[k] = true
		s := r.seen[k]
		if s == nil {
			s = &sighting{first: now}
			r.seen[k] = s
		}
		if !s.handled && now.Sub(s.first) >= r.After {
			due = append(due, k)
		}
	}
	for k := range r.seen {
		if !current[k] {
			delete(r.seen, k)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].pid < due[j].pid })

	var victims []victim
	for _, k := range due {
		s := r.seen[k]
		s.handled = true
		tree := append([]int{k.pid}, devserver.Descendants(parents, []int{k.pid})...)
		var cpu float64
		var mine []victim
		for _, pid := range tree {
			p := byPID[pid]
			cpu += p.CPU
			if pid <= 1 || pid == os.Getpid() || !cliexec.SignalablePgid(p.PGID) {
				continue
			}
			mine = append(mine, victim{pid, p.PGID, p.Command})
		}
		kind := "orphan-tree-would-reap"
		if r.Mode == ModeOn {
			kind = "orphan-tree-reaped"
			r.term(mine)
			victims = append(victims, mine...)
		}
		r.journal(pigtail.JournalEntry{
			ID:        fmt.Sprintf("orphan-tree:%d:%d", k.pid, s.first.Unix()),
			Timestamp: r.Now(),
			Kind:      kind,
			Scope:     string(scope.Master()),
			Summary: fmt.Sprintf("%s · orphaned %s · %d pids · CPU %.1f%%",
				oneLine(k.command, summaryCommandMax), now.Sub(s.first).Round(time.Second), len(tree), cpu),
			Source: fmt.Sprintf("proc:%d", k.pid),
		})
	}
	if len(victims) > 0 {
		r.Sleep(r.Grace)
		r.kill(victims)
	}
}

// term sends SIGTERM to every distinct group of the tree.
func (r *Reaper) term(vs []victim) {
	groups := map[int]bool{}
	for _, v := range vs {
		groups[v.pgid] = true
	}
	ordered := make([]int, 0, len(groups))
	for g := range groups {
		ordered = append(ordered, g)
	}
	sort.Ints(ordered)
	for _, g := range ordered {
		if !cliexec.SignalablePgid(g) {
			continue
		}
		if err := r.Signal(-g, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			r.Logf("orphan reaper: TERM group %d: %v", g, err)
		}
	}
}

// kill sends SIGKILL to the captured pids still there with the same command
// and the same group: a pid reused since the TERM is someone else.
func (r *Reaper) kill(vs []victim) {
	procs, err := r.Procs()
	if err != nil {
		r.Logf("orphan reaper: no KILL after the grace: %v", err)
		return
	}
	now := make(map[int]Proc, len(procs))
	for _, p := range procs {
		now[p.PID] = p
	}
	for _, v := range vs {
		p, ok := now[v.pid]
		if !ok || p.Command != v.command || p.PGID != v.pgid {
			continue
		}
		if v.pid <= 1 || v.pid == os.Getpid() {
			continue
		}
		if err := r.Signal(v.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			r.Logf("orphan reaper: KILL %d: %v", v.pid, err)
		}
	}
}

func (r *Reaper) journal(e pigtail.JournalEntry) {
	if r.Journal == nil {
		return
	}
	if err := r.Journal(e); err != nil {
		r.Logf("orphan reaper: journal %s: %v", e.ID, err)
	}
}

// control is the positive control: in each 24 h window, claude alive and no
// snapshot shell seen means the signature no longer matches what Claude Code
// runs. Logged once per window.
func (r *Reaper) control(now time.Time, procs []Proc) {
	if r.windowStart.IsZero() {
		r.windowStart = now
	}
	for _, p := range procs {
		if IsAgentShell(p.Command) {
			r.sawSnapshot = true
		} else if isClaude(p.Command) {
			r.sawLive = true
		}
	}
	if now.Sub(r.windowStart) < signatureWindow {
		return
	}
	if r.sawLive && !r.sawSnapshot {
		r.Logf("orphan reaper: signature shell-snapshots introuvable — claude ran for %s with no shell matching %s",
			signatureWindow, agentShellRE)
	}
	r.windowStart, r.sawSnapshot, r.sawLive = now, false, false
}

// oneLine flattens s onto one line of at most max runes.
func oneLine(s string, max int) string {
	s = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || c == utf8.RuneError {
			return ' '
		}
		return c
	}, s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
