package cmuxsync

import (
	"errors"
	"fmt"
	"sort"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// OpKind is one sidebar write. There is deliberately no reorder kind.
type OpKind string

// Op kinds, one per cmux primitive.
const (
	OpSetStatus        OpKind = "set-status"
	OpClearStatus      OpKind = "clear-status"
	OpRename           OpKind = "rename"
	OpClearName        OpKind = "clear-name"
	OpSetDescription   OpKind = "set-description"
	OpClearDescription OpKind = "clear-description"
)

// Op is one write on one workspace. Status is set for the status kinds
// (only Key for a clear), Text for rename and set-description.
type Op struct {
	Kind        OpKind
	WorkspaceID string
	Status      cmux.StatusEntry
	Text        string
}

// ErrNotOwned refuses a status op on a key the loop does not own.
var ErrNotOwned = errors.New("cmuxsync: status key not owned by pigtail")

// Apply performs the op through sb. A status op on a key outside OwnedKeys
// is refused before cmux is called, whatever built the op. The one exception
// is OpClaudeCodeIdle (temporary patch, claudecode_patch.go), which writes
// cmux's native Idle row and nothing else, whatever Status it carries.
func (o Op) Apply(sb cmux.Sidebar) error {
	switch o.Kind {
	case OpSetStatus, OpClearStatus:
		if !Owns(o.Status.Key) {
			return fmt.Errorf("%w: %q", ErrNotOwned, o.Status.Key)
		}
	}
	switch o.Kind {
	case OpSetStatus:
		return sb.SetStatus(o.WorkspaceID, o.Status)
	case OpClearStatus:
		return sb.ClearStatus(o.WorkspaceID, o.Status.Key)
	case OpRename:
		return sb.Rename(o.WorkspaceID, o.Text)
	case OpClearName:
		return sb.ClearName(o.WorkspaceID)
	case OpSetDescription:
		return sb.SetDescription(o.WorkspaceID, o.Text)
	case OpClearDescription:
		return sb.ClearDescription(o.WorkspaceID)
	case OpClaudeCodeIdle:
		return sb.SetStatus(o.WorkspaceID, claudeCodeIdle)
	}
	return fmt.Errorf("cmuxsync: unknown op kind %q", o.Kind)
}

// Diff lists the minimal writes that take the sidebar from applied to
// desired (spec §4.2). Equal states give no op, so applying a state twice
// costs nothing. Only desired workspaces are considered: one missing from it
// is closed, and cmux dropped its metadata with it. Order is deterministic:
// by workspace, then title, description, owned keys.
func Diff(desired map[string]Desired, applied map[string]Applied) []Op {
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var ops []Op
	for _, id := range ids {
		d, a := desired[id], applied[id]
		if d.Title != "" && d.Title != a.Title {
			ops = append(ops, Op{Kind: OpRename, WorkspaceID: id, Text: d.Title})
		}
		if d.Description != "" && d.Description != a.Description {
			ops = append(ops, Op{Kind: OpSetDescription, WorkspaceID: id, Text: d.Description})
		}
		if d.ClearDescription && a.Description != "" {
			ops = append(ops, Op{Kind: OpClearDescription, WorkspaceID: id})
		}
		want := d.Statuses()
		for _, key := range OwnedKeys {
			w, wok := want[key]
			h, hok := a.Statuses[key]
			switch {
			case wok && (!hok || w != h):
				ops = append(ops, Op{Kind: OpSetStatus, WorkspaceID: id, Status: w})
			case !wok && hok:
				ops = append(ops, Op{Kind: OpClearStatus, WorkspaceID: id, Status: cmux.StatusEntry{Key: key}})
			}
		}
	}
	return ops
}

// Record folds a successful op into the applied state. The runner calls it
// only after cmux said OK, so a failed write is retried on the next pass.
func Record(applied map[string]Applied, op Op) {
	a := applied[op.WorkspaceID]
	switch op.Kind {
	case OpSetStatus, OpClearStatus:
		if !Owns(op.Status.Key) {
			return
		}
		st := make(map[string]cmux.StatusEntry, len(a.Statuses)+1)
		for k, v := range a.Statuses {
			st[k] = v
		}
		if op.Kind == OpSetStatus {
			st[op.Status.Key] = op.Status
		} else {
			delete(st, op.Status.Key)
		}
		a.Statuses = st
	case OpRename:
		a.Title = op.Text
	case OpClearName:
		a.Title = ""
	case OpSetDescription:
		a.Description = op.Text
	case OpClearDescription:
		a.Description = ""
	}
	applied[op.WorkspaceID] = a
}

// SeedApplied builds a workspace's applied state from what cmux really shows
// (`list-status`) and what the binding says pigtail posed. Foreign keys are
// dropped: a stale pigtail pill persisted by cmux (spec C3) is then cleared
// by the next Diff, a foreign one is never looked at.
func SeedApplied(statuses []cmux.StatusEntry, b *pigtail.SubjectCmuxBinding) Applied {
	a := Applied{Statuses: map[string]cmux.StatusEntry{}}
	for _, e := range statuses {
		if Owns(e.Key) {
			a.Statuses[e.Key] = e
		}
	}
	if b != nil {
		a.Title, a.Description = b.AppliedTitle, b.AppliedDescription
	}
	return a
}

// RestoreOps gives a workspace back when its link goes (detach, sidebar
// switched off): the title and description pigtail posed are replaced by
// what was there before, or cleared so cmux's live automatic title returns
// (spec C1, §4.5). A field the user took back is left alone.
func RestoreOps(b pigtail.SubjectCmuxBinding) []Op {
	var ops []Op
	if b.TitleOwner != pigtail.CmuxOwnerUser && b.AppliedTitle != "" {
		if b.PrevTitleCustom && sidebarText(b.PrevTitle) != "" {
			ops = append(ops, Op{Kind: OpRename, WorkspaceID: b.CmuxWorkspaceID, Text: sidebarText(b.PrevTitle)})
		} else {
			ops = append(ops, Op{Kind: OpClearName, WorkspaceID: b.CmuxWorkspaceID})
		}
	}
	if b.DescriptionOwner != pigtail.CmuxOwnerUser && b.AppliedDescription != "" {
		if prev := sidebarText(b.PrevDescription); prev != "" {
			ops = append(ops, Op{Kind: OpSetDescription, WorkspaceID: b.CmuxWorkspaceID, Text: prev})
		} else {
			ops = append(ops, Op{Kind: OpClearDescription, WorkspaceID: b.CmuxWorkspaceID})
		}
	}
	return ops
}

// ClearOwnedOps clears every owned pill pigtail believes it posed (sidebar
// switched off, self-disable, clean shutdown).
func ClearOwnedOps(applied map[string]Applied) []Op {
	ids := make([]string, 0, len(applied))
	for id := range applied {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var ops []Op
	for _, id := range ids {
		for _, key := range OwnedKeys {
			if _, ok := applied[id].Statuses[key]; ok {
				ops = append(ops, Op{Kind: OpClearStatus, WorkspaceID: id, Status: cmux.StatusEntry{Key: key}})
			}
		}
	}
	return ops
}
