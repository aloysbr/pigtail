package tmpsweep_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/tmpsweep"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The sweeper must recognise what the test helper makes: the base name of a
// testutil.MkdirTemp dir, recreated under t.TempDir() and aged, is swept
// (never a sweep of the real /tmp).
func TestSweepsWhatTestutilMakes(t *testing.T) {
	made := testutil.MkdirTemp(t, "recognise-")
	name := filepath.Base(made)
	root := t.TempDir()
	d := filepath.Join(root, name)
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(d, old, old); err != nil {
		t.Fatal(err)
	}
	s := tmpsweep.New(tmpsweep.ModeOn)
	s.Dirs = []string{root}
	s.Journal = func(pigtail.JournalEntry) error { return nil }
	s.Tick(time.Now())
	if _, err := os.Lstat(d); !os.IsNotExist(err) {
		t.Fatalf("%s survived: %v", name, err)
	}
}
