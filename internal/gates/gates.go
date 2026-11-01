// Package gates reads the gates the user's gate tool declares (SI-D7: that tool
// declares, pigtail shows). It never replays gates.jsonl itself: a second
// replayer would drift from the gate tool's. It runs `<cmd> list --json`, whose
// keys are a contract documented in the gate tool's header.
package gates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// DefaultTimeout bounds one run: bin/gate replays a few hundred JSONL lines in
// well under a second; 10 s covers a cold python start, never a hung request.
const DefaultTimeout = 10 * time.Second

// Error kinds, stable on the wire.
const (
	ErrNotFound      = "not_found"
	ErrFailed        = "failed"
	ErrTimeout       = "timeout"
	ErrInvalidOutput = "invalid_output"
)

// Runner runs name with args and returns its stdout. ExecRunner is the real one.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner leaves cmd.Stderr nil so that Output fills ExitError.Stderr.
// WaitDelay makes the timeout hard: when the context kills the command, a child
// it left holding stdout open would otherwise keep Output waiting for EOF.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// Source is pigtaild's --gate-cmd. Cmd "" means not configured (the caller checks).
type Source struct {
	Cmd     string
	Timeout time.Duration
	Run     Runner
}

// Gate is one parsed row.
type Gate struct {
	ID, Title, Action string
	Scope             scope.Scope
	Status            string
	Added             time.Time
	FiredAt           *time.Time
	SubjectID         string
	Value             string
	LastCheck         *time.Time
	LastRun           *time.Time // the gate tool's last complete `gate check` pass (same on every row)
	Errors            int
	Broken            bool // the gate tool's verdict: errors for >= 72 h in a row — pigtail never recomputes it
	NotBefore         string
}

func (s Source) Read(ctx context.Context) ([]Gate, *pigtail.GatesError) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	run := s.Run
	if run == nil {
		run = ExecRunner
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(ctx, s.Cmd, "list", "--json")
	if err != nil {
		return nil, classify(ctx, s.Cmd, err)
	}
	return Parse(out)
}

func classify(ctx context.Context, cmd string, err error) *pigtail.GatesError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &pigtail.GatesError{Kind: ErrTimeout, Message: fmt.Sprintf("%s list --json: no answer within the timeout", cmd)}
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return &pigtail.GatesError{Kind: ErrNotFound, Message: fmt.Sprintf("%s: not found (pigtaild --gate-cmd)", cmd)}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &pigtail.GatesError{Kind: ErrFailed, Message: fmt.Sprintf("%s list --json: exit %d: %s", cmd, ee.ExitCode(), lastLine(ee.Stderr))}
	}
	return &pigtail.GatesError{Kind: ErrFailed, Message: fmt.Sprintf("%s list --json: %v", cmd, err)}
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 200 {
		l = l[:200]
	}
	return l
}

type rawGate struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Action    string  `json:"action"`
	Scope     string  `json:"scope"`
	Status    string  `json:"status"`
	Added     string  `json:"added"`
	FiredAt   *string `json:"fired_at"`
	SubjectID *string `json:"subject_id"`
	Value     *string `json:"value"`
	LastCheck *string `json:"last_check"`
	LastRun   *string `json:"last_run"`
	Errors    int     `json:"errors"`
	Broken    bool    `json:"broken"`
	NotBefore *string `json:"not_before"`
}

func invalid(format string, a ...any) *pigtail.GatesError {
	return &pigtail.GatesError{Kind: ErrInvalidOutput, Message: fmt.Sprintf(format, a...)}
}

