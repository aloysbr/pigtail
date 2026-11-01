package cmuxsync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Spec 7: the folded state of each folder, by name, survives a daemon
// restart in a JSON file next to the database.
func TestCollapseFileRoundTrip(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cmux-folders.json")
	want := map[string]bool{"Terrace": true, "Hedge": false}
	if err := SaveCollapseFile(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCollapseFile(p)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadCollapseFile = %v, %v; want %v", got, err, want)
	}
	if left, _ := filepath.Glob(p + ".*"); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestCollapseFileAbsentIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := LoadCollapseFile(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("LoadCollapseFile(absent) = %v, %v; want an empty map, nil", got, err)
	}
}

func TestCollapseFileInvalidIsError(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cmux-folders.json")
	if err := os.WriteFile(p, []byte(`{"Terrace": true, "Hedge": `), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCollapseFile(p)
	if err == nil || got != nil {
		t.Fatalf("invalid JSON: %v, %v; want nil, an error (never a partial map)", got, err)
	}
}
