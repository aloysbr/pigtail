package testrun

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Every run says what it got, arbitrated or not. A run that quietly went
// unarbitrated is how a 64 GB machine ended up at load 124 with nobody
// noticing (docs/specs/2026-09-22-mem-admission-design.md §1).
func TestStatusLineAlwaysStatesTheOutcome(t *testing.T) {
	arbitrated := Plan{
		Runner: RunnerVitest, Layout: LayoutSingle, Grain: GrainFile,
		Budget: 9, Ceiling: 9, CeilingFrom: "min(repo,pigtail)",
		BaseConfig: "/repo/vitest.config.ts",
	}
	line := StatusLine(arbitrated)
	for _, want := range []string{"pigtail:", "arbitrated", "runner=vitest", "layout=single", "grain=file", "budget=9"} {
		if !strings.Contains(line, want) {
			t.Errorf("status line missing %q: %s", want, line)
		}
	}
	if strings.Contains(line, "NOT arbitrated") {
		t.Errorf("an arbitrated run must not read as unarbitrated: %s", line)
	}

	skipped := Plan{Runner: RunnerVitest, Grain: GrainNone}.
		WithSkip(SkipNoConfig, "no vitest or vite config found", "run from the config's directory")
	line = StatusLine(skipped)
	if !strings.Contains(line, "NOT arbitrated") || !strings.Contains(line, SkipNoConfig) {
		t.Errorf("an unarbitrated run must say so, with its reason: %s", line)
	}
	// It must point at the command that explains the rest, rather than
	// leaving the user to guess.
	if !strings.Contains(line, "--explain") {
		t.Errorf("status line should point at --explain: %s", line)
	}
}

// The status line is one line: it prefixes a test run's own output, and a
// multi-line banner there is noise on every single run.
func TestStatusLineIsOneLine(t *testing.T) {
	p := Plan{Runner: RunnerVitest, Layout: LayoutSingle, Grain: GrainFile, Budget: 9, Ceiling: 9}
	if strings.Contains(StatusLine(p), "\n") {
		t.Error("status line must be a single line")
	}
}

func TestExplainPrintsEverySkipWithItsRemedy(t *testing.T) {
	p := Plan{Runner: RunnerJest, Cwd: "/repo", Root: "/repo", Budget: 9}.
		WithSkip(SkipNoConfig, "this is a jest repo", "run it through `pigtail admit`")

	var buf bytes.Buffer
	Explain(&buf, p)
	out := buf.String()
	for _, want := range []string{"jest", SkipNoConfig, "this is a jest repo", "pigtail admit"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}
}

// --explain --json is what an agent reads; it must be machine-parseable and
// carry the skip codes verbatim.
func TestExplainJSONRoundTrips(t *testing.T) {
	p := Plan{
		Runner: RunnerVitest, Layout: LayoutWorkspace, Grain: GrainNone,
		Cwd: "/repo/apps/web", Root: "/repo", Budget: 9,
		Projects: []Project{{ConfigPath: "/repo/apps/web/vite.config.ts", Root: "/repo/apps/web"}},
	}.WithSkip(SkipWorkspaceNotStatic, "the workspace file is not a static list", "run it through pigtail admit")

	var buf bytes.Buffer
	if err := ExplainJSON(&buf, p); err != nil {
		t.Fatalf("ExplainJSON: %v", err)
	}
	var got Plan
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got.Skips[0].Code != SkipWorkspaceNotStatic {
		t.Errorf("skip code = %q, want %q", got.Skips[0].Code, SkipWorkspaceNotStatic)
	}
	if len(got.Projects) != 1 {
		t.Errorf("projects = %v, want the one project kept", got.Projects)
	}
	if got.Arbitrated() {
		t.Error("a plan with skips must not read as arbitrated after a round trip")
	}
}

// A repo holding both runners gets an arbitrated vitest plan plus a note: the
// jest suite is reachable, and only --explain is quiet enough to say so.
func TestExplainPrintsNotes(t *testing.T) {
	p := Plan{
		Runner: RunnerVitest, Layout: LayoutSingle, Grain: GrainFile,
		Budget: 9, Ceiling: 9, CeilingFrom: "min(repo,pigtail)",
		Notes: []string{"this repo also has jest (jest.config.js) — `pigtail test --runner=jest` arbitrates that suite"},
	}
	var b strings.Builder
	Explain(&b, p)
	out := b.String()
	if !strings.Contains(out, "note:") || !strings.Contains(out, "--runner=jest") {
		t.Errorf("explain output has no note:\n%s", out)
	}
	// A note is not a skip: the run is still arbitrated.
	if strings.Contains(out, "NOT arbitrated") {
		t.Errorf("a note must not read as a refusal:\n%s", out)
	}
}

// The status line stays one line whatever the runner, and never carries notes:
// a two-runner repo would otherwise print the same reminder on every run.
func TestStatusLineNamesTheRunnerAndOmitsNotes(t *testing.T) {
	p := Plan{
		Runner: RunnerJest, Layout: LayoutSingle, Grain: GrainFile,
		Budget: 9, Ceiling: 9, CeilingFrom: "min(repo,pigtail)",
		Notes: []string{"this repo also has vitest"},
	}
	line := StatusLine(p)
	if !strings.Contains(line, "runner=jest") {
		t.Errorf("status line = %q, want runner=jest", line)
	}
	if strings.Contains(line, "also has") || strings.Contains(line, "\n") {
		t.Errorf("status line = %q, want one line with no note", line)
	}
}
