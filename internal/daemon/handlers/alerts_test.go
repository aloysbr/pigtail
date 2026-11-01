package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedRespawnLine writes the line NotifyRespawnEnd writes (Task 4), straight
// into the store: GET /alerts must read the journal and nothing else.
func seedRespawnLine(t *testing.T, st *store.Store, sc, state, run, subject, detail string, at time.Time, gone bool) {
	t.Helper()
	sum := "respawn " + state + " " + run + ": " + detail
	if gone {
		sum += respawnNoOne
	}
	e := &pigtail.JournalEntry{ID: "respawn:" + run + ":" + state, Timestamp: at, Kind: respawnJournalKindPrefix + state,
		Scope: sc, SubjectID: subject, Summary: sum, Source: "pigtail:respawn/" + run}
	if _, err := st.AddJournal(e); err != nil {
		t.Fatal(err)
	}
}

func alertsRouter(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	r, st, svc := scopedFixtureSvc(t)
	RegisterAlertRoutes(r, svc, testResponder{})
	return r, st
}

func getAlerts(t *testing.T, r http.Handler, c caller) []pigtail.AlertView {
	t.Helper()
	rec := doAs(t, r, c, "GET", "/alerts", nil)
	var got pigtail.AlertsResponse // testResponder writes the bare object (subjects_test.go:21-25), no {data:…}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Alerts == nil {
		t.Fatalf("GET /alerts = %d %s (alerts must be an array, never null)", rec.Code, rec.Body)
	}
	return got.Alerts
}

func TestListAlertsShowsOpenRespawnAlerts(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	t0 := time.Now().Add(-20 * time.Minute).UTC()
	seedRespawnLine(t, st, "perso", "failed", "r-old", "sub_p", "no fresh Claude in surface:7 after 30s", t0, false)
	seedRespawnLine(t, st, "perso", "abandoned", "r-new", "sub_p", "gave up after 10m0s: background shell: `npm run dev` (14m)", t0.Add(10*time.Minute), false)
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "gate-fired:g1", Timestamp: t0, Kind: "gate-fired", Scope: "perso", Summary: "g1", Source: "file:x#g1"}); err != nil {
		t.Fatal(err)
	}
	got := getAlerts(t, r, perso)
	if len(got) != 2 || got[0].ID != "r-old" || got[1].ID != "r-new" {
		t.Fatalf("alerts = %+v, want r-old then r-new (oldest first), no gate line", got)
	}
	a := got[1]
	if a.Kind != pigtail.AlertKindRespawn || a.State != "abandoned" || a.Scope != "perso" || a.SubjectID != "sub_p" ||
		a.SubjectTitle != pigtail.ShortTitle(pigtail.Subject{Title: "perso subject"}) ||
		a.Detail != "gave up after 10m0s: background shell: `npm run dev` (14m)" || a.AgeSeconds < 590 || a.AgeSeconds > 700 {
		t.Fatalf("alert = %+v", a)
	}
	// A resolution line closes its run, and only its run.
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "alert-resolved:r-old", Timestamp: time.Now().UTC(), Kind: "alert-resolved",
		Scope: "perso", Summary: "respawn alert r-old resolved: acked by hand", Source: "pigtail:alerts/r-old"}); err != nil {
		t.Fatal(err)
	}
	if got := getAlerts(t, r, perso); len(got) != 1 || got[0].ID != "r-new" {
		t.Fatalf("after the resolution of r-old: %+v", got)
	}
}

