package sysdisk

import (
	"os"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestFreeIsPositiveAndBelowTotal(t *testing.T) {
	dir := testutil.MkdirTemp(t, "sysdisk-")
	free, err := Free(dir)
	if err != nil {
		t.Fatalf("Free: %v", err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	total := uint64(st.Blocks) * uint64(st.Bsize)
	if free == 0 || free > total {
		t.Fatalf("free = %d, want in (0, %d]", free, total)
	}
}

func TestFreeMissingPathErrors(t *testing.T) {
	if _, err := Free("/nonexistent-pigtail-path/x"); err == nil {
		t.Fatal("want an error for a missing path")
	}
}

func TestFreeMinOverDistinctVolumes(t *testing.T) {
	dir := testutil.MkdirTemp(t, "sysdisk-")
	one, err := Free(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The same volume listed twice is counted once and gives the same value
	// (within noise: the free space can move between two reads).
	got, err := FreeMin(dir, dir)
	if err != nil {
		t.Fatalf("FreeMin: %v", err)
	}
	diff := int64(got) - int64(one)
	if diff < -(1<<30) || diff > 1<<30 {
		t.Fatalf("FreeMin = %d, Free = %d", got, one)
	}
}

func TestFreeMinNoPathsErrors(t *testing.T) {
	if _, err := FreeMin(); err == nil {
		t.Fatal("want an error without paths")
	}
}

func TestFreeMinSkipsUnreadableButFailsWhenAllFail(t *testing.T) {
	if _, err := FreeMin("/nonexistent-pigtail-path/a", "/nonexistent-pigtail-path/b"); err == nil {
		t.Fatal("want an error when every path fails")
	}
	home, _ := os.UserHomeDir()
	if _, err := FreeMin("/nonexistent-pigtail-path/a", home); err != nil {
		t.Fatalf("one readable path must suffice: %v", err)
	}
}

func TestFreeMinDefaultVolumes(t *testing.T) {
	if _, err := FreeMinDefault(); err != nil {
		t.Fatalf("FreeMinDefault: %v", err)
	}
}
