package cmuxsync

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestDiff(t *testing.T) {
	t.Parallel()
	waiting := Desired{WorkspaceID: wsA, Attention: AttentionWaiting, WaitingCount: 1}
	waitPill := waiting.Statuses()[KeyAttention]
	deadPill := Desired{Attention: AttentionDead}.Statuses()[KeyAttention]
	foreign := cmux.StatusEntry{Key: "claude_code", Value: "Running"}

	cases := []struct {
		name    string
		desired map[string]Desired
		applied map[string]Applied
		want    []Op
	}{
		{"nothing wanted, nothing posed", map[string]Desired{wsA: {WorkspaceID: wsA}}, nil, nil},
		{"pill appears", map[string]Desired{wsA: waiting}, nil,
			[]Op{{Kind: OpSetStatus, WorkspaceID: wsA, Status: waitPill}}},
		{"same pill already posed: no op", map[string]Desired{wsA: waiting},
			map[string]Applied{wsA: {Statuses: map[string]cmux.StatusEntry{KeyAttention: waitPill}}}, nil},
		{"pill changes value", map[string]Desired{wsA: {WorkspaceID: wsA, Attention: AttentionDead}},
			map[string]Applied{wsA: {Statuses: map[string]cmux.StatusEntry{KeyAttention: waitPill}}},
			[]Op{{Kind: OpSetStatus, WorkspaceID: wsA, Status: deadPill}}},
		{"stale pill (persisted by cmux) is cleared", map[string]Desired{wsA: {WorkspaceID: wsA}},
			map[string]Applied{wsA: SeedApplied([]cmux.StatusEntry{deadPill, foreign, {Key: KeyServers, Value: "▶ dev :5173 up"}}, nil)},
			[]Op{
				{Kind: OpClearStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: KeyAttention}},
				{Kind: OpClearStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: KeyServers}},
			}},
		{"foreign keys are never touched", map[string]Desired{wsA: {WorkspaceID: wsA}},
			map[string]Applied{wsA: {Statuses: map[string]cmux.StatusEntry{"claude_code": foreign, cmux.InboxStatusKey: {Key: cmux.InboxStatusKey, Value: "✉ 2"}}}}, nil},
		{"rename + description on first pass, in order",
			map[string]Desired{wsA: {WorkspaceID: wsA, Title: "Sidebar pigtail", Description: "Method · spec", Attention: AttentionWaiting, WaitingCount: 1}}, nil,
			[]Op{
				{Kind: OpRename, WorkspaceID: wsA, Text: "Sidebar pigtail"},
				{Kind: OpSetDescription, WorkspaceID: wsA, Text: "Method · spec"},
				{Kind: OpSetStatus, WorkspaceID: wsA, Status: waitPill},
			}},
		{"title already posed: no rename", map[string]Desired{wsA: {WorkspaceID: wsA, Title: "Sidebar pigtail", Description: "Method · spec"}},
			map[string]Applied{wsA: {Title: "Sidebar pigtail", Description: "Method · spec"}}, nil},
		{"short title changed: rename", map[string]Desired{wsA: {WorkspaceID: wsA, Title: "Nouveau", Description: "Method · spec"}},
			map[string]Applied{wsA: {Title: "Sidebar pigtail", Description: "Method · spec"}},
			[]Op{{Kind: OpRename, WorkspaceID: wsA, Text: "Nouveau"}}},
		{"empty desired title leaves a posed one alone (manual takeover)", map[string]Desired{wsA: {WorkspaceID: wsA}},
			map[string]Applied{wsA: {Title: "Sidebar pigtail", Description: "Method · spec"}}, nil},
		{"closed workspace (absent from desired): nothing to write", map[string]Desired{},
			map[string]Applied{wsA: {Title: "x", Statuses: map[string]cmux.StatusEntry{KeyAttention: waitPill}}}, nil},
		{"workspaces in id order", map[string]Desired{wsB: {WorkspaceID: wsB, Attention: AttentionDead}, wsA: waiting}, nil,
			[]Op{{Kind: OpSetStatus, WorkspaceID: wsA, Status: waitPill}, {Kind: OpSetStatus, WorkspaceID: wsB, Status: deadPill}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Diff(c.desired, c.applied); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ops =\n%+v\nwant\n%+v", got, c.want)
			}
		})
	}
}