// Review Focus 16: a resolution never falls out of a read window — 1 001
// newer resolutions of other runs, and r-old stays closed.
func TestListAlertsResolutionNeverFallsOut(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	t0 := time.Now().Add(-time.Hour).UTC()
	seedRespawnLine(t, st, "perso", "abandoned", "r-old", "sub_p", "d", t0, false)
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "alert-resolved:r-old", Timestamp: t0.Add(time.Second), Kind: "alert-resolved",
		Scope: "perso", Summary: "respawn alert r-old resolved: acked by hand", Source: "pigtail:alerts/r-old"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= pigtail.JournalListMax; i++ {
		run := fmt.Sprintf("r-%04d", i)
		if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "alert-resolved:" + run, Timestamp: t0.Add(time.Minute + time.Duration(i)*time.Millisecond),
			Kind: "alert-resolved", Scope: "perso", Summary: "respawn alert " + run + " resolved: acked by hand", Source: "pigtail:alerts/" + run}); err != nil {
			t.Fatal(err)
		}
	}
	if got := getAlerts(t, r, perso); len(got) != 0 {
		t.Fatalf("a resolved alert came back: %+v", got)
	}
}

// Review Focus 20: an open alert never falls out of a read window either —
// 1 001 newer alert lines of its kind, all resolved, and r-open is still listed.
// Red under one window of the latest JournalListMax alert lines (review round 4).
func TestListAlertsOpenAlertNeverFallsOut(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	t0 := time.Now().Add(-time.Hour).UTC()
	seedRespawnLine(t, st, "perso", "abandoned", "r-open", "sub_p", "d", t0, false)
	for i := 0; i <= pigtail.JournalListMax; i++ {
		run := fmt.Sprintf("r-%04d", i)
		at := t0.Add(time.Minute + time.Duration(i)*time.Millisecond)
		seedRespawnLine(t, st, "perso", "abandoned", run, "sub_p", "d", at, false)
		if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "alert-resolved:" + run, Timestamp: at.Add(time.Second), Kind: "alert-resolved",
			Scope: "perso", Summary: "respawn alert " + run + " resolved: acked by hand", Source: "pigtail:alerts/" + run}); err != nil {
			t.Fatal(err)
		}
	}
	if got := getAlerts(t, r, perso); len(got) != 1 || got[0].ID != "r-open" {
		t.Fatalf("got %d alerts, want r-open alone: %+v", len(got), got)
	}
}

// D4: a failed run whose session is gone still shows; the tail is not the reason.
func TestListAlertsShowsGoneSessionAlert(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	seedRespawnLine(t, st, "perso", "failed", "r1", "sub_p", "the session ended on its own before the respawn — nothing relaunched", time.Now().UTC(), true)
	got := getAlerts(t, r, perso)
	if len(got) != 1 || got[0].Detail != "the session ended on its own before the respawn — nothing relaunched" {
		t.Fatalf("alerts = %+v", got)
	}
}

// Scope filter, and D23: the title only for a caller that reads the subject's scope.
func TestListAlertsTitleFollowsScope(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	seedRespawnLine(t, st, "perso", "abandoned", "r-p", "sub_p", "d", now, false)
	seedRespawnLine(t, st, "globex", "abandoned", "r-t", "sub_t", "d", now, false)
	seedRespawnLine(t, st, "globex", "failed", "r-x", "sub_p", "d", now, false) // a work line on a perso subject
	if got := getAlerts(t, r, perso); len(got) != 3 {
		t.Fatalf("master sees %d alerts, want 3: %+v", len(got), got)
	}
	got := getAlerts(t, r, work)
	if len(got) != 2 {
		t.Fatalf("work sees %+v, want r-t and r-x only", got)
	}
	for _, a := range got {
		switch a.ID {
		case "r-t":
			if a.SubjectTitle == "" {
				t.Errorf("r-t: work reads its own subject, title missing")
			}
		case "r-x":
			if a.SubjectTitle != "" || a.SubjectID != "sub_p" {
				t.Errorf("r-x: a perso title leaked to work: %+v", a)
			}
		default:
			t.Errorf("work sees %s", a.ID)
		}
	}
}

func ackAlert(t *testing.T, r http.Handler, c caller, run string, body pigtail.AlertAckRequest) (int, pigtail.AlertResolution, string) {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/alerts/"+run+"/ack", body)
	var got pigtail.AlertResolution // testResponder writes the bare object, no {data:…}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got, rec.Body.String()
}

