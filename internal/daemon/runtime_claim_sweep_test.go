package daemon

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var sweepT0 = time.Unix(1_800_000_000, 0)

func sweepAlive(string, int, string) bool { return true }

func newSweep(t *testing.T) (*runtimeClaimSweep, *store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	return &runtimeClaimSweep{dir: dir, add: st.AddJournal, logf: t.Logf}, st, dir
}

func sweepRows(t *testing.T, st *store.Store) []pigtail.JournalEntry {
	t.Helper()
	rows, err := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "runtime-claim-expired"})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRuntimeClaimSweepLiftsAndJournalsOnce(t *testing.T) {
	w, st, dir := newSweep(t)
	def := string(scope.Default())
	held, err := runtimeclaim.Claim(dir, runtimeclaim.State{Reason: "probe-reason", Session: "sess-AAAA-BBBB", Label: "probe-label",
		LabelScope: def, PID: 1, ConfigDir: "/cfg"}, false, sweepAlive, sweepT0)
	if err != nil {
		t.Fatal(err)
	}
	w.once(sweepT0.Add(runtimeclaim.TTL - time.Second))
	if _, ok, _ := runtimeclaim.Load(dir); !ok || len(sweepRows(t, st)) != 0 {
		t.Fatal("lifted or journalled before the deadline")
	}
	w.once(sweepT0.Add(runtimeclaim.TTL))
	if _, ok, _ := runtimeclaim.Load(dir); ok {
		t.Fatal("lease still there at the deadline")
	}
	rows := sweepRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.ID != "runtime-claim-expired:"+held.ID || r.Source != "pigtail:runtime/claim#"+held.ID || r.Scope != def {
		t.Fatalf("row = %+v", r)
	}
	if !strings.Contains(r.Summary, held.ID) || strings.Contains(r.Summary, "probe-label") || strings.Contains(r.Summary, "probe-reason") {
		t.Fatalf("summary = %q", r.Summary)
	}
	w.once(sweepT0.Add(runtimeclaim.TTL + time.Minute))
	if n := len(sweepRows(t, st)); n != 1 {
		t.Fatalf("rows after a third once = %d", n)
	}
}

func TestRuntimeClaimSweepJournalsCLILift(t *testing.T) {
	w, st, dir := newSweep(t)
	if _, err := runtimeclaim.Claim(dir, runtimeclaim.State{Reason: "r", Session: "sess-A"}, false, sweepAlive, sweepT0); err != nil {
		t.Fatal(err)
	}
	at := sweepT0.Add(runtimeclaim.TTL + 7*time.Minute)
	if _, ok, err := runtimeclaim.Expire(dir, at); !ok || err != nil {
		t.Fatalf("Expire = %v %v", ok, err)
	}
	w.once(at.Add(time.Hour))
	rows := sweepRows(t, st)
	if len(rows) != 1 || rows[0].Timestamp.UnixMilli() != at.UnixMilli() {
		t.Fatalf("rows = %+v, want one at %v", rows, at)
	}
}

func TestRuntimeClaimSweepSkipsMarkerOfHeldLease(t *testing.T) {
	w, st, dir := newSweep(t)
	held, err := runtimeclaim.Claim(dir, runtimeclaim.State{Reason: "r", Session: "sess-A"}, false, sweepAlive, sweepT0)
	if err != nil {
		t.Fatal(err)
	}
	// A marker naming the lease currently held (a restored backup, a hand edit).
	raw, _ := json.Marshal(runtimeclaim.Expiry{Lease: held, At: sweepT0})
	if err := os.WriteFile(filepath.Join(dir, runtimeclaim.ExpiredName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	w.once(sweepT0.Add(time.Minute))
	if n := len(sweepRows(t, st)); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	if _, ok, _ := runtimeclaim.Load(dir); !ok {
		t.Fatal("held lease touched")
	}
}

func TestRuntimeClaimSweepUserMasterScope(t *testing.T) {
	w, st, dir := newSweep(t)
	if _, err := runtimeclaim.Claim(dir, runtimeclaim.State{Reason: "r"}, false, sweepAlive, sweepT0); err != nil {
		t.Fatal(err)
	}
	w.once(sweepT0.Add(runtimeclaim.TTL))
	rows := sweepRows(t, st)
	if len(rows) != 1 || rows[0].Scope != string(scope.Master()) || !strings.Contains(rows[0].Summary, "the user outside Claude Code") {
		t.Fatalf("rows = %+v", rows)
	}
}

// The holder of a lease taken outside Claude Code is named after human.name.
func TestRuntimeClaimSweepNamesTheConfiguredUser(t *testing.T) {
	w, st, dir := newSweep(t)
	w.human = "Ada"
	if _, err := runtimeclaim.Claim(dir, runtimeclaim.State{Reason: "r"}, false, sweepAlive, sweepT0); err != nil {
		t.Fatal(err)
	}
	w.once(sweepT0.Add(runtimeclaim.TTL))
	rows := sweepRows(t, st)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "held by Ada outside Claude Code") || strings.Contains(rows[0].Summary, "the user") {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestLogRuntimeClaimNamesTheConfiguredUser(t *testing.T) {
	dir := t.TempDir()
	if _, err := runtimeclaim.Claim(dir, runtimeclaim.State{Reason: "r"}, false, sweepAlive, sweepT0); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	logRuntimeClaim(dir, "Ada", sweepAlive)
	if !strings.Contains(buf.String(), "held by Ada outside Claude Code") {
		t.Fatalf("log = %q", buf.String())
	}
}