// Applying a state, then diffing the same state again, costs zero ops.
func TestDiffIdempotent(t *testing.T) {
	t.Parallel()
	desired := map[string]Desired{
		wsA: {WorkspaceID: wsA, Title: "Sidebar pigtail", Description: "Method · spec · #2", Attention: AttentionWaiting, WaitingCount: 2},
		wsB: {WorkspaceID: wsB, Attention: AttentionDead},
	}
	applied := map[string]Applied{wsB: SeedApplied([]cmux.StatusEntry{{Key: KeyServers, Value: "stale"}}, nil)}
	first := Diff(desired, applied)
	if len(first) == 0 {
		t.Fatal("first pass must write")
	}
	for _, op := range first {
		Record(applied, op)
	}
	if again := Diff(desired, applied); len(again) != 0 {
		t.Fatalf("second pass wrote %+v", again)
	}
}

// fakeSidebar records every cmux call the ops make.
type fakeSidebar struct {
	calls []string
	fail  error
}

func (f *fakeSidebar) rec(s ...string) error {
	f.calls = append(f.calls, strings.Join(s, " "))
	return f.fail
}
func (f *fakeSidebar) Rename(ws, t string) error         { return f.rec("rename", ws, t) }
func (f *fakeSidebar) ClearName(ws string) error         { return f.rec("clear-name", ws) }
func (f *fakeSidebar) SetDescription(ws, d string) error { return f.rec("set-description", ws, d) }
func (f *fakeSidebar) ClearDescription(ws string) error  { return f.rec("clear-description", ws) }
func (f *fakeSidebar) SetStatus(ws string, e cmux.StatusEntry) error {
	return f.rec("set-status", ws, e.Key, e.Value)
}
func (f *fakeSidebar) ClearStatus(ws, key string) error { return f.rec("clear-status", ws, key) }
func (f *fakeSidebar) ListStatus(string) ([]cmux.StatusEntry, error) {
	return nil, f.rec("list-status")
}
func (f *fakeSidebar) ListWindows() ([]cmux.Window, error) { return nil, f.rec("list-windows") }
func (f *fakeSidebar) ListWorkspaces(string) ([]cmux.WorkspaceInfo, error) {
	return nil, f.rec("workspace-list")
}
func (f *fakeSidebar) ListSurfaces(string) (map[string][]string, error) {
	return nil, f.rec("tree")
}

// Key ownership: whatever reaches the ops — a hand-built op included —
// nothing outside {pigtail-attn, pigtail-srv} is ever written or cleared.
func TestKeyOwnership(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"claude_code", cmux.InboxStatusKey, "pigtail-other", ""} {
		for _, kind := range []OpKind{OpSetStatus, OpClearStatus} {
			f := &fakeSidebar{}
			err := Op{Kind: kind, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: key, Value: "v"}}.Apply(f)
			if !errors.Is(err, ErrNotOwned) || len(f.calls) != 0 {
				t.Errorf("%s %q: err=%v calls=%q", kind, key, err, f.calls)
			}
		}
	}

	// End to end: every op Diff/RestoreOps/ClearOwnedOps can produce, from
	// states full of foreign keys, only ever touches owned keys.
	foreign := []cmux.StatusEntry{{Key: "claude_code", Value: "Running"}, {Key: cmux.InboxStatusKey, Value: "✉ 1"}, {Key: KeyAttention, Value: "stale"}}
	applied := map[string]Applied{wsA: SeedApplied(foreign, nil), wsB: {Statuses: map[string]cmux.StatusEntry{"claude_code": foreign[0]}}}
	desired := map[string]Desired{wsA: {WorkspaceID: wsA}, wsB: {WorkspaceID: wsB, Attention: AttentionWaiting, WaitingCount: 1}}
	b := binding(wsA)
	b.AppliedTitle, b.AppliedDescription = "t", "d"
	ops := append(Diff(desired, applied), RestoreOps(b)...)
	ops = append(ops, ClearOwnedOps(applied)...)
	f := &fakeSidebar{}
	for _, op := range ops {
		if err := op.Apply(f); err != nil {
			t.Fatalf("%+v: %v", op, err)
		}
	}
	for _, c := range f.calls {
		if strings.Contains(c, "claude_code") || strings.Contains(c, cmux.InboxStatusKey) {
			t.Errorf("foreign key written: %q", c)
		}
	}
	if len(f.calls) == 0 {
		t.Fatal("expected some calls")
	}
}

