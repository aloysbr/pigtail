package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// This file owns the `--merged` selection phase of `pigtail clean`.
//
// It is deliberately not in main.go: the CLI used to answer "is this branch
// merged?" itself, by shelling out to git — a violation of architecture.md §8
// (no exec.Command("git", …) outside internal/backend/git) and §10 (the CLI
// never touches git; it speaks to the daemon). Both are fixed by asking the
// daemon, which asks the backend, which is the only layer that knows what
// "merged" means for its kind of workspace.
//
// The selection also gets a real deadline. It runs *before* the first
// deletion, so `clean --timeout` — which bounds each teardown — has no power
// over it; a slow scan here used to hang the whole command (issue #54).

var (
	// mergedScanTimeout bounds the whole selection sweep.
	mergedScanTimeout = 2 * time.Minute

	// mergedProbeTimeout bounds a single workspace's containment probe, so one
	// pathological repo cannot eat the sweep's budget.
	mergedProbeTimeout = 30 * time.Second

	// mergedScanWorkers bounds concurrent probes. The phase is read-only, but
	// each probe spawns git (and possibly a delivery lookup), so we stay
	// polite with the machine.
	mergedScanWorkers = 4
)

// mergedVerdict is one workspace's answer during the selection sweep.
type mergedVerdict struct {
	ID     string
	Ref    string
	Reason string // why it was selected
	Note   string // why it could not be classified
}

// mergedSelection is the outcome of the sweep, shaped for reporting. Silently
// selecting 0 of N candidates reads as "nothing to clean", which is the
// opposite of the truth when the real answer is "we could not classify them"
// (issue #54, point 4) — so absent and unclassified are counted apart.
type mergedSelection struct {
	Considered   int
	Selected     []mergedVerdict
	Absent       int
	Unclassified []mergedVerdict
}

// selectMerged asks the daemon, for each workspace, whether its work is
// already contained in its base ref — or, failing that, whether the delivery
// layer says it was merged. The delivery answer is authoritative where git
// cannot decide: a squash whose conflicts were resolved by hand leaves no
// mechanical trace, but its PR is still MERGED.
func selectMerged(ctx context.Context, c *pigtail.Client, workspaces []pigtail.Workspace) mergedSelection {
	sel := mergedSelection{Considered: len(workspaces)}
	if len(workspaces) == 0 {
		return sel
	}

	results := make([]struct {
		verdict   mergedVerdict
		contained bool
		unknown   bool
	}, len(workspaces))

	workers := mergedScanWorkers
	if workers > len(workspaces) {
		workers = len(workspaces)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				ws := workspaces[idx]
				probeCx, cancel := context.WithTimeout(ctx, mergedProbeTimeout)
				pf, err := c.Preflight(probeCx, ws.ID)
				cancel()

				v := mergedVerdict{ID: ws.ID, Ref: ws.DisplayRef}
				switch {
				case err != nil:
					v.Note = err.Error()
					results[idx].unknown = true
				case pf.Git.MergeStatus == "contained" || pf.Git.Merged:
					v.Reason = containmentReason(pf)
					results[idx].contained = true
				case pf.Delivery != nil && pf.Delivery.State == "merged":
					// The delivery layer is the authoritative answer where git
					// cannot decide. Kept abstract on purpose: a non-git
					// Delivery answers this differently (architecture.md §8).
					v.Reason = "delivery merged"
					results[idx].contained = true
				case pf.Git.MergeStatus == "":
					v.Note = "backend cannot answer containment"
					results[idx].unknown = true
				case pf.Git.MergeStatus == "unknown":
					v.Note = pf.Git.MergeNote
					if v.Note == "" {
						v.Note = "unclassified"
					}
					results[idx].unknown = true
				}
				results[idx].verdict = v
			}
		}()
	}
	for i := range workspaces {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Report in workspace order, not completion order.
	for _, r := range results {
		switch {
		case r.contained:
			sel.Selected = append(sel.Selected, r.verdict)
		case r.unknown:
			sel.Unclassified = append(sel.Unclassified, r.verdict)
		default:
			sel.Absent++
		}
	}
	return sel
}

// containmentReason turns a preflight answer into the one-line "why" shown
// next to a selected workspace.
func containmentReason(pf *pigtail.PreflightResponse) string {
	how := pf.Git.MergedHow
	if how == "" {
		how = "merged"
	}
	base := pf.Git.MergeBaseRef
	if base == "" {
		return how
	}
	return how + " into " + base
}

// report writes the sweep summary. It always states how many workspaces were
// considered and how many could not be classified — a sweep that selects
// nothing must not be indistinguishable from a repo with nothing to clean.
func (s mergedSelection) report(w io.Writer) {
	fmt.Fprintf(w, "--merged: %d workspace(s) considered — %d selected, %d not merged, %d unclassified\n",
		s.Considered, len(s.Selected), s.Absent, len(s.Unclassified))
	for _, v := range s.Unclassified {
		label := v.Ref
		if label == "" {
			label = v.ID
		}
		fmt.Fprintf(w, "  ? %s: %s\n", label, v.Note)
	}
}
