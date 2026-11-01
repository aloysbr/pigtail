package cmuxsync

import (
	"slices"
	"testing"
	"time"
)

// The simulated cmux's membership lag and hand drag (Tâche 7,
// sub_x000000000000029): the engine's group-add / group-remove may show in
// the group list a moment late (addLag, cmux probe P22); a drag by hand is no
// recorded write, seen by the next listing.

// listedGroupOf is the id of the group ws is listed in by ListGroups, "" if none.
func listedGroupOf(t *testing.T, fw *folderWorld, ws string) string {
	t.Helper()
	gs, err := fw.ListGroups(win)
	if err != nil {
		t.Fatal(err)
	}
	for _, gr := range gs {
		if gr.AnchorID != ws && slices.Contains(gr.Members, ws) {
			return gr.ID
		}
	}
	return ""
}

func TestHarnessAddLag(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.add(win, "WS-A", "WS-A")
	fw.add(win, "WS-B", "WS-B")
	fw.manualGroup("G1", "One", "ANC-1", true)
	fw.manualGroup("G2", "Two", "ANC-2", true, "WS-B")
	fw.addLag = 1500 * time.Millisecond

	if err := fw.AddToGroup(win, "G1", "WS-A"); err != nil {
		t.Fatal(err)
	}
	if got := fw.folderOf("WS-A"); got != "One" {
		t.Fatalf("the world itself holds the add at once: WS-A in %q", got)
	}
	if got := listedGroupOf(t, fw, "WS-A"); got != "" {
		t.Fatalf("within addLag the list shows the previous membership (none), got %q", got)
	}
	h.now = h.now.Add(fw.addLag)
	if got := listedGroupOf(t, fw, "WS-A"); got != "G1" {
		t.Fatalf("after addLag the list shows the add, got %q", got)
	}

	// a member moved from one group to another: the old group is listed
	if err := fw.AddToGroup(win, "G1", "WS-B"); err != nil {
		t.Fatal(err)
	}
	if got := listedGroupOf(t, fw, "WS-B"); got != "G2" {
		t.Fatalf("within addLag a moved member is listed in its previous group, got %q", got)
	}
	h.now = h.now.Add(fw.addLag)
	if got := listedGroupOf(t, fw, "WS-B"); got != "G1" {
		t.Fatalf("after addLag the moved member is in its new group, got %q", got)
	}

	if err := fw.RemoveFromGroup(win, "WS-A"); err != nil {
		t.Fatal(err)
	}
	if got := listedGroupOf(t, fw, "WS-A"); got != "G1" {
		t.Fatalf("within addLag a removed member is still listed, got %q", got)
	}
	h.now = h.now.Add(fw.addLag)
	if got := listedGroupOf(t, fw, "WS-A"); got != "" {
		t.Fatalf("after addLag the remove shows, got %q", got)
	}

	// addLag zero (the default): every write shows at once
	fw.addLag = 0
	if err := fw.AddToGroup(win, "G2", "WS-A"); err != nil {
		t.Fatal(err)
	}
	if got := listedGroupOf(t, fw, "WS-A"); got != "G2" {
		t.Fatalf("addLag 0: the add shows at once, got %q", got)
	}
}

func TestHarnessDragIsNotAWrite(t *testing.T) {
	t.Parallel()
	for _, lag := range []time.Duration{0, 1500 * time.Millisecond} {
		t.Run(lag.String(), func(t *testing.T) {
			_, fw := newFolderHarness(t)
			fw.addLag = lag
			fw.add(win, "WS-A", "WS-A")
			fw.manualGroup("G1", "One", "ANC-1", true, "WS-X")
			fw.manualGroup("G2", "Two", "ANC-2", true, "WS-Y")
			fw.add(win, "WS-F", "WS-F") // a free workspace under the last block
			before := len(fw.folderWrites())

			fw.drag("WS-A", "G2")
			if got := fw.folderOf("WS-A"); got != "Two" {
				t.Fatalf("drag: WS-A in %q, want Two", got)
			}
			order := fw.windows[win]
			if i, y := slices.Index(order, "WS-A"), slices.Index(order, "WS-Y"); i != y+1 {
				t.Fatalf("drag: WS-A lands at the end of the target block, order %q", order)
			}
			if got := listedGroupOf(t, fw, "WS-A"); got != "G2" {
				t.Fatalf("drag: the next list shows it at once, got %q", got)
			}

			fw.drag("WS-A", "G1") // from one group to another
			if got := listedGroupOf(t, fw, "WS-A"); got != "G1" {
				t.Fatalf("drag G2 → G1: listed in %q", got)
			}
			order = fw.windows[win]
			if i, x := slices.Index(order, "WS-A"), slices.Index(order, "WS-X"); i != x+1 {
				t.Fatalf("drag G2 → G1: WS-A at the end of One, order %q", order)
			}

			fw.dragOut("WS-A")
			if got := fw.folderOf("WS-A"); got != "" {
				t.Fatalf("dragOut: WS-A in %q, want no folder", got)
			}
			if got := listedGroupOf(t, fw, "WS-A"); got != "" {
				t.Fatalf("dragOut: the next list shows it free, got %q", got)
			}
			order = fw.windows[win]
			if i, y := slices.Index(order, "WS-A"), slices.Index(order, "WS-Y"); i != y+1 {
				t.Fatalf("dragOut: WS-A free right under the last block, order %q", order)
			}
			if w := fw.folderWrites(); len(w) != before {
				t.Fatalf("a drag is no write: %q", w[before:])
			}
		})
	}
}
