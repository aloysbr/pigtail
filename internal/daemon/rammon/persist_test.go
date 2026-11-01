package rammon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rammon-state.json")
	at := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	want := persistedState{
		Version: stateVersion,
		SavedAt: at,
		Entries: []persistedEntry{
			{Key: "ws1:dev", LastCPU: 42.5, LastActive: at.Add(-time.Hour), Port: 3030, PIDs: []int{100, 101}},
		},
		Deaths: []Death{{WorkspaceID: "ws2", Server: "storybook", Port: 6006, At: at.Add(-time.Minute)}},
	}

	if err := writeStateFile(path, want); err != nil {
		t.Fatalf("writeStateFile: %v", err)
	}
	got, err := readStateFile(path)
	if err != nil {
		t.Fatalf("readStateFile: %v", err)
	}

	if len(got.Entries) != 1 || got.Entries[0].Key != "ws1:dev" ||
		got.Entries[0].LastCPU != 42.5 || got.Entries[0].Port != 3030 ||
		!got.Entries[0].LastActive.Equal(at.Add(-time.Hour)) ||
		len(got.Entries[0].PIDs) != 2 {
		t.Fatalf("entries round-tripped as %+v", got.Entries)
	}
	if len(got.Deaths) != 1 || got.Deaths[0].Server != "storybook" || got.Deaths[0].Port != 6006 {
		t.Fatalf("deaths round-tripped as %+v", got.Deaths)
	}
}

// The file records which sessions ran what, next to the database in the user's
// config dir. It is written by a long-lived daemon and read by nobody else.
func TestStateFileIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rammon-state.json")
	if err := writeStateFile(path, persistedState{Version: stateVersion}); err != nil {
		t.Fatalf("writeStateFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file mode = %04o, want 0600", perm)
	}
}

// A save must never leave a half-written file where the next daemon start would
// read it: the temp+rename is the same reasoning as writeOwner's.
func TestStateFileLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rammon-state.json")
	if err := writeStateFile(path, persistedState{Version: stateVersion}); err != nil {
		t.Fatalf("writeStateFile: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "rammon-state.json" {
		names := make([]string, len(ents))
		for i, e := range ents {
			names[i] = e.Name()
		}
		t.Fatalf("dir contains %v, want only the state file", names)
	}
}

// Every read failure is a reason to start from a blank slate, never to fail
// starting: the file is a cache whose loss costs exactly today's behaviour.
func TestReadStateFileRejectsUnusableFiles(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing", func(t *testing.T) {
		if _, err := readStateFile(filepath.Join(dir, "nope.json")); err == nil {
			t.Fatal("want an error for a missing file")
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		p := filepath.Join(dir, "corrupt.json")
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStateFile(p); err == nil {
			t.Fatal("want an error for a corrupt file")
		}
	})

	t.Run("version from another build", func(t *testing.T) {
		p := filepath.Join(dir, "v99.json")
		if err := writeStateFile(p, persistedState{Version: stateVersion + 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := readStateFile(p); err == nil {
			t.Fatal("want an error for an unknown state version")
		}
	})
}

func TestStateFileRoundTripKeepsWindowAndPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rammon-state.json")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	want := persistedState{Version: stateVersion, SavedAt: at, Entries: []persistedEntry{{
		Key: "ws1:dev", LastCPU: 11, LastActive: at, Port: 3030, PIDs: []int{100},
		Samples:    []persistedSample{{At: at.Add(-16 * time.Second), CPU: 10}, {At: at, CPU: 11, NewConns: 1}},
		Peers:      []string{"127.0.0.1:50001"},
		PeersKnown: true,
	}}}
	if err := writeStateFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readStateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entries[0]
	if len(e.Samples) != 2 || e.Samples[1].NewConns != 1 || !e.Samples[0].At.Equal(at.Add(-16*time.Second)) ||
		len(e.Peers) != 1 || !e.PeersKnown {
		t.Fatalf("entry round-tripped as %+v", e)
	}
}

// Pins ride in the same file, additively: version unchanged.
func TestStateFileRoundTripKeepsPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rammon-state.json")
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	want := persistedState{Version: stateVersion, SavedAt: at, Pins: []persistedPin{
		{Key: "ws1:dev", Durable: true},
		{Key: "ws2:storybook", Until: at.Add(time.Hour)},
	}}
	if err := writeStateFile(path, want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"until":"0001-01-01`) {
		t.Fatalf("a durable pin must not write a zero until: %s", raw)
	}
	got, err := readStateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pins) != 2 || !got.Pins[0].Durable || got.Pins[0].Key != "ws1:dev" ||
		got.Pins[1].Durable || !got.Pins[1].Until.Equal(at.Add(time.Hour)) {
		t.Fatalf("pins round-tripped as %+v", got.Pins)
	}
}