// Decision of 2026-09-30: no op the engine can produce reorders anything.
func TestNoOpReorders(t *testing.T) {
	t.Parallel()
	for _, k := range []OpKind{OpSetStatus, OpClearStatus, OpRename, OpClearName, OpSetDescription, OpClearDescription} {
		if strings.Contains(string(k), "reorder") || strings.Contains(string(k), "move") {
			t.Errorf("op kind %q reorders", k)
		}
	}
	f := &fakeSidebar{}
	if err := (Op{Kind: "reorder-workspaces", WorkspaceID: wsA}).Apply(f); err == nil || len(f.calls) != 0 {
		t.Fatalf("unknown op kind reached cmux: err=%v calls=%q", err, f.calls)
	}
}

func TestRecordIgnoresForeignKeys(t *testing.T) {
	t.Parallel()
	applied := map[string]Applied{}
	Record(applied, Op{Kind: OpSetStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: "claude_code", Value: "x"}})
	if len(applied[wsA].Statuses) != 0 {
		t.Fatalf("foreign key recorded: %+v", applied)
	}
	Record(applied, Op{Kind: OpRename, WorkspaceID: wsA, Text: "t"})
	Record(applied, Op{Kind: OpSetDescription, WorkspaceID: wsA, Text: "d"})
	Record(applied, Op{Kind: OpClearName, WorkspaceID: wsA})
	Record(applied, Op{Kind: OpClearDescription, WorkspaceID: wsA})
	if a := applied[wsA]; a.Title != "" || a.Description != "" {
		t.Fatalf("applied = %+v", a)
	}
}

func TestSeedAppliedFromBinding(t *testing.T) {
	t.Parallel()
	b := binding(wsA)
	b.AppliedTitle, b.AppliedDescription = "Sidebar pigtail", "Method · spec"
	a := SeedApplied(nil, &b)
	if a.Title != "Sidebar pigtail" || a.Description != "Method · spec" || a.Statuses == nil {
		t.Fatalf("seed = %+v", a)
	}
}

func TestRestoreOps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		b    func(*pigtail.SubjectCmuxBinding)
		want []Op
	}{
		{"auto title before: clear-name returns the live title", func(b *pigtail.SubjectCmuxBinding) {
			b.AppliedTitle, b.AppliedDescription = "Sidebar pigtail", "Method · spec"
		}, []Op{{Kind: OpClearName, WorkspaceID: wsA}, {Kind: OpClearDescription, WorkspaceID: wsA}}},
		{"custom title before: put it back", func(b *pigtail.SubjectCmuxBinding) {
			b.AppliedTitle, b.PrevTitle, b.PrevTitleCustom = "Sidebar pigtail", "Metric login", true
			b.AppliedDescription, b.PrevDescription = "Method · spec", "Ship checklist"
		}, []Op{{Kind: OpRename, WorkspaceID: wsA, Text: "Metric login"}, {Kind: OpSetDescription, WorkspaceID: wsA, Text: "Ship checklist"}}},
		{"user-owned fields are left alone", func(b *pigtail.SubjectCmuxBinding) {
			b.AppliedTitle, b.AppliedDescription = "Sidebar pigtail", "Method · spec"
			b.TitleOwner, b.DescriptionOwner = pigtail.CmuxOwnerUser, pigtail.CmuxOwnerUser
		}, nil},
		{"nothing posed: nothing to restore", func(*pigtail.SubjectCmuxBinding) {}, nil},
	}
	for _, c := range cases {
		b := binding(wsA)
		c.b(&b)
		if got := RestoreOps(b); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestClearOwnedOps(t *testing.T) {
	t.Parallel()
	applied := map[string]Applied{
		wsB: {Statuses: map[string]cmux.StatusEntry{KeyServers: {Key: KeyServers}}},
		wsA: {Statuses: map[string]cmux.StatusEntry{KeyAttention: {Key: KeyAttention}, "claude_code": {Key: "claude_code"}}},
	}
	want := []Op{
		{Kind: OpClearStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: KeyAttention}},
		{Kind: OpClearStatus, WorkspaceID: wsB, Status: cmux.StatusEntry{Key: KeyServers}},
	}
	if got := ClearOwnedOps(applied); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}
