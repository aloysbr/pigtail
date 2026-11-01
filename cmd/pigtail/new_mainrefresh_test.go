package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newWorkspaceDaemon serves POST /workspaces/new with a canned workspace,
// optionally carrying the main-refresh note the create path produced.
func newWorkspaceDaemon(t *testing.T, note *pigtail.MainRefreshNote) *stubDaemon {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces/new", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		writeData(w, http.StatusCreated, pigtail.Workspace{
			ID:          "ws_test",
			ProjectID:   "prj_1",
			Path:        "/Users/me/dev/wk-workspace/feat-x",
			DisplayRef:  "feat/x",
			Ports:       map[string]int{"dev": 3007},
			MainRefresh: note,
		})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return s
}

// runNew executes `pigtail new` against the stub, returning stdout and stderr
// SEPARATELY — the whole contract under test is about which stream a line
// lands on, so a harness that merged them could not see a violation.
func runNew(t *testing.T, note *pigtail.MainRefreshNote, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	withStubDaemon(t, newWorkspaceDaemon(t, note), func() {
		cmd := newCmd()
		cmd.SetArgs(append([]string{"an intent"}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("pigtail new: %v", err)
		}
	})
	return out.String(), errOut.String()
}

const wantPath = "/Users/me/dev/wk-workspace/feat-x"

// TestNewFirstLineIsAlwaysTheWorktreePath is the contract every other test
// here is subordinate to: `cd "$(pigtail new --quiet '…')"` consumes the first
// line of stdout as a path. Any message that lands on stdout, or lands before
// it, breaks a documented shell idiom in a way that fails as a `cd` into a
// sentence. The drift notice is therefore stderr-only, on every path.
func TestNewFirstLineIsAlwaysTheWorktreePath(t *testing.T) {
	notes := map[string]*pigtail.MainRefreshNote{
		"no note": nil,
		"a fast-forward": {
			Action: pigtail.MainRefreshUpdated, Base: "origin/main",
			Behind: 23, From: "08e971a", To: "4c1f9d2",
		},
		"an abstention over a stale checkout": {
			Action: pigtail.MainRefreshSkipped, Base: "origin/main", Behind: 23,
			Reason: "working tree not pristine (3 uncommitted or untracked entries)",
		},
		"a level checkout": {Action: pigtail.MainRefreshUpToDate, Base: "origin/main"},
	}
	for name, note := range notes {
		for _, flags := range [][]string{nil, {"--quiet"}} {
			t.Run(name+strings.Join(flags, " "), func(t *testing.T) {
				stdout, stderr := runNew(t, note, flags...)

				first, _, _ := strings.Cut(stdout, "\n")
				if first != wantPath {
					t.Fatalf("first line of stdout = %q, want the worktree path %q", first, wantPath)
				}
				if strings.Contains(stdout, "main") {
					t.Errorf("a main-worktree message reached stdout:\n%s", stdout)
				}
				if strings.Contains(stderr, wantPath) {
					t.Errorf("the path must not be duplicated onto stderr:\n%s", stderr)
				}
			})
		}
	}
}

// TestNewNamesTheDriftItClosed: the create path fast-forwards main before
// branching from it. When that move was a real catch-up, say so — "23 commits
// behind, caught up" is the sentence whose absence let the staleness survive
// two months under a fix believed to be applied.
func TestNewNamesTheDriftItClosed(t *testing.T) {
	_, stderr := runNew(t, &pigtail.MainRefreshNote{
		Action: pigtail.MainRefreshUpdated, Base: "origin/main",
		Behind: 23, From: "08e971a", To: "4c1f9d2",
	})
	for _, want := range []string{"23 commits", "behind", "origin/main", "08e971a", "4c1f9d2"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q does not carry %q", stderr, want)
		}
	}
}

// TestNewNamesTheDriftItLeftInPlace: the abstention is the half that matters
// most. pigtail knows main is behind, has decided not to touch it, and until
// now said so only in a daemon log nobody greps.
func TestNewNamesTheDriftItLeftInPlace(t *testing.T) {
	_, stderr := runNew(t, &pigtail.MainRefreshNote{
		Action: pigtail.MainRefreshSkipped, Base: "origin/main", Behind: 23,
		Reason: `checked out on "feat/x", not "main"`,
	})
	for _, want := range []string{"23 commits", "left as-is", `checked out on "feat/x"`} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q does not carry %q", stderr, want)
		}
	}
}

// TestNewSaysNothingWhenThereIsNoDrift: a line printed on every create is a
// line nobody reads by the third day. Silence is what gives the two above
// their weight.
func TestNewSaysNothingWhenThereIsNoDrift(t *testing.T) {
	cases := map[string]*pigtail.MainRefreshNote{
		"level":            {Action: pigtail.MainRefreshUpToDate, Base: "origin/main"},
		"no note at all":   nil,
		"local base":       {Action: pigtail.MainRefreshSkipped, Base: "main", Reason: `base "main" is local — nothing remote to fast-forward onto`},
		"drift unmeasured": {Action: pigtail.MainRefreshSkipped, Base: "origin/main", Reason: "working tree state unreadable"},
	}
	for name, note := range cases {
		t.Run(name, func(t *testing.T) {
			if _, stderr := runNew(t, note); strings.TrimSpace(stderr) != "" {
				t.Fatalf("stderr should be empty, got %q", stderr)
			}
		})
	}
}

// TestNewQuietStaysQuiet: --quiet exists so a shell can consume the path and
// nothing else. It must silence the drift notice too — a warning on stderr is
// harmless to `$(…)` but the flag's promise is one line, and a caller
// redirecting 2>&1 would have it back inside the substitution.
func TestNewQuietStaysQuiet(t *testing.T) {
	stdout, stderr := runNew(t, &pigtail.MainRefreshNote{
		Action: pigtail.MainRefreshSkipped, Base: "origin/main", Behind: 23,
		Reason: "working tree not pristine (3 uncommitted or untracked entries)",
	}, "--quiet")

	if got := strings.TrimSpace(stdout); got != wantPath {
		t.Fatalf("--quiet printed %q, want only the path", got)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("--quiet wrote to stderr: %q", stderr)
	}
}

// TestMainRefreshNoteRoundTripsOverTheWire: the note is stamped by the daemon
// and read by the CLI; a JSON tag drift on either side would silently restore
// the invisibility this feature removes (the client would decode a zero note
// and print nothing, which looks exactly like "no drift").
func TestMainRefreshNoteRoundTripsOverTheWire(t *testing.T) {
	t.Parallel()
	in := pigtail.Workspace{MainRefresh: &pigtail.MainRefreshNote{
		Action: pigtail.MainRefreshSkipped, Base: "origin/main", Path: "/x/main",
		Behind: 23, Reason: "dirty",
	}}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out pigtail.Workspace
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	if out.MainRefresh == nil || out.MainRefresh.Behind != 23 || out.MainRefresh.Action != pigtail.MainRefreshSkipped {
		t.Fatalf("note did not survive the wire: %+v", out.MainRefresh)
	}
	// And a workspace without one must not marshal an empty object that the
	// CLI would then have to distinguish from a real verdict.
	blob, _ = json.Marshal(pigtail.Workspace{})
	if strings.Contains(string(blob), "main_refresh") {
		t.Fatalf("a workspace with no verdict emitted a note: %s", blob)
	}
}