// seedPeer registers a session whose subject (as GetPeer reads it) is subject.
func seedPeer(t *testing.T, st *store.Store, id, sc, subject string) {
	t.Helper()
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: id, Scope: sc, CellID: "c", SubjectID: subject, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func TestAckAlertResolvesOnce(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	seedRespawnLine(t, st, "perso", "abandoned", "r1", "sub_p", "d", time.Now().UTC(), false)
	seedPeer(t, st, "0f3c9a1e-1111-4222-8333-444455556666", "perso", "sub_p") // a session of the alert's subject
	code, res, body := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{SessionID: "0f3c9a1e-1111-4222-8333-444455556666"})
	if code != 200 || !res.Resolved || res.Summary != "respawn alert r1 resolved: acked by session 0f3c9a1e" {
		t.Fatalf("ack = %d %s", code, body)
	}
	if got := getAlerts(t, r, perso); len(got) != 0 {
		t.Fatalf("still open: %+v", got)
	}
	rows, err := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"})
	if err != nil || len(rows) != 1 || rows[0].ID != "alert-resolved:r1" || rows[0].Scope != "perso" ||
		rows[0].SubjectID != "sub_p" || rows[0].Source != "pigtail:alerts/r1" {
		t.Fatalf("resolution line = %+v, %v", rows, err)
	}
	// Again, by hand: 200, nothing written, the first resolution said back.
	code, res, _ = ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{})
	if code != 200 || res.Resolved || res.Summary != "respawn alert r1 resolved: acked by session 0f3c9a1e" {
		t.Fatalf("second ack = %d %+v", code, res)
	}
	seedRespawnLine(t, st, "perso", "failed", "r2", "", "d", time.Now().UTC(), true)
	if _, res, _ := ackAlert(t, r, perso, "r2", pigtail.AlertAckRequest{}); res.Summary != "respawn alert r2 resolved: acked by hand" {
		t.Fatalf("by hand = %+v", res)
	}
	if code, _, _ := ackAlert(t, r, perso, "r-none", pigtail.AlertAckRequest{}); code != 404 {
		t.Fatalf("unknown run = %d, want 404", code)
	}
	if code, _, _ := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{SessionID: "../x"}); code != 400 {
		t.Fatalf("bad session id = %d, want 400", code)
	}
}

// D5 « une session du sujet »: acked by session is true or refused.
func TestAckAlertSessionMustBeOnTheSubject(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	seedRespawnLine(t, st, "perso", "abandoned", "r1", "sub_p", "d", time.Now().UTC(), false)
	seedPeer(t, st, "s-on-q", "perso", "sub_q")
	seedPeer(t, st, "s-work-on-p", "gx", "sub_p")
	if code, _, body := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{SessionID: "s-on-q"}); code != 409 || !strings.Contains(body, "was not told") {
		t.Fatalf("session of another subject = %d %s, want 409", code, body)
	}
	// A session of another scope answers like a session nobody registered (D19).
	code, _, other := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{SessionID: "s-work-on-p"})
	_, _, unknown := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{SessionID: "s-nobody"})
	if code != 404 || other != unknown {
		t.Fatalf("other-scope session = %d %s, unknown = %s", code, other, unknown)
	}
	if rows, _ := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"}); len(rows) != 0 {
		t.Fatalf("a refused ack wrote %+v", rows)
	}
	// By hand stays open.
	if code, res, _ := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{}); code != 200 || res.Summary != "respawn alert r1 resolved: acked by hand" {
		t.Fatalf("by hand = %d %+v", code, res)
	}
}

