package maintenance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFrozenMarkers(t *testing.T) {
	dir := t.TempDir()
	on := State{ID: "mnt_a", On: true, Freeze: true}
	if err := MarkFrozen(dir, "s1", on.ID); err != nil {
		t.Fatal(err)
	}
	if err := MarkFrozen(dir, "s-old", "mnt_previous"); err != nil {
		t.Fatal(err)
	}
	if got := FrozenSessions(dir, on); len(got) != 1 || !got["s1"] {
		t.Fatalf("frozen = %v", got)
	}
	if got := FrozenSessions(dir, State{ID: "mnt_a", On: true}); len(got) != 0 {
		t.Fatalf("no freeze, no Blocked: %v", got)
	}
	if got := FrozenSessions(dir, State{ID: "mnt_a"}); len(got) != 0 {
		t.Fatalf("lifted, no Blocked: %v", got)
	}
	for _, bad := range []string{"", "../x", "a/b"} {
		if err := MarkFrozen(dir, bad, on.ID); err == nil {
			t.Errorf("session %q accepted", bad)
		}
	}
	if err := ClearFrozen(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FrozenDirName)); !os.IsNotExist(err) {
		t.Fatalf("markers left: %v", err)
	}
	if err := ClearFrozen(dir); err != nil {
		t.Fatalf("clearing nothing is fine: %v", err)
	}
}
