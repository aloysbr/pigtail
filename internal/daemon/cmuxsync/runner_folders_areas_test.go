package cmuxsync

import (
	"slices"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
)

// A session whose theme's stored space is no longer declared (a name removed
// from the table) is filed in « Inbox », while declared spaces keep their folder.
func TestUndeclaredSpaceFilesInOther(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Hedge")
	h.sessionIn("WS-B", "Zzgone")
	h.converge(12)
	if got := fw.folderOf("WS-A"); got != "Hedge" {
		t.Fatalf("declared space: WS-A in %q, want Hedge", got)
	}
	if got := fw.folderOf("WS-B"); got != space.NoSubject {
		t.Fatalf("undeclared space: WS-B in %q, want %q", got, space.NoSubject)
	}
}

// The real builtin table, new aires included, files a session of a new aire
// in its folder after the old ones, and « Inbox » / « Standby » stay last.
func TestBuiltinAreasFileInTheirFolder(t *testing.T) {
	t.Parallel()
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	order := tbl.FolderOrder()
	h, fw := newFolderHarness(t, func(_ *Config, d *Deps) {
		d.FolderOrder = func() []string { return slices.Clone(order) }
		d.FolderIcon = tbl.Icon
	})
	h.sessionIn("WS-A", "Garden")
	h.sessionIn("WS-B", "Vineyard")
	h.sessionIn("WS-C", "Allotment")
	h.converge(14)
	for ws, want := range map[string]string{"WS-A": "Garden", "WS-B": "Vineyard", "WS-C": "Allotment"} {
		if got := fw.folderOf(ws); got != want {
			t.Fatalf("%s in %q, want %q", ws, got, want)
		}
	}
	if i, j := slices.Index(order, "Garden"), slices.Index(order, "Vineyard"); i < 0 || j < i {
		t.Fatalf("order %q: Vineyard must follow Garden", order)
	}
}

// TestThemeWithRetiredSpaceFilesInInbox: a workspace bound to a theme whose
// stored space is a pre-taxonomy name (Hedge, written before the retirement)
// is filed in « Inbox » with the real builtin table: spaceFolder finds no such
// folder and falls back to NoSubject, nothing crashes (plan B, task 17).
func TestThemeWithRetiredSpaceFilesInInbox(t *testing.T) {
	t.Parallel()
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	order := tbl.FolderOrder()
	h, fw := newFolderHarness(t, func(_ *Config, d *Deps) {
		d.FolderOrder = func() []string { return slices.Clone(order) }
		d.FolderIcon = tbl.Icon
	})
	h.sessionIn("WS-A", "Hedge")
	h.sessionIn("WS-B", "Vineyard")
	h.converge(14)
	if got := fw.folderOf("WS-A"); got != space.NoSubject {
		t.Fatalf("retired space: WS-A in %q, want %q", got, space.NoSubject)
	}
	if got := fw.folderOf("WS-B"); got != "Vineyard" {
		t.Fatalf("WS-B in %q, want Vineyard", got)
	}
}
