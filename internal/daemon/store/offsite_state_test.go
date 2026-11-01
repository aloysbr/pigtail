package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// `pigtail db list` runs with the daemon stopped — that is the whole point of
// the command — so it cannot ask the daemon how the offsite copy is doing. The
// daemon leaves the answer in a file beside the database instead, on the model
// of rammon-state.json.
//
// Without it, a NAS unplugged three weeks ago is one log line an hour that
// nobody reads, and `db list` keeps showing three healthy generations as if
// everything were protected.

func TestOffsiteStateRoundTrips(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	want := OffsiteState{
		DestDir:    "/Volumes/nas/pigtail",
		LastOKAt:   at,
		LastTryAt:  at,
		SameDevice: true,
	}
	if err := WriteOffsiteState(db, want); err != nil {
		t.Fatalf("WriteOffsiteState: %v", err)
	}

	got := ReadOffsiteState(db)
	if got.DestDir != want.DestDir {
		t.Errorf("DestDir = %q, want %q", got.DestDir, want.DestDir)
	}
	if !got.LastOKAt.Equal(at) {
		t.Errorf("LastOKAt = %v, want %v", got.LastOKAt, at)
	}
	if !got.SameDevice {
		t.Error("SameDevice = false, want true")
	}
	if !got.Configured() {
		t.Error("Configured() = false for a state with a destination")
	}
}

// Every read failure degrades to "not configured" rather than to an error: the
// state is a report, and a missing or corrupt report must not make `db list`
// fail on a machine whose database is fine.
func TestOffsiteStateReadsAsUnconfiguredWhenAbsentOrJunk(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	if got := ReadOffsiteState(db); got.Configured() {
		t.Error("a missing state file reported as configured")
	}

	if err := os.WriteFile(OffsiteStatePath(db), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadOffsiteState(db); got.Configured() {
		t.Error("a corrupt state file reported as configured")
	}
}

// A state file from another shape is discarded rather than guessed at — the
// same rule rammon-state.json follows.
func TestOffsiteStateDiscardsAnotherVersion(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	if err := os.WriteFile(OffsiteStatePath(db),
		[]byte(`{"version":99,"dest_dir":"/Volumes/nas"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadOffsiteState(db); got.Configured() {
		t.Error("a state file from another version was accepted")
	}
}

// An in-memory database has no directory to write beside, so state is off
// rather than an error path nobody can act on.
func TestOffsiteStatePathIsEmptyWithoutAFileDatabase(t *testing.T) {
	if p := OffsiteStatePath(""); p != "" {
		t.Errorf("OffsiteStatePath(\"\") = %q, want empty", p)
	}
	if err := WriteOffsiteState("", OffsiteState{DestDir: "/x"}); err != nil {
		t.Errorf("WriteOffsiteState on an unnamed database: %v", err)
	}
}

// ConsecutiveFailures round-trips like any other field.
func TestOffsiteStateRoundTripsConsecutiveFailures(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")

	want := OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		LastTryAt:           time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC),
		LastErr:             "offsite destination unreachable",
		ConsecutiveFailures: 3,
	}
	if err := WriteOffsiteState(db, want); err != nil {
		t.Fatalf("WriteOffsiteState: %v", err)
	}

	got := ReadOffsiteState(db)
	if got.ConsecutiveFailures != 3 {
		t.Errorf("ConsecutiveFailures = %d, want 3", got.ConsecutiveFailures)
	}
}

// A state file from before this field existed — Task 1's format, still
// version 1 — must read back with a zero count rather than fail or be
// discarded. This is the whole point of adding the field without bumping
// offsiteStateVersion: an older writer and a newer reader (or vice versa)
// must stay compatible.
func TestOffsiteStateWithoutConsecutiveFailuresReadsAsZero(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	body := `{"version":1,"dest_dir":"/Volumes/nas/pigtail","last_try_at":"2026-09-10T09:00:00Z"}`
	if err := os.WriteFile(OffsiteStatePath(db), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ReadOffsiteState(db)
	if !got.Configured() {
		t.Fatal("a valid version-1 file without the new field was discarded")
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 for a file predating the field", got.ConsecutiveFailures)
	}
}

// Pins the version constant (plan constraint 3): a field added to the struct
// must stay compatible in both directions. Bumping offsiteStateVersion would
// make an older `pigtail db list` blind to every state file a newer daemon
// writes, which is exactly the failure the version check exists to prevent.
func TestOffsiteStateVersionStaysAtOne(t *testing.T) {
	if offsiteStateVersion != 1 {
		t.Fatalf("offsiteStateVersion = %d, want 1; adding ConsecutiveFailures must not require a version bump",
			offsiteStateVersion)
	}
}

// The two failure shapes must stay distinguishable in the report: "the volume
// is not mounted" is a Tuesday, "the write failed" is a problem.
func TestOffsiteStateKeepsTheLastError(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	if err := WriteOffsiteState(db, OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastTryAt: at,
		LastErr:   "offsite destination unreachable: /Volumes/nas/pigtail",
	}); err != nil {
		t.Fatal(err)
	}
	got := ReadOffsiteState(db)
	if got.LastErr == "" {
		t.Fatal("LastErr was dropped")
	}
	if !got.LastOKAt.IsZero() {
		t.Errorf("LastOKAt = %v, want zero for a state that never succeeded", got.LastOKAt)
	}
}