// Review Focus 15: the session the notice woke acks its alert even with no
// subject in the registry (a respawn accepted in an unbound workspace); a third
// session with no subject is refused, and so is it for another run.
func TestAckAlertFromToldSessionWithoutSubject(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	for _, l := range []struct{ run, subject string }{{"r1", "sub_p"}, {"r2", ""}, {"r3", "sub_p"}} {
		told := "s-told"
		if l.run == "r3" {
			told = "s-other" // r3 told another session
		}
		if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:" + l.run + ":abandoned", Timestamp: now, Kind: "respawn-abandoned",
			Scope: "perso", SubjectID: l.subject, Summary: "respawn abandoned " + l.run + ": d", Source: respawnSource(l.run, told)}); err != nil {
			t.Fatal(err)
		}
	}
	seedPeer(t, st, "s-told", "perso", "")  // no subject: unbound workspace, no worktree
	seedPeer(t, st, "s-third", "perso", "") // no subject, never told
	if code, _, body := ackAlert(t, r, perso, "r1", pigtail.AlertAckRequest{SessionID: "s-third"}); code != 409 || !strings.Contains(body, "was not told") {
		t.Fatalf("third session = %d %s, want 409", code, body)
	}
	if code, _, body := ackAlert(t, r, perso, "r3", pigtail.AlertAckRequest{SessionID: "s-told"}); code != 409 {
		t.Fatalf("told of r1, not of r3 = %d %s, want 409", code, body)
	}
	if rows, _ := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"}); len(rows) != 0 {
		t.Fatalf("a refused ack wrote %+v", rows)
	}
	for _, run := range []string{"r1", "r2"} { // r2: an alert with no subject, its told session acks it
		code, res, body := ackAlert(t, r, perso, run, pigtail.AlertAckRequest{SessionID: "s-told"})
		if code != 200 || !res.Resolved || res.Summary != "respawn alert "+run+" resolved: acked by session s-told" {
			t.Fatalf("told session on %s = %d %s", run, code, body)
		}
	}
}

// Review Focus 11: an alert of a --cross-scope respawn sits in the subject's
// scope; the subject's own session acks it with --cross-scope (as the notice
// says), not without.
func TestAckAlertCrossScopeFromSubjectSession(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := standardPeers(f, t) // a work subject
	RegisterAlertRoutes(f.r, f.rl.svc, testResponder{})
	f.peers(t, pigtail.PeerSession{SessionID: "s-master-on-work", Cwd: "/wt/feat-session-relay", Scope: "perso", CellID: "mp"})
	req := respawnReq(pigtail.CmuxRespawnAbandoned, "d")
	req.SubjectID, req.SessionID = sub.ID, "s-master-on-work"
	if got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), true, cmuxWS, req); got.Journal != "ok" {
		t.Fatalf("notify = %+v", got)
	}
	ack := pigtail.AlertAckRequest{SessionID: "s-master-on-work"}
	if code, _, body := ackAlert(t, f.r, perso, req.RunID, ack); code != 403 {
		t.Fatalf("without --cross-scope = %d %s, want 403", code, body)
	}
	code, res, body := ackAlert(t, f.r, persoCross, req.RunID, ack)
	if code != 200 || !res.Resolved || res.Summary != "respawn alert "+req.RunID+" resolved: acked by session s-master" {
		t.Fatalf("with --cross-scope = %d %s", code, body)
	}
	subScope, _ := f.st.SubjectScope(sub.ID)
	rows, _ := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"})
	if len(rows) != 1 || rows[0].Scope != string(subScope) {
		t.Fatalf("resolution = %+v, want one line in %s", rows, subScope)
	}
}

// Another scope's alert answers like an unknown run (D19), and stays open.
func TestAckAlertOtherScopeIsUnknown(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	seedRespawnLine(t, st, "perso", "abandoned", "r1", "sub_p", "d", time.Now().UTC(), false)
	_, _, unknown := ackAlert(t, r, work, "r-none", pigtail.AlertAckRequest{})
	code, _, body := ackAlert(t, r, work, "r1", pigtail.AlertAckRequest{})
	if code != 404 || strings.ReplaceAll(body, "r1", "r-none") != unknown {
		t.Fatalf("work acks a perso alert: %d %s (unknown: %s)", code, body, unknown)
	}
	if got := getAlerts(t, r, perso); len(got) != 1 {
		t.Fatalf("perso alert closed by work: %+v", got)
	}
}

