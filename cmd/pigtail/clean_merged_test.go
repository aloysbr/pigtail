package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// mergedDaemon serves the two endpoints `clean --merged` uses: the workspace
// list, and one preflight answer per workspace. It records the DELETEs so a
// test can assert on what was actually selected rather than on printed text.
type mergedDaemon struct {
	*stubDaemon
	rec *cleanRecorder
}

func newMergedDaemon(t *testing.T, workspaces []pigtail.Workspace, preflight map[string]pigtail.PreflightResponse) *mergedDaemon {
	t.Helper()
	rec := &cleanRecorder{}

	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, workspaces)
	})
	mux.HandleFunc("/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/workspaces/")
		if id, ok := strings.CutSuffix(rest, "/preflight"); ok {
			pf, known := preflight[id]
			if !known {
				http.Error(w, "no preflight", http.StatusNotFound)
				return
			}
			writeData(w, http.StatusOK, pf)
			return
		}
		if r.Method == http.MethodDelete {
			rec.add(rest)
			writeData(w, http.StatusOK, map[string]any{"workspace_id": rest})
			return
		}
		http.Error(w, "method", http.StatusMethodNotAllowed)
	})

	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return &mergedDaemon{stubDaemon: s, rec: rec}
}

func ws(id, ref string) pigtail.Workspace {
	return pigtail.Workspace{ID: id, DisplayRef: ref, State: pigtail.StateBuilding}
}

// runCleanMerged executes `clean --merged`, returning what the command printed.
func runCleanMerged(t *testing.T, d *mergedDaemon) string {
	t.Helper()
	var out bytes.Buffer
	withStubDaemon(t, d.stubDaemon, func() {
		cmd := cleanCmd()
		cmd.SetArgs([]string{"--merged"})
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("clean --merged: %v", err)
		}
	})
	return out.String()
}

// TestCleanMergedSelectsSquashMergedWorkspace is the issue #54 regression at
// the command level: a workspace the backend reports as contained via a
// multi-commit squash must be selected, and one that is merely absent must not.
func TestCleanMergedSelectsSquashMergedWorkspace(t *testing.T) {
	d := newMergedDaemon(t,
		[]pigtail.Workspace{ws("ws_squash", "feat/redesign"), ws("ws_open", "feat/wip")},
		map[string]pigtail.PreflightResponse{
			"ws_squash": {WorkspaceID: "ws_squash", Git: pigtail.PreflightGit{
				Clean: true, MergeStatus: "contained", MergedHow: "squash-merged", MergeBaseRef: "origin/main",
			}},
			"ws_open": {WorkspaceID: "ws_open", Git: pigtail.PreflightGit{
				Clean: true, MergeStatus: "absent",
			}},
		})

	runCleanMerged(t, d)

	got := d.rec.snapshot()
	if len(got) != 1 || got[0] != "ws_squash" {
		t.Fatalf("cleaned %v, want [ws_squash] only", got)
	}
}

// TestCleanMergedTrustsDeliveryWhenGitCannotDecide covers the case git has no
// mechanical trace of (a hand-resolved squash): the delivery layer is the
// authoritative answer.
func TestCleanMergedTrustsDeliveryWhenGitCannotDecide(t *testing.T) {
	d := newMergedDaemon(t,
		[]pigtail.Workspace{ws("ws_pr", "feat/shipped")},
		map[string]pigtail.PreflightResponse{
			"ws_pr": {
				WorkspaceID: "ws_pr",
				Git:         pigtail.PreflightGit{Clean: true, MergeStatus: "absent"},
				Delivery:    &pigtail.PreflightDelivery{State: "merged", URL: "https://example/pr/1"},
			},
		})

	runCleanMerged(t, d)

	if got := d.rec.snapshot(); len(got) != 1 || got[0] != "ws_pr" {
		t.Fatalf("cleaned %v, want [ws_pr]", got)
	}
}

// TestCleanMergedReportsUnclassified proves the sweep never passes off "we
// could not tell" as "nothing to clean" (issue #54, point 4).
func TestCleanMergedReportsUnclassified(t *testing.T) {
	d := newMergedDaemon(t,
		[]pigtail.Workspace{ws("ws_a", "feat/a"), ws("ws_b", "feat/b"), ws("ws_c", "feat/c")},
		map[string]pigtail.PreflightResponse{
			"ws_a": {Git: pigtail.PreflightGit{MergeStatus: "absent"}},
			"ws_b": {Git: pigtail.PreflightGit{MergeStatus: "unknown", MergeNote: "base ref origin/main not found"}},
			"ws_c": {Git: pigtail.PreflightGit{MergeStatus: "contained", MergedHow: "merged", MergeBaseRef: "main"}},
		})

	out := runCleanMerged(t, d)

	for _, want := range []string{
		"3 workspace(s) considered",
		"1 selected",
		"1 not merged",
		"1 unclassified",
		"feat/b: base ref origin/main not found",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestCleanMergedCountsAFailedProbeAsUnclassified proves a daemon that cannot
// answer never silently reduces the candidate set.
func TestCleanMergedCountsAFailedProbeAsUnclassified(t *testing.T) {
	d := newMergedDaemon(t,
		[]pigtail.Workspace{ws("ws_gone", "feat/gone")},
		map[string]pigtail.PreflightResponse{}) // no preflight answer at all

	out := runCleanMerged(t, d)

	if got := d.rec.snapshot(); len(got) != 0 {
		t.Fatalf("cleaned %v, want nothing", got)
	}
	if !strings.Contains(out, "1 unclassified") {
		t.Errorf("expected the failed probe to be reported as unclassified:\n%s", out)
	}
}

// TestCleanMergedDoesNotDoubleSelectShippedWorkspaces guards the --shipped +
// --merged combination against cleaning the same workspace twice.
func TestCleanMergedDoesNotDoubleSelectShippedWorkspaces(t *testing.T) {
	shipped := pigtail.Workspace{ID: "ws_ship", DisplayRef: "feat/ship", State: pigtail.StateShipped}
	d := newMergedDaemon(t,
		[]pigtail.Workspace{shipped, ws("ws_merged", "feat/merged")},
		map[string]pigtail.PreflightResponse{
			"ws_merged": {Git: pigtail.PreflightGit{MergeStatus: "contained", MergedHow: "merged", MergeBaseRef: "main"}},
			"ws_ship":   {Git: pigtail.PreflightGit{MergeStatus: "contained", MergedHow: "merged", MergeBaseRef: "main"}},
		})

	withStubDaemon(t, d.stubDaemon, func() {
		cmd := cleanCmd()
		cmd.SetArgs([]string{"--merged", "--shipped"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("clean: %v", err)
		}
	})

	got := d.rec.snapshot()
	sort.Strings(got)
	want := []string{"ws_merged", "ws_ship"}
	if len(got) != len(want) {
		t.Fatalf("cleaned %v, want each workspace exactly once: %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cleaned %v, want %v", got, want)
		}
	}
}
