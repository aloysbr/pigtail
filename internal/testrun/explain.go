package testrun

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StatusLine is the one line every run prints to stderr, arbitrated or not.
//
// Before it, arbitration announced itself only when it succeeded, and some of
// the ways it declined printed nothing at all. A run that silently went
// unarbitrated looked exactly like one that was arbitrated — which is how
// several worktrees each ran a full-budget suite on one machine without anyone
// being able to see it. Saying it every time costs one line; not saying it
// cost a day.
func StatusLine(p Plan) string {
	if !p.Arbitrated() {
		s := p.Skips[0]
		return fmt.Sprintf("pigtail: NOT arbitrated runner=%s reason=%s (%s) — run `pigtail test --explain` for the rest",
			p.Runner, s.Code, s.Detail)
	}
	// "≤" rather than a bare number: pigtail promises an upper bound, and the
	// repo's own config may pin something lower when the wrapper evaluates it
	// — printing a flat "ceiling=9" would claim nine workers on a repo that
	// pinned four, which is the very confusion this release fixes.
	ceiling := fmt.Sprintf("≤%d", p.Ceiling)
	if p.CeilingFrom != "" {
		ceiling += " " + p.CeilingFrom
	}
	line := fmt.Sprintf("pigtail: arbitrated runner=%s layout=%s grain=%s budget=%d ceiling=%s",
		p.Runner, p.Layout, p.Grain, p.Budget, ceiling)
	if p.Class != "" {
		// A run charged as something other than its runner is worth one
		// word on every run: it is why its wait, and its reservation, differ
		// from the identical-looking run next door.
		line += " class=" + p.Class
	}
	return line
}

// Explain writes the whole Plan in the form a human reads, including every
// skip with its remedy. It is what `pigtail test --explain` prints, and it
// resolves without writing anything into the target repo.
func Explain(w io.Writer, p Plan) {
	var b strings.Builder
	verdict := "arbitrated"
	if !p.Arbitrated() {
		verdict = "NOT arbitrated"
	}
	fmt.Fprintf(&b, "%s\n\n", verdict)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "  %-14s %s\n", k, v)
		}
	}
	row("runner", string(p.Runner))
	if p.Class != "" {
		charged := fmt.Sprintf("%s (memory admission)", p.Class)
		if p.Workers > 0 {
			charged = fmt.Sprintf("%s, ~%d workers", charged, p.Workers)
		}
		row("charged as", charged)
	}
	row("layout", string(p.Layout))
	row("grain", string(p.Grain))
	row("cwd", p.Cwd)
	row("worktree", p.Root)
	row("config", p.BaseConfig)
	row("node_modules", p.NodeModules)
	row("budget", fmt.Sprintf("%d slots", p.Budget))
	if p.Arbitrated() {
		row("ceiling", fmt.Sprintf("at most %d workers (%s — this repo's own config may pin fewer, and pigtail keeps the lower)",
			p.Ceiling, p.CeilingFrom))
	}
	if len(p.Projects) > 0 {
		row("projects", fmt.Sprintf("%d", len(p.Projects)))
		for _, pr := range p.Projects {
			fmt.Fprintf(&b, "  %-14s %s\n", "", pr.ConfigPath)
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "\n  note: %s\n", n)
	}
	for _, s := range p.Skips {
		fmt.Fprintf(&b, "\n  not arbitrated: %s\n    %s\n", s.Code, s.Detail)
		if s.Remedy != "" {
			fmt.Fprintf(&b, "    remedy: %s\n", s.Remedy)
		}
	}
	_, _ = io.WriteString(w, b.String())
}

// ExplainJSON writes the Plan as JSON — what an agent reads, and what a PR
// description can carry as evidence.
func ExplainJSON(w io.Writer, p Plan) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return fmt.Errorf("encode plan: %w", err)
	}
	return nil
}
