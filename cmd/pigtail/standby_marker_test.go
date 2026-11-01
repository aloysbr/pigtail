package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// The marker predicate (§ Prédicat Standby): present → in Standby; absent →
// not; any other stat error → in Standby, the error handed back; an invalid
// key is never stat'ed.
func TestMarkerInStandby(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if in, err := markerInStandby(dir, "s1"); !in || err != nil {
		t.Fatalf("present = %v, %v", in, err)
	}
	if in, err := markerInStandby(dir, "s2"); in || err != nil {
		t.Fatalf("absent = %v, %v", in, err)
	}
	file := filepath.Join(dir, "s1") // a regular file as the directory: ENOTDIR
	if in, err := markerInStandby(file, "s3"); !in || err == nil {
		t.Fatalf("unreadable = %v, %v", in, err)
	}
	if in, err := markerInStandby(file, "../x"); in || err != nil {
		t.Fatalf("invalid key = %v, %v", in, err)
	}
	env := envOf(map[string]string{"PIGTAIL_STANDBY_DIR": dir})
	if standbyMarkerDir(env) != dir || !inStandby(env, "s1") || inStandby(env, "s2") {
		t.Fatalf("inStandby via env: dir=%q", standbyMarkerDir(env))
	}
	if !inStandby(envOf(map[string]string{"PIGTAIL_STANDBY_DIR": file}), "s3") {
		t.Fatal("unreadable marker dir read as not in Standby")
	}
}

func TestStandbyEnds(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Turnless: []*regexp.Regexp{regexp.MustCompile(`(?i)^\s*/(?:show|clip|rename|pin)(?:\s|$)`)}})
	for p, want := range map[string]bool{
		"reprends":           true,
		"/standby":           true,  // a new /standby: cleared, then set again by the skill
		"/compact":           false, // typed by pigtail (Review Focus 1)
		"  /compact garde X": false,
		"/COMPACT":           false,
		"/compacter":         true,
		"/exit":              false, // typed by cmux check --fix / session move / cmux-unfreeze before a relaunch; the Standby outlives the process (D24, Tâches 1, 12)
		"/exit now":          true,
		"/show 2":            false, // turnless
		"/rename":            false, // turnless
		"/pin":               false, // turnless: /pin ends Standby through its route, not the prompt
		"/pin off":           false, // turnless
	} {
		if got := standbyEnds(p); got != want {
			t.Errorf("standbyEnds(%q) = %v, want %v", p, got, want)
		}
	}
}
