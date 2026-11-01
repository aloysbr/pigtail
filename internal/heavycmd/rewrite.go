package heavycmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Outcome summarises what Rewrite did with a line, for the hook's log.
type Outcome string

const (
	OutcomeRewritten Outcome = "rewritten"
	OutcomePass      Outcome = "pass"     // nothing heavy in it
	OutcomeUnparsed  Outcome = "unparsed" // the lexer declined the line
	OutcomeOptOut    Outcome = "optout"
	OutcomeNested    Outcome = "nested"
	OutcomeSelf      Outcome = "self"
)

// Result is what Rewrite returns.
type Result struct {
	Line    string
	Outcome Outcome
	// Classes are the classes routed, one per rewritten command, in order.
	Classes []Class
	// Commands are the bare command words routed, in the same order as
	// Classes — `npx`, `yarn`, `tsc`. Safe to log; the arguments beside them
	// are not.
	Commands []string
	// Reason says why an unparsed line was declined.
	Reason Reason
	// Command is the command word of a declined line — the *only* part of it
	// that may be logged (unknownCommand when even that is unknowable).
	// Deliberately not the line and not its arguments: a log that could hold
	// a token passed on a command line would be a worse problem than the one
	// it documents.
	Command string
}

// Rewrite routes every heavy simple command of a shell line through
// `pigtail admit`, inserting the prefix right before the command word —
// after its leading assignments, so `FOO=1 tsc` keeps its environment — and
// leaving every other byte of the line as written.
//
// cwd is the directory the line runs in; `cd <dir>` segments move it for
// the ones that follow, so `cd apps/web && npm run lint` resolves apps/web's
// scripts. maxWait, when positive, is passed as --max-wait.
func Rewrite(line, cwd string, maxWait time.Duration) Result {
	segs, lexReason := lex(line)
	if lexReason != ReasonNone {
		return Result{Line: line, Outcome: OutcomeUnparsed,
			Reason: lexReason, Command: firstCommandWord(line)}
	}
	type insertion struct {
		at    int
		class Class
		cmd   string
	}
	var ins []insertion
	outcome := OutcomePass
	var reason Reason
	var command string
	dir := cwd
	for _, s := range segs {
		if len(s.words) == 0 {
			continue
		}
		words := make([]string, len(s.words))
		for k, w := range s.words {
			words[k] = w.val
		}
		if words[0] == "cd" {
			if len(words) == 2 && words[1] != "-" && s.op != "|" {
				dir = joinDir(dir, words[1])
			}
			continue
		}
		c, at, note, segReason := resolver{dir: dir}.classify(words, 0)
		switch note {
		case NoteOptOut:
			outcome = strongest(outcome, OutcomeOptOut)
		case NoteNested:
			outcome = strongest(outcome, OutcomeNested)
		case NoteSelf:
			outcome = strongest(outcome, OutcomeSelf)
		case NoteOpaque:
			// The line parses; this one command hides what it runs. Report
			// the first such command, with its own command word.
			if outcome != OutcomeUnparsed {
				reason, command = segReason, safeCommandWord(words[at])
			}
			outcome = strongest(outcome, OutcomeUnparsed)
		}
		if c != "" {
			ins = append(ins, insertion{at: s.words[at].start, class: c, cmd: safeCommandWord(words[at])})
		}
	}
	if len(ins) == 0 {
		return Result{Line: line, Outcome: outcome, Reason: reason, Command: command}
	}
	sort.Slice(ins, func(i, j int) bool { return ins[i].at < ins[j].at })
	var b strings.Builder
	prev := 0
	classes := make([]Class, 0, len(ins))
	cmds := make([]string, 0, len(ins))
	for _, in := range ins {
		b.WriteString(line[prev:in.at])
		b.WriteString(Prefix(in.class, maxWait))
		prev = in.at
		classes = append(classes, in.class)
		cmds = append(cmds, in.cmd)
	}
	b.WriteString(line[prev:])
	return Result{Line: b.String(), Outcome: OutcomeRewritten, Classes: classes, Commands: cmds}
}

// Prefix is the text inserted before a routed command.
func Prefix(c Class, maxWait time.Duration) string {
	if maxWait > 0 {
		return fmt.Sprintf("pigtail admit --class=%s --max-wait=%s -- ", c, maxWait.Round(time.Second))
	}
	return fmt.Sprintf("pigtail admit --class=%s -- ", c)
}

// strongest keeps the most informative of two non-rewrite outcomes: an
// explicit decision (opt-out, nested, self) says more than "unparsed", which
// says more than "pass".
func strongest(a, b Outcome) Outcome {
	rank := map[Outcome]int{OutcomePass: 0, OutcomeUnparsed: 1, OutcomeSelf: 2, OutcomeNested: 3, OutcomeOptOut: 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Getwd is os.Getwd, split out so callers and tests share one fallback rule:
// an unreadable cwd resolves no scripts but still classifies direct commands.
func Getwd() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}
