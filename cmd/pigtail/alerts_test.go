package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runAlerts runs `pigtail alerts <args>` against s, returning stdout
// (runJournal, cmd/pigtail/journal_test.go:15, without stdin).
func runAlerts(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var out string
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		rd, wr, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stdout = wr
		cmd := alertsCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
		_ = wr.Close()
		os.Stdout = stdout
		b, _ := io.ReadAll(rd)
		out = string(b)
	})
	return out, err
}

func TestAlertsCLIListAndAck(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterAlertRoutes(r, &handlers.Services{Store: st}, envelopeResponder{}) // the daemon's {data:…} envelope, as realSubjectDaemonSvc registers its routes
	})
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:r1:abandoned", Timestamp: time.Now().Add(-12 * time.Minute).UTC(),
		Kind: "respawn-abandoned", Scope: "perso", SubjectID: "sub_p", Summary: "respawn abandoned r1: gave up after 10m0s: background shell: `npm run dev` (14m)",
		Source: "pigtail:respawn/r1"}); err != nil {
		t.Fatal(err)
	}
	out, err := runAlerts(t, s, "list")
	if err != nil || !strings.Contains(out, "r1") || !strings.Contains(out, "abandoned") || !strings.Contains(out, "12m") ||
		!strings.Contains(out, "gave up after 10m0s") || strings.Contains(out, "respawn abandoned r1:") {
		t.Fatalf("list: %q %v", out, err)
	}
	// A session of the alert's subject, registered in the scope sessionScope
	// resolves for this cell (withCells sets the config dir: perso here).
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "0f3c9a1e-1111-4222-8333-444455556666", Scope: "perso", CellID: "mp",
		SubjectID: "sub_p", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0f3c9a1e-1111-4222-8333-444455556666")
	if out, err := runAlerts(t, s, "ack", "r1"); err != nil || !strings.Contains(out, "✓ alert r1 resolved: acked by session 0f3c9a1e") {
		t.Fatalf("ack: %q %v", out, err)
	}
	if out, err := runAlerts(t, s, "ack", "r1"); err != nil || !strings.Contains(out, "already resolved") {
		t.Fatalf("second ack: %q %v", out, err)
	}
	if _, err := runAlerts(t, s, "ack", "r-none"); err == nil || !strings.Contains(err.Error(), "r-none") {
		t.Fatalf("unknown run: %v", err)
	}
	// --by-hand, from inside a session: the session is not sent.
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:r2:failed", Timestamp: time.Now().UTC(), Kind: "respawn-failed",
		Scope: "perso", SubjectID: "sub_p", Summary: "respawn failed r2: d", Source: "pigtail:respawn/r2"}); err != nil {
		t.Fatal(err)
	}
	if out, err := runAlerts(t, s, "ack", "r2", "--by-hand"); err != nil || !strings.Contains(out, "✓ alert r2 resolved: acked by hand") {
		t.Fatalf("--by-hand: %q %v", out, err)
	}
	if out, _ := runAlerts(t, s, "list"); !strings.Contains(out, "no open alert") {
		t.Fatalf("list after ack: %q", out)
	}
}

// --json: the alerts array, as GET /alerts returns it (never null).
func TestAlertsCLIListJSON(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterAlertRoutes(r, &handlers.Services{Store: st}, envelopeResponder{})
	})
	out, err := runAlerts(t, s, "list", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty --json: %q %v — want []", out, err)
	}
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:r1:failed", Timestamp: time.Now().UTC(), Kind: "respawn-failed",
		Scope: "perso", SubjectID: "sub_p", Summary: "respawn failed r1: d", Source: "pigtail:respawn/r1"}); err != nil {
		t.Fatal(err)
	}
	out, err = runAlerts(t, s, "list", "--json")
	var got []pigtail.AlertView
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got) != 1 || got[0].ID != "r1" ||
		got[0].State != "failed" || got[0].Scope != "perso" || got[0].SubjectID != "sub_p" || got[0].SubjectTitle == "" {
		t.Fatalf("--json: %q %v → %+v", out, err, got)
	}
}

// A 409 (a session neither told nor on the alert's subject): nothing written,
// the error names the daemon's refusal and its second line is the --by-hand hint.
func TestAlertsCLIAckConflictShowsHint(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterAlertRoutes(r, &handlers.Services{Store: st}, envelopeResponder{})
	})
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:r3:abandoned", Timestamp: time.Now().UTC(), Kind: "respawn-abandoned",
		Scope: "perso", SubjectID: "sub_p", Summary: "respawn abandoned r3: d", Source: "pigtail:respawn/r3?session=0f3c9a1e-1111-4222-8333-444455556666"}); err != nil {
		t.Fatal(err)
	}
	stranger := "7a7a7a7a-1111-4222-8333-444455556666" // perso, no subject, not the told session
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: stranger, Scope: "perso", CellID: "mp", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", stranger)
	_, err := runAlerts(t, s, "ack", "r3")
	if err == nil {
		t.Fatal("a stranger session acked the alert")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ack alert r3: ") ||
		lines[1] != "  (from outside the session the notice woke and the alert's subject: pigtail alerts ack r3 --by-hand)" {
		t.Fatalf("409: %q", err)
	}
	if out, _ := runAlerts(t, s, "list"); !strings.Contains(out, "r3") {
		t.Fatalf("the refused ack closed the alert: %q", out)
	}
	// Any other error carries no hint: an unknown run is not a 409.
	if _, err := runAlerts(t, s, "ack", "r-none"); err == nil || strings.Contains(err.Error(), "--by-hand") {
		t.Fatalf("unknown run: %v", err)
	}
}

