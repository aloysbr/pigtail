package dbtool

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

func TestResolveDBPathPrefersTheOverride(t *testing.T) {
	t.Parallel()
	want := filepath.Join(t.TempDir(), "custom.db")
	if got := ResolveDBPath(want); got != want {
		t.Fatalf("ResolveDBPath(%q) = %q, want the override", want, got)
	}
}

func TestResolveDBPathDefaultsBesideTheSocket(t *testing.T) {
	t.Parallel()
	got := ResolveDBPath("")
	if filepath.Base(got) != "pigtail.db" {
		t.Fatalf("ResolveDBPath(\"\") = %q, want a path ending in pigtail.db", got)
	}
}

func TestFormatGenerationsMarksTheLiveRowAndReportsCorruption(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	rows := []store.GenerationInfo{
		{
			Generation: store.LiveGeneration, Exists: true, IntegrityOK: true, HasMigrations: true,
			ModTime: now, SizeBytes: 1 << 20, Workspaces: 12, Projects: 2, Subjects: 31,
		},
		{
			Generation: 0, Exists: true, IntegrityOK: true, HasMigrations: true,
			ModTime: now.Add(-40 * time.Minute), SizeBytes: 1 << 20, Workspaces: 12, Projects: 2, Subjects: 31,
		},
		{
			Generation: 1, Exists: true, IntegrityOK: false, IntegrityErr: "malformed",
			ModTime: now.Add(-100 * time.Minute), SizeBytes: 900, Workspaces: -1, Projects: -1, Subjects: -1,
		},
	}

	out := formatGenerations(rows, now, "perso")

	if !strings.Contains(out, "live") {
		t.Fatalf("the live row is not labelled:\n%s", out)
	}
	if !strings.Contains(out, ".0") || !strings.Contains(out, ".1") {
		t.Fatalf("generation labels missing:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Fatalf("a corrupt generation must be visibly FAILED:\n%s", out)
	}
	// Unknown counts print as a dash, never as 0 — "0 workspaces" and "we
	// could not read it" must not look the same.
	if strings.Contains(out, "malformed") && !strings.Contains(out, "—") {
		t.Fatalf("unknown counts must render as a dash:\n%s", out)
	}
}

func dirNames(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// A globex cell is shown the globex subjects only: neither the total across scopes
// nor the perso count may reach it. A perso cell sees the total and the split.
func TestFormatGenerationsCountsSubjectsWithinTheCallersScope(t *testing.T) {
	scopetest.Three(t)
	now := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	rows := []store.GenerationInfo{{
		Generation: store.LiveGeneration, Exists: true, IntegrityOK: true, HasMigrations: true,
		ModTime: now, SizeBytes: 1 << 20, Workspaces: 12, Projects: 2, Subjects: 491,
		SubjectsByScope: map[string]int{"globex": 123, "perso": 348, "acme": 17},
	}, {
		Generation: 0, Exists: true, IntegrityOK: true, HasMigrations: true,
		ModTime: now, SizeBytes: 1 << 20, Workspaces: 12, Projects: 2, Subjects: 7,
		SubjectsByScope: map[string]int{"perso": 2, "zeta": 5},
	}}

	persoOut := formatGenerations(rows, now, "perso")
	if !strings.Contains(persoOut, "SUBJECTS  ACME  GLOBEX  PERSO  ZETA") {
		t.Fatalf("master header:\n%s", persoOut)
	}
	for _, want := range []string{"488", "123", "348", "17"} {
		if !strings.Contains(persoOut, want) {
			t.Fatalf("master view misses %q:\n%s", want, persoOut)
		}
	}

	tsOut := formatGenerations(rows, now, "globex")
	if !strings.Contains(tsOut, "123") || strings.Contains(tsOut, "GLOBEX") {
		t.Fatalf("globex view lost its count or shows columns:\n%s", tsOut)
	}
	for _, leak := range []string{"488", "348", "17", "PERSO", "ACME", "ZETA"} {
		if strings.Contains(tsOut, leak) {
			t.Fatalf("globex view shows %q:\n%s", leak, tsOut)
		}
	}
	acmeOut := formatGenerations(rows, now, "acme")
	if !strings.Contains(acmeOut, "17") || strings.Contains(acmeOut, "123") {
		t.Fatalf("acme view:\n%s", acmeOut)
	}
}

func testIO(stdin string, tty bool) (IO, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return IO{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut,
		IsTTY: func() bool { return tty },
		Now:   func() time.Time { return time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC) },
	}, &out, &errOut
}

func seededDB(t *testing.T) (dir, db string) {
	t.Helper()
	dir = t.TempDir()
	db = filepath.Join(dir, "pigtail.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BackupRotated(store.BackupGenerations); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, db
}

func TestRunListPrintsTheGenerations(t *testing.T) {
	_, db := seededDB(t)
	// Seed the offsite state so the test proves runList wires the offsite line.
	if err := store.WriteOffsiteState(db, store.OffsiteState{DestDir: "/mnt/offsite"}); err != nil {
		t.Fatal(err)
	}
	o, out, errOut := testIO("", false)
	if code := Run([]string{"list", "-view", string(scope.Master()), "-db", db}, o); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"GENERATION", "live", ".0", "offsite:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
}

// An empty view never falls back to a default, let alone to the master: the
// CLI always passes the caller's scope, and a call without one is a bug.
func TestRunRequiresAView(t *testing.T) {
	_, db := seededDB(t)
	for _, args := range [][]string{{"list", "-db", db}, {"restore", "-db", db, "-dry-run"}} {
		o, out, errOut := testIO("", false)
		if code := Run(args, o); code != ExitUsage {
			t.Fatalf("%v: code %d, want %d", args, code, ExitUsage)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "-view") {
			t.Fatalf("%v: stdout %q stderr %q", args, out, errOut)
		}
	}
}

// Exit code 2 is reserved: the CLI reads it as "pigtaild predates -db-tool".
func TestRunNeverExitsTwo(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"list", "-nope"}, {"list", "extra"}, {"restore", "-generation", "x"}} {
		o, _, _ := testIO("", false)
		if code := Run(args, o); code == 2 || code == ExitOK {
			t.Fatalf("%v: code %d", args, code)
		}
	}
}