// Parse reads `gate list --json`. A missing contract key fails the whole read
// (the contract broke: say so); an unparseable fired_at/last_check/last_run only makes
// that age unknown. last_run and broken are additive: absent = unknown / false.
func Parse(out []byte) ([]Gate, *pigtail.GatesError) {
	var rows []rawGate
	if err := json.Unmarshal(bytes.TrimSpace(out), &rows); err != nil {
		return nil, invalid("gate list --json: not a JSON array of gates: %v", err)
	}
	if rows == nil {
		return nil, invalid("gate list --json: null instead of an array")
	}
	gs := make([]Gate, 0, len(rows))
	for i, r := range rows {
		if r.Status == "closed" {
			continue
		}
		// Only what places a gate is required: an empty title or action is shown
		// empty, never a reason to lose every other gate to invalid_output.
		for _, f := range []struct{ name, v string }{
			{"id", r.ID}, {"scope", r.Scope}, {"status", r.Status}, {"added", r.Added},
		} {
			if strings.TrimSpace(f.v) == "" {
				return nil, invalid("gate #%d (%q): key %q missing — the gate tool's list --json contract changed", i, r.ID, f.name)
			}
		}
		added, err := time.Parse(time.RFC3339, r.Added)
		if err != nil {
			return nil, invalid("gate %q: added %q is not RFC 3339", r.ID, r.Added)
		}
		gs = append(gs, Gate{
			ID: r.ID, Title: r.Title, Action: r.Action, Scope: scope.Canonical(r.Scope),
			Status: r.Status, Added: added, FiredAt: optTime(r.FiredAt), SubjectID: deref(r.SubjectID),
			Value: deref(r.Value), LastCheck: optTime(r.LastCheck), LastRun: optTime(r.LastRun),
			Errors: r.Errors, Broken: r.Broken, NotBefore: deref(r.NotBefore),
		})
	}
	return gs, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func optTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}

func ageSeconds(t *time.Time, now time.Time) *int {
	if t == nil {
		return nil
	}
	s := int(now.Sub(*t).Seconds())
	if s < 0 {
		s = 0
	}
	return &s
}

// Build is the response for one caller: the gates canRead lets through,
// fired first (oldest fire first), then open (oldest first).
func Build(gs []Gate, canRead func(scope.Scope) bool, complete bool, now time.Time) pigtail.GatesResponse {
	out := pigtail.GatesResponse{Configured: true, Complete: complete,
		Fired: []pigtail.GateView{}, Open: []pigtail.GateView{}, ReadAt: now}
	var counts pigtail.GateCounts
	var lastRun *time.Time
	for _, g := range gs {
		// The pass of `gate check` is global, not a gate's data: read it before the scope filter.
		if g.LastRun != nil && (lastRun == nil || g.LastRun.After(*lastRun)) {
			lr := *g.LastRun
			lastRun = &lr
		}
		if !canRead(g.Scope) {
			continue
		}
		v := pigtail.GateView{ID: g.ID, Title: g.Title, Action: g.Action, Scope: string(g.Scope),
			Status: g.Status, AddedAt: g.Added, SubjectID: g.SubjectID, Value: g.Value,
			Errors: g.Errors, NotBefore: g.NotBefore}
		switch g.Status {
		case "fired", "firing":
			v.FiredAt, v.FiredAgeSeconds = g.FiredAt, ageSeconds(g.FiredAt, now)
			out.Fired = append(out.Fired, v)
			counts.Fired++
		default:
			out.Open = append(out.Open, v)
			counts.Open++
			if g.Broken {
				counts.Broken++
			}
		}
	}
	sort.SliceStable(out.Fired, func(i, j int) bool {
		a, b := out.Fired[i].FiredAt, out.Fired[j].FiredAt
		switch {
		case a == nil && b == nil:
			return out.Fired[i].ID < out.Fired[j].ID
		case a == nil:
			return false
		case b == nil:
			return true
		case !a.Equal(*b):
			return a.Before(*b)
		}
		return out.Fired[i].ID < out.Fired[j].ID
	})
	sort.SliceStable(out.Open, func(i, j int) bool {
		if !out.Open[i].AddedAt.Equal(out.Open[j].AddedAt) {
			return out.Open[i].AddedAt.Before(out.Open[j].AddedAt)
		}
		return out.Open[i].ID < out.Open[j].ID
	})
	out.Counts = &counts
	if lastRun != nil {
		out.LastCheckAt, out.LastCheckAgeSeconds = lastRun, ageSeconds(lastRun, now)
	}
	return out
}