// Outside any cell (Ada' terminal): no session, the default scope is
// globex, and the alert is perso. The ack by hand must speak for the scope
// --scope / PIGTAIL_SCOPE names, as `alerts list` does — sessionScope ignores
// both and would answer 404 (review round 3). Red on the plan's previous
// sessionClient-only ack, green with newScopedClient on the by-hand path.
func TestAlertsCLIAckByHandOutsideACell(t *testing.T) {
	withCells(t, func(_, _ string) string { return t.TempDir() }) // a config dir no cell owns
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterAlertRoutes(r, &handlers.Services{Store: st}, envelopeResponder{})
	})
	for _, run := range []string{"r4", "r5"} {
		if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:" + run + ":abandoned", Timestamp: time.Now().UTC(), Kind: "respawn-abandoned",
			Scope: "perso", SubjectID: "sub_p", Summary: "respawn abandoned " + run + ": d", Source: "pigtail:respawn/" + run}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(envScope, "perso")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "") // not in a session
	if out, err := runAlerts(t, s, "ack", "r4"); err != nil || !strings.Contains(out, "✓ alert r4 resolved: acked by hand") {
		t.Fatalf("out of a cell, out of a session: %q %v", out, err)
	}
	// --by-hand with a session id in the environment: still the caller's scope.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0f3c9a1e-1111-4222-8333-444455556666")
	if out, err := runAlerts(t, s, "ack", "r5", "--by-hand"); err != nil || !strings.Contains(out, "✓ alert r5 resolved: acked by hand") {
		t.Fatalf("out of a cell, --by-hand: %q %v", out, err)
	}
	// Instrument control: without the override the default scope cannot read the perso line.
	t.Setenv(envScope, "")
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:r6:abandoned", Timestamp: time.Now().UTC(), Kind: "respawn-abandoned",
		Scope: "perso", SubjectID: "sub_p", Summary: "respawn abandoned r6: d", Source: "pigtail:respawn/r6"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runAlerts(t, s, "ack", "r6", "--by-hand"); err == nil || !strings.Contains(err.Error(), "r6") {
		t.Fatalf("default scope acked a perso alert: %v", err)
	}
}

// `pigtail alerts list` shows the kind of each alert and, for the machine-wide
// disk alert, no scope: its column holds a dash, not the master scope's name.
func TestAlertsCLIListShowsTheDiskAlertWithoutScope(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterAlertRoutes(r, &handlers.Services{Store: st}, envelopeResponder{})
	})
	at := time.Now().Add(-5 * time.Minute).UTC()
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "disk-low:1", Timestamp: at, Kind: "disk-low", Scope: string(scope.Master()),
		Summary: "free_bytes=33285996544 threshold_bytes=42949672960 floor_bytes=21474836480", Source: "proc:pigtaild"}); err != nil {
		t.Fatal(err)
	}
	out, err := runAlerts(t, s, "list")
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "disk-low:1") {
			line = l
		}
	}
	f := strings.Fields(line)
	if len(f) < 8 || f[1] != "disk" || f[2] != "-" || f[3] != "low" || !strings.Contains(line, "31 GB free on disk") {
		t.Fatalf("disk alert line = %q (fields %q), want: <id> disk - low - <age> 31 GB free on disk", line, f)
	}
	if strings.Contains(line, string(scope.Master())) {
		t.Fatalf("the line names the master scope: %q", line)
	}
}

// A role alert id carries colons; ack takes it as any run id.
func TestAlertsAckRoleID(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		handlers.RegisterAlertRoutes(r, &handlers.Services{Store: st}, envelopeResponder{})
	})
	id := "role-silent:pilote:1793359800000"
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: id, Timestamp: time.Now().Add(-40 * time.Minute).UTC(), Kind: "role-alert",
		Scope: "perso", SubjectID: "sub_p", Summary: "Role pilote silent: wakeup due 11:30 never fired",
		Source: "pigtail:roles/pilote?state=silent"}); err != nil {
		t.Fatal(err)
	}
	out, err := runAlerts(t, s, "list")
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "wakeup due 11:30 never fired") {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, err := runAlerts(t, s, "ack", id, "--by-hand"); err != nil || !strings.Contains(out, "✓ alert "+id+" resolved: acked by hand") {
		t.Fatalf("ack: %q %v", out, err)
	}
	if out, _ := runAlerts(t, s, "list"); !strings.Contains(out, "no open alert") {
		t.Fatalf("list after ack: %q", out)
	}
}
