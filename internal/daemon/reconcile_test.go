package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The reconciliation is where the record catches up with git. It is a
// background pass rather than something a handler does, for two reasons that
// are the whole design:
//
//   - a `GET /workspaces` that writes to the store is a surprise, and the menu
//     bar polls it every few seconds;
//   - `DisplayRef` is pure and synchronous — no context, no error — so it
//     cannot do git I/O at all. Giving it a store handle would not change that;
//     the signature is the constraint, not the dependency.
//
// So `DisplayRef` keeps stamping from the record, and this keeps the record
// true.

type fakeRefResolver struct {
	refs map[string]string
	err  error
	// calls counts invocations so a test can prove nothing ran.
	calls int
}

func (f *fakeRefResolver) ResolveRefs(_ context.Context, _ []pigtail.Workspace) (map[string]string, error) {
	f.calls++
	return f.refs, f.err
}

type recordingWriter struct {
	written map[string]map[string]any
}

func (w *recordingWriter) update(id string, data map[string]any) error {
	if w.written == nil {
		w.written = map[string]map[string]any{}
	}
	w.written[id] = data
	return nil
}

func wsWithBranch(id, branch string) pigtail.Workspace {
	return pigtail.Workspace{
		ID:          id,
		Path:        "/repo/" + id,
		BackendData: map[string]any{"branch": branch, "base_branch": "main"},
	}
}

func TestReconcileWritesOnlyWhatDiverged(t *testing.T) {
	wss := []pigtail.Workspace{
		wsWithBranch("ws_moved", "feat/old"),
		wsWithBranch("ws_same", "feat/steady"),
	}
	res := &fakeRefResolver{refs: map[string]string{
		"ws_moved": "feat/new",
		"ws_same":  "feat/steady",
	}}
	w := &recordingWriter{}

	n := reconcileBranchRecords(context.Background(), wss, res, w.update, nil)

	if n != 1 {
		t.Fatalf("reconciled %d records, want 1", n)
	}
	if got := w.written["ws_moved"]["branch"]; got != "feat/new" {
		t.Fatalf("ws_moved branch = %v, want feat/new", got)
	}
	if _, touched := w.written["ws_same"]; touched {
		t.Fatal("a record that already matched was rewritten; a no-op must write nothing")
	}
	// Everything else in the record survives: this refreshes one key, it does
	// not replace the backend's data.
	if got := w.written["ws_moved"]["base_branch"]; got != "main" {
		t.Fatalf("base_branch = %v, want it carried through untouched", got)
	}
}

// The rule that keeps this from being destructive. A detached worktree, a
// vanished directory, a git that could not answer — all arrive as "absent from
// the map", and absent must never mean "clear the record". Overwriting a real
// branch name with "" would lose the only information the teardown has left.
func TestReconcileNeverClearsARecord(t *testing.T) {
	wss := []pigtail.Workspace{
		wsWithBranch("ws_detached", "feat/real"),
		wsWithBranch("ws_gone", "feat/also-real"),
	}
	// Both absent from the resolver's answer, plus an explicit empty string
	// for good measure: neither may reach the store.
	res := &fakeRefResolver{refs: map[string]string{"ws_detached": ""}}
	w := &recordingWriter{}

	n := reconcileBranchRecords(context.Background(), wss, res, w.update, nil)

	if n != 0 {
		t.Fatalf("reconciled %d records from a resolver that resolved nothing, want 0", n)
	}
	if len(w.written) != 0 {
		t.Fatalf("wrote %v; an unresolvable workspace must keep its record", w.written)
	}
}

// A resolver that fails is not evidence about any workspace. Nothing is
// written, and the pass simply happens again next tick.
func TestReconcileWritesNothingWhenTheResolverFails(t *testing.T) {
	res := &fakeRefResolver{err: errors.New("git exploded")}
	w := &recordingWriter{}

	n := reconcileBranchRecords(context.Background(),
		[]pigtail.Workspace{wsWithBranch("ws_a", "feat/x")}, res, w.update, nil)

	if n != 0 || len(w.written) != 0 {
		t.Fatalf("a failed resolve wrote %v (n=%d)", w.written, n)
	}
}

// A workspace with no recorded branch at all is the one case where writing a
// resolved name is pure gain: there was nothing to lose.
func TestReconcileFillsAnEmptyRecord(t *testing.T) {
	ws := pigtail.Workspace{ID: "ws_blank", Path: "/repo/blank", BackendData: map[string]any{}}
	res := &fakeRefResolver{refs: map[string]string{"ws_blank": "feat/found"}}
	w := &recordingWriter{}

	if n := reconcileBranchRecords(context.Background(),
		[]pigtail.Workspace{ws}, res, w.update, nil); n != 1 {
		t.Fatalf("reconciled %d, want 1", n)
	}
	if got := w.written["ws_blank"]["branch"]; got != "feat/found" {
		t.Fatalf("branch = %v, want feat/found", got)
	}
}

// A backend that cannot resolve refs at all (a filesystem or Notion backend,
// or none configured) must make this a no-op rather than an error path — the
// capability is optional, asserted like backend.MergeInspector.
func TestReconcileIsANoOpWithoutAResolver(t *testing.T) {
	w := &recordingWriter{}
	if n := reconcileBranchRecords(context.Background(),
		[]pigtail.Workspace{wsWithBranch("ws_a", "feat/x")}, nil, w.update, nil); n != 0 {
		t.Fatalf("reconciled %d without a resolver, want 0", n)
	}
	if len(w.written) != 0 {
		t.Fatalf("wrote %v without a resolver", w.written)
	}
}

// Each divergence is worth one line — this is exactly the kind of drift that
// is confusing to discover later with no record of when it happened.
func TestReconcileLogsEachDivergence(t *testing.T) {
	var lines []string
	res := &fakeRefResolver{refs: map[string]string{"ws_moved": "feat/new"}}
	w := &recordingWriter{}

	reconcileBranchRecords(context.Background(),
		[]pigtail.Workspace{wsWithBranch("ws_moved", "feat/old")}, res, w.update,
		func(format string, args ...interface{}) { lines = append(lines, format) })

	if len(lines) != 1 {
		t.Fatalf("logged %d lines for one divergence, want 1: %v", len(lines), lines)
	}
}
