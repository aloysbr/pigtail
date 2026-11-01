package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sideConnWaiting is sideConn that waits for the lock instead of failing at
// once: the test writes while Relay.Run purges every 10 ms on the store's
// connection, which would otherwise answer SQLITE_BUSY (flaky ~6/100).
func sideConnWaiting(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// G3: closed waits are purged after 30 days, at the start of Relay.Run and at
// each tick of its sweep; an open one never.
func TestRelayRunPurgesClosedWaitsAtStartAndOnTick(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	f := newRelayFixtureAt(t, path)
	db := sideConnWaiting(t, path)
	T := f.clock()
	n := 0
	// closedAt seeds a wait of a fresh session, closes it and dates its close.
	closedAt := func(at time.Time) string {
		t.Helper()
		n++
		s := "s" + string(rune('a'+n))
		tr, err := f.st.SetSessionWaitRecord(s, pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: T}, store.WaitMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.ClearSessionWaits(s); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE wait_records SET closed_at = ? WHERE id = ?`, at.UnixMilli(), tr.OpenedID); err != nil {
			t.Fatal(err)
		}
		return tr.OpenedID
	}
	gone := func(id string) bool {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := f.st.GetWaitRecord(id); errors.Is(err, store.ErrNotFound) {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	run := func(interval time.Duration) context.CancelFunc {
		f.rl.cfg.SweepInterval = interval
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { f.rl.Run(ctx); close(done) }()
		return func() { cancel(); <-done }
	}
	old := closedAt(T.Add(-31 * 24 * time.Hour))
	young := closedAt(T.Add(-29 * 24 * time.Hour))
	tr, err := f.st.SetSessionWaitRecord("s-open", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: T.Add(-40 * 24 * time.Hour)}, store.WaitMeta{})
	if err != nil {
		t.Fatal(err)
	}

	// At start: no tick within the hour.
	stop := run(time.Hour)
	if !gone(old) {
		stop()
		t.Fatal("a wait closed 31 days ago outlived Run's start")
	}
	stop()

	// On a tick: the start's purge is past once start is gone.
	stop = run(10 * time.Millisecond)
	defer stop()
	start := closedAt(T.Add(-31 * 24 * time.Hour))
	if !gone(start) {
		t.Fatal("start purge of the second run did not happen")
	}
	tick := closedAt(T.Add(-31 * 24 * time.Hour))
	if !gone(tick) {
		t.Fatal("a wait closed 31 days ago outlived the sweep's tick")
	}
	for _, id := range []string{young, tr.OpenedID} {
		if _, err := f.st.GetWaitRecord(id); err != nil {
			t.Fatalf("%s purged: %v", id, err)
		}
	}
}

// C8: a kept answer purged before any session of its subject ran is logged.
func TestPurgeLogsNeverDeliveredAnswer(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	var lines []string
	f.rl.svc.Logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	old := f.clock().Add(-31 * 24 * time.Hour)
	tr, err := f.st.SetSessionWaitRecord("s-a", pigtail.SessionWait{Type: "arbitrage", Source: pigtail.WaitSourceDeclared, Since: old},
		store.WaitMeta{Scope: "perso", SubjectID: "sub_s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AnswerWaitWithMessage(tr.OpenedID, []byte(`{"choice":"A"}`), old, nil); err != nil {
		t.Fatal(err)
	}
	f.rl.purgeWaits()
	for _, l := range lines {
		if strings.Contains(l, tr.OpenedID) && strings.Contains(l, "never delivered") && strings.Contains(l, "sub_s") {
			return
		}
	}
	t.Fatalf("no log line names the kept answer %s: %q", tr.OpenedID, lines)
}