func TestResolveSubjectAlertsStaysInScope(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	seedRespawnLine(t, st, "perso", "abandoned", "r1", "sub_p", "d", now, false)
	seedRespawnLine(t, st, "perso", "failed", "r2", "sub_p", "d", now, false)
	seedRespawnLine(t, st, "perso", "abandoned", "r3", "sub_q", "d", now, false)
	seedRespawnLine(t, st, "globex", "abandoned", "r4", "sub_p", "d", now, false)
	resolve := func(c caller, req pigtail.AlertResolveRequest) (int, pigtail.AlertsResolved) {
		rec := doAs(t, r, c, "POST", "/alerts/resolve", req)
		var got pigtail.AlertsResolved // bare object (testResponder)
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return rec.Code, got
	}
	code, got := resolve(perso, pigtail.AlertResolveRequest{SubjectID: "sub_p", Cause: "respawn r9"})
	if code != 200 || strings.Join(got.IDs, ",") != "r1,r2" {
		t.Fatalf("resolve = %d %+v, want r1,r2 (r3: other subject; r4: work, not writable without --cross-scope)", code, got)
	}
	open := getAlerts(t, r, perso)
	if len(open) != 2 || open[0].ID == "r1" || open[1].ID == "r1" {
		t.Fatalf("open after resolve: %+v", open)
	}
	rows, _ := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"})
	for _, row := range rows {
		if !strings.HasSuffix(row.Summary, "resolved: subject relaunched (respawn r9)") {
			t.Errorf("summary = %q", row.Summary)
		}
	}
	if code, got := resolve(perso, pigtail.AlertResolveRequest{SubjectID: "sub_p", Cause: "respawn r9"}); code != 200 || len(got.IDs) != 0 || got.IDs == nil {
		t.Fatalf("second resolve = %d %+v, want 200 and [] (never null)", code, got)
	}
	for name, req := range map[string]pigtail.AlertResolveRequest{
		"no subject": {Cause: "x"}, "no cause": {SubjectID: "sub_p"}, "cause on two lines": {SubjectID: "sub_p", Cause: "a\nb"},
	} {
		if code, _ := resolve(perso, req); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
}

// A subject the store cannot read (not "not found") keeps the id fallback and
// says why in the daemon log; a plain not-found stays silent.
func TestListAlertsLogsUnreadableSubject(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "p.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var logged []string
	svc := &Services{Store: st, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	mux := chi.NewRouter()
	RegisterAlertRoutes(mux, svc, testResponder{})
	now := time.Now().UTC()
	seedRespawnLine(t, st, "perso", "abandoned", "r-gone", "sub_nowhere", "d", now, false) // never created: not-found
	if got := getAlerts(t, mux, perso); len(got) != 1 || got[0].SubjectID != "sub_nowhere" || len(logged) != 0 {
		t.Fatalf("not-found: alerts %+v, log %q (want the id kept and no line)", got, logged)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE themes RENAME TO themes_gone`); err != nil {
		t.Fatal(err)
	}
	got := getAlerts(t, mux, perso)
	if len(got) != 1 || got[0].SubjectID != "sub_nowhere" || got[0].SubjectTitle != "" {
		t.Fatalf("unreadable store: alerts %+v, want the id fallback", got)
	}
	if len(logged) != 1 || !strings.HasPrefix(logged[0], "alerts: subject sub_nowhere unreadable: ") {
		t.Fatalf("log = %q, want one 'alerts: subject sub_nowhere unreadable: …' line", logged)
	}
}