func TestRunRestoreDryRunChangesNothing(t *testing.T) {
	dir, db := seededDB(t)
	before := dirNames(t, dir)
	o, out, errOut := testIO("", false)
	if code := Run([]string{"restore", "-view", string(scope.Master()), "-db", db, "-generation", "0", "-dry-run", "-yes"}, o); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out.String(), "(dry run — no changes made)") {
		t.Fatalf("stdout:\n%s", out)
	}
	if after := dirNames(t, dir); after != before {
		t.Fatalf("--dry-run modified the directory:\nbefore %s\nafter  %s", before, after)
	}
}

// Without a terminal there is nobody to ask, so --yes becomes mandatory rather
// than the prompt being silently skipped. Agent sessions run without a TTY.
func TestRunRestoreWithoutATTYRequiresYes(t *testing.T) {
	dir, db := seededDB(t)
	before := dirNames(t, dir)
	o, _, errOut := testIO("", false)
	if code := Run([]string{"restore", "-view", string(scope.Master()), "-db", db, "-generation", "0"}, o); code != ExitError {
		t.Fatalf("code %d, want %d", code, ExitError)
	}
	if got := errOut.String(); !strings.HasPrefix(got, "Error: ") || !strings.Contains(got, "--yes") || strings.Count(got, "\n") != 1 {
		t.Fatalf("want one `Error: … --yes` line, got %q", got)
	}
	if after := dirNames(t, dir); after != before {
		t.Fatal("a refused restore modified the directory")
	}
}

func TestRunRestoreAnsweredNoIsAborted(t *testing.T) {
	dir, db := seededDB(t)
	before := dirNames(t, dir)
	o, out, errOut := testIO("\n", true) // bare Enter = default no
	if code := Run([]string{"restore", "-view", string(scope.Master()), "-db", db, "-generation", "0"}, o); code != ExitError {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "[y/N]") || errOut.String() != "Error: aborted\n" {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
	if after := dirNames(t, dir); after != before {
		t.Fatal("an aborted restore modified the directory")
	}
}

func TestRunRestoreReplacesAndKeepsThePrevious(t *testing.T) {
	_, db := seededDB(t)
	o, out, errOut := testIO("y\n", true)
	if code := Run([]string{"restore", "-view", string(scope.Master()), "-db", db, "-generation", "0"}, o); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out.String(), "✓ restored") || !strings.Contains(out.String(), "launchctl bootstrap") {
		t.Fatalf("stdout:\n%s", out)
	}
	if m, _ := filepath.Glob(db + ".pre-restore-*"); len(m) == 0 {
		t.Fatal("the previous database was not kept aside")
	}
}
