package cmuxsync

import (
	"reflect"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
)

func collapsed(gr cmux.Group, c bool) cmux.Group { gr.Collapsed = c; return gr }

func TestToggleCollapsesWhenOneIsOpen(t *testing.T) {
	t.Parallel()
	gs := []cmux.Group{collapsed(g("M", "Hedge", "GM", true, "A"), false), collapsed(g("P", "Terrace", "GP", true, "B"), true), collapsed(g("S", "Standby", "GS", true, "C"), true)}
	if ops := ToggleFolders(folders, gs, nil); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderCollapse, Group: "M"}}) {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestToggleExpandsAllButStandby(t *testing.T) {
	t.Parallel()
	gs := []cmux.Group{collapsed(g("M", "Hedge", "GM", true, "A"), true), collapsed(g("P", "Terrace", "GP", true, "B"), true), collapsed(g("S", "Standby", "GS", true, "C"), true)}
	want := []FolderOp{{Kind: FolderExpand, Group: "M"}, {Kind: FolderExpand, Group: "P"}}
	if ops := ToggleFolders(folders, gs, nil); !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v, want %+v", ops, want)
	}
}

func TestToggleCollapsesAnOpenStandby(t *testing.T) {
	t.Parallel()
	gs := []cmux.Group{collapsed(g("M", "Hedge", "GM", true, "A"), true), collapsed(g("S", "Standby", "GS", true, "C"), false)}
	if ops := ToggleFolders(folders, gs, nil); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderCollapse, Group: "S"}}) {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestToggleIgnoresFolderWhoseAnchorHostsSession(t *testing.T) {
	t.Parallel()
	gs := []cmux.Group{collapsed(g("M", "Hedge", "GM", true, "A"), false), collapsed(g("P", "Terrace", "GP", true, "B"), true)}
	if ops := ToggleFolders(folders, gs, map[string]bool{"GM": true}); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderExpand, Group: "P"}}) {
		t.Fatalf("a group whose anchor hosts a session is foreign: neither counted nor moved: %+v", ops)
	}
}

func TestToggleIgnoresForeignGroups(t *testing.T) {
	t.Parallel()
	gs := []cmux.Group{collapsed(g("X", "Mine", "GX", true, "A"), false), collapsed(g("M", "Hedge", "GM", true, "B"), true)}
	if ops := ToggleFolders(folders, gs, nil); !reflect.DeepEqual(ops, []FolderOp{{Kind: FolderExpand, Group: "M"}}) {
		t.Fatalf("a foreign group neither counts nor moves: %+v", ops)
	}
}
