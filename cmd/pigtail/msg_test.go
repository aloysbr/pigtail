package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/presence"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestReadSelfEnvAndResolveSelf(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"CLAUDE_CODE_SESSION_ID": "sess-1", "CLAUDE_CONFIG_DIR": "/cfg",
		"CMUX_WORKSPACE_ID": "W", "CMUX_SURFACE_ID": "S", "CLAUDE_PID": "42",
	}
	s := readSelfEnv(func(k string) string { return env[k] })
	if s.SessionID != "sess-1" || s.ConfigDir != "/cfg" || s.WorkspaceID != "W" || s.SurfaceID != "S" || s.PID != 42 {
		t.Fatalf("self = %+v", s)
	}
	if id, err := resolveSelf(s, nil); err != nil || id != "sess-1" {
		t.Fatalf("resolveSelf = %q %v", id, err)
	}
	// Without the env id, the cmux surface decides.
	s.SessionID = ""
	id, err := resolveSelf(s, []pigtail.PeerSession{{SessionID: "other", CmuxSurfaceID: "x"}, {SessionID: "mine", CmuxSurfaceID: "s"}})
	if err != nil || id != "mine" {
		t.Fatalf("by surface = %q %v", id, err)
	}
	s.SurfaceID = ""
	if _, err := resolveSelf(s, nil); err == nil {
		t.Fatal("no identity must fail")
	}
}

func TestSelfPeerCarriesScope(t *testing.T) {
	t.Parallel()
	cells := &claudeprofile.Table{Cells: map[string]claudeprofile.Cell{"mt": {ID: "mt", ConfigDir: "/cfg", Scope: "globex"}}}
	p := selfPeer(selfEnv{SessionID: "s", ConfigDir: "/cfg", WorkspaceID: "W"}, cells)
	if p.CellID != "mt" || p.Scope != "globex" || p.CmuxWorkspaceID != "W" || p.Source != "self" {
		t.Fatalf("self peer = %+v", p)
	}
}

func TestMergePeerKeepsKnownFields(t *testing.T) {
	t.Parallel()
	dst := pigtail.PeerSession{SessionID: "s", Name: "n", PID: 1}
	mergePeer(&dst, pigtail.PeerSession{Name: "other", Scope: "globex", PID: 2, CmuxWorkspaceID: "W"})
	if dst.Name != "n" || dst.PID != 1 || dst.Scope != "globex" || dst.CmuxWorkspaceID != "W" {
		t.Fatalf("merged = %+v", dst)
	}
}

func TestReadBody(t *testing.T) {
	t.Parallel()
	if b, _ := readBody([]string{"hello", "world"}, nil); b != "hello world" {
		t.Fatalf("args body = %q", b)
	}
	if b, _ := readBody([]string{"-"}, strings.NewReader("from stdin\n")); b != "from stdin" {
		t.Fatalf("stdin body = %q", b)
	}
}

func TestPrintSessionsWide(t *testing.T) {
	t.Parallel()
	alive, dead := true, false
	rows := []sessionRow{{SurfaceRef: "surface:1", SessionID: "s1", CellID: "mt", Cwd: "/w"}}
	by := map[string]pigtail.PeerSession{
		"s1": {SessionID: "s1", SubjectSlug: "session-relay", SubjectShortTitle: "Relais de messages", CmuxBound: true, WorkspaceName: "feat-session-relay",
			CmuxTitle: "Relais de messages", Name: "n1", Status: "idle", Alive: &alive, Inbox: 2},
		"s2": {SessionID: "s2", CellID: "it", Name: "outside", Alive: &alive},
		"s3": {SessionID: "s3", Alive: &dead},
		// The daemon's label wins: it carries the live title (a renamed tab
		// shows « fait » until the loop puts the short title back).
		"s4": {SessionID: "s4", CellID: "mt", SubjectSlug: "relance", SubjectShortTitle: "Relance cmux sûre", CmuxBound: true, CmuxTitle: "fait",
			Name: "n4", Label: "Relance cmux sûre · n4 (mt)", Alive: &alive},
	}
	var out bytes.Buffer
	printSessionsWide(&out, rows, by, time.Now(), func(string) bool { return true })
	got := out.String()
	for _, want := range []string{"SUBJECT", "INBOX", "LABEL", "Relais de messages · n1", "feat-session-relay", "idle", "outside (it)", "Relance cmux sûre · n4 (mt)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"s3", "session-relay ·"} {
		if strings.Contains(got, not) {
			t.Errorf("unexpected %q in:\n%s", not, got)
		}
	}
	// SUBJECT is the short title (5th column), "-" without a subject.
	cols := regexp.MustCompile("  +")
	subjects := map[string]string{}
	for _, l := range strings.Split(got, "\n") {
		f := cols.Split(strings.TrimSpace(l), -1)
		if len(f) < 5 {
			continue
		}
		subjects[f[2]] = f[4]
	}
	if subjects["s1"] != "Relais de messages" || subjects["s4"] != "Relance cmux sûre" || subjects["s2"] != "-" {
		t.Errorf("SUBJECT column = %v in:\n%s", subjects, got)
	}
}

func TestPrintSessionsWidePaused(t *testing.T) {
	t.Parallel()
	dead := false
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	by := map[string]pigtail.PeerSession{
		"sp": {SessionID: "sp", CmuxTitle: "lp-1", Alive: &dead, EndedAt: &at, PausedAt: &at},
		"sd": {SessionID: "sd", Alive: &dead, EndedAt: &at},
	}
	var out bytes.Buffer
	printSessionsWide(&out, nil, by, time.Now(), func(string) bool { return true })
	got := out.String()
	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "sp") && strings.Contains(l, "paused") {
			line = l
		}
	}
	if line == "" {
		t.Errorf("no paused line for sp in:\n%s", got)
	}
	if strings.Contains(got, "sd") {
		t.Errorf("a dead, unpaused peer must stay absent:\n%s", got)
	}
}

func TestWorktreeCell(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 19, 36, 0, 0, time.UTC)
	at := now.Add(-36 * time.Minute)
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	cases := []struct {
		p      pigtail.PeerSession
		exists func(string) bool
		want   string
	}{
		{pigtail.PeerSession{WorkspaceName: "feat-role-tools-panel", LastWorktree: "/c/other", LastWorktreeAt: &at}, yes, "feat-role-tools-panel"},
		{pigtail.PeerSession{LastWorktree: "/c/tws/feat-tws-error-pages-ds", LastWorktreeAt: &at}, yes, "feat-tws-error-pages-ds (36m)"},
		{pigtail.PeerSession{LastWorktree: "/c/bigapp/feat-cal-ds-39-slot-hover", LastWorktreeAt: &at}, no, "-"},
		{pigtail.PeerSession{}, yes, "-"},
	}
	for _, c := range cases {
		if got := worktreeCell(c.p, now, c.exists); got != c.want {
			t.Errorf("worktreeCell(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// R4: old junk stays quiet; a malformed file newer than every valid marker
// (the hook changed its format) or malformed files with no valid one speak.
func TestWarnPresence(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var b bytes.Buffer
	warnPresence(&b, "/T", presence.Report{}, errors.New("permission denied"))
	if !strings.Contains(b.String(), "presence markers unreadable") || !strings.Contains(b.String(), "/T") {
		t.Fatalf("read error not reported: %q", b.String())
	}
	b.Reset()
	warnPresence(&b, "/T", presence.Report{Markers: map[string]presence.Marker{"s": {}}, Malformed: 1,
		NewestMalformed: now.Add(-48 * time.Hour), NewestValid: now}, nil)
	if b.Len() != 0 {
		t.Fatalf("old junk must stay quiet: %q", b.String())
	}
	b.Reset()
	warnPresence(&b, "/T", presence.Report{Markers: map[string]presence.Marker{"s": {}}, Malformed: 3,
		NewestMalformed: now, NewestValid: now.Add(-time.Hour)}, nil)
	if !strings.Contains(b.String(), "3 presence marker(s)") {
		t.Fatalf("malformed newer than every valid marker not reported: %q", b.String())
	}
	b.Reset()
	warnPresence(&b, "/T", presence.Report{Malformed: 2, NewestMalformed: now.Add(-48 * time.Hour)}, nil)
	if !strings.Contains(b.String(), "2 presence marker(s)") {
		t.Fatalf("malformed with no valid marker not reported: %q", b.String())
	}
	b.Reset()
	warnPresence(&b, "/T", presence.Report{Markers: map[string]presence.Marker{"s": {Gone: true}}, Gone: 2, NewestValid: now}, nil)
	if b.Len() != 0 {
		t.Fatalf("a deleted worktree is normal life, not a warning: %q", b.String())
	}
}

// R2: session ls displays the daemon's echo; an older daemon drops the
// field, so the CLI lays its own scan over it, newest wins.
func TestOverlayMarkersNewerWins(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	got := []pigtail.PeerSession{
		{SessionID: "empty"},
		{SessionID: "older", LastWorktree: "/wt/old", LastWorktreeAt: &t1},
		{SessionID: "newer", LastWorktree: "/wt/daemon", LastWorktreeAt: &t2},
	}
	overlayMarkers(got, map[string]presence.Marker{
		"empty": {Path: "/wt/a", At: t1},
		"older": {Path: "/wt/b", At: t2},
		"newer": {Path: "/wt/stale-local", At: t1},
	})
	if got[0].LastWorktree != "/wt/a" || got[1].LastWorktree != "/wt/b" || got[2].LastWorktree != "/wt/daemon" {
		t.Fatalf("overlay = %q %q %q", got[0].LastWorktree, got[1].LastWorktree, got[2].LastWorktree)
	}
}

// R6: the glue between snapshot and presence — the spec's "msg send
// <worktree> must work" on the CLI side.
func TestSnapshotAttachesMarkers(t *testing.T) {
	tmp, wt := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no cell table: best-effort, warns
	t.Setenv(cmux.EnvBundledCLI, "")         // no cmux: hermetic
	t.Setenv("PATH", t.TempDir())            // no cmux, no ps on PATH
	const sid = "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(tmp, "claude-wt-abcdef01-"+sid), []byte(wt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	ps, rep := snapshot(selfEnv{SessionID: sid}, &warn)
	if rep.Markers[sid].Path != wt {
		t.Fatalf("report = %+v", rep)
	}
	selfRow := func(ps []pigtail.PeerSession) pigtail.PeerSession {
		t.Helper()
		for _, p := range ps {
			if p.SessionID == sid {
				return p
			}
		}
		t.Fatalf("self row missing from snapshot: %+v", ps)
		return pigtail.PeerSession{}
	}
	if p := selfRow(ps); p.LastWorktree != wt || p.LastWorktreeAt == nil {
		t.Fatalf("self row without its marker: %+v", p)
	}

	// Gone: A an hour ago, then B (since deleted) now. The snapshot carries B,
	// so the store learns the session left A — never a fallback to A.
	tmp2 := t.TempDir()
	t.Setenv("TMPDIR", tmp2)
	gone := filepath.Join(t.TempDir(), "feat-deleted-since")
	for _, m := range []struct {
		name, path string
		at         time.Time
	}{
		{"claude-wt-aaaaaaaa-" + sid, wt, time.Now().Add(-time.Hour)},
		{"claude-wt-bbbbbbbb-" + sid, gone, time.Now()},
	} {
		f := filepath.Join(tmp2, m.name)
		if err := os.WriteFile(f, []byte(m.path+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, m.at, m.at); err != nil {
			t.Fatal(err)
		}
	}
	ps, _ = snapshot(selfEnv{SessionID: sid}, &warn)
	if p := selfRow(ps); p.LastWorktree != gone {
		t.Fatalf("gone newest marker: LastWorktree = %q, want %q (B, not A)", p.LastWorktree, gone)
	}
}

func TestReportSentNamesMarkerLevel(t *testing.T) {
	at := time.Now().Add(-3 * time.Hour)
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	var out, warn bytes.Buffer
	reportSent(&out, &warn, &pigtail.SendMessageResponse{
		Message:   pigtail.Message{ID: "msg_1", ThreadID: "thr_1", ToLabel: "Error UI · globex-21 (it)"},
		To:        pigtail.PeerSession{LastWorktree: "/c/tws/feat-tws-error-pages-ds", LastWorktreeAt: &at},
		MatchedBy: "last worktree",
	})
	if !strings.Contains(out.String(), "last worktree marker") || !strings.Contains(out.String(), "3h") {
		t.Fatalf("send via a marker must say so, with its age: %q", out.String())
	}
}

func TestReportSentWarnsWhenRecipientWillNotWake(t *testing.T) {
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	const label = "Panel UX · globex-b2 (it)"
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const ask = `  Ask the user to type "lis ta boîte pigtail" in its window.` + "\n"
	for _, tc := range []struct {
		delivery string
		want     string // exact lines after the "sent" line; "" = none
	}{
		{pigtail.DeliveryNone, "⚠ " + label + " is idle with no watcher: it will NOT wake up on its own.\n" + ask},
		{pigtail.DeliveryUnknown, "⚠ " + label + " has no watcher and its state is unknown: it may NOT wake up on its own.\n" + ask},
		{pigtail.DeliveryWake, "⏰ " + label + " is idle: pigtail will wake it (screen guard).\n"},
		{pigtail.DeliveryWaiter, ""},
		{pigtail.DeliveryBusy, ""},
		{"", ""}, // daemon older than the field: say nothing
	} {
		t.Run("delivery="+tc.delivery, func(t *testing.T) {
			var out, warn bytes.Buffer
			// The reply path builds the same response: one test covers both
			// msg send and msg reply (both call reportSent).
			reportSent(&out, &warn, &pigtail.SendMessageResponse{
				Message:  pigtail.Message{ID: "msg_1", ThreadID: "thr_1", ToLabel: label, InReplyTo: "msg_0"},
				Pending:  1,
				Delivery: tc.delivery,
			})
			lines := strings.SplitAfterN(out.String(), "\n", 2)
			if !strings.HasPrefix(lines[0], "sent msg_1 → "+label) {
				t.Fatalf("first line must stay the sent line: %q", out.String())
			}
			rest := ""
			if len(lines) == 2 {
				rest = lines[1]
			}
			if rest != tc.want {
				t.Fatalf("after sent line:\n got %q\nwant %q", rest, tc.want)
			}
			if warn.Len() != 0 {
				t.Fatalf("warning must go to stdout, stderr got %q", warn.String())
			}
		})
	}
}

// A20: msg send posts no cmux notification; it sets the inbox pill.
func TestReportSentPostsNoNotification(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := filepath.Join(t.TempDir(), "cmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cmux.EnvBundledCLI, bin)
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "")
	t.Cleanup(cmux.SetCallTimeoutForTest(time.Minute)) // a shell under make check -race can exceed 3 s
	var out, warn bytes.Buffer
	reportSent(&out, &warn, &pigtail.SendMessageResponse{
		Message: pigtail.Message{ID: "msg_1", ThreadID: "thr_1", ToLabel: "x", FromLabel: "y", Body: "hello"},
		To:      pigtail.PeerSession{CmuxWorkspaceID: "WS-1"},
		Pending: 2, Delivery: pigtail.DeliveryNone,
	})
	b, _ := os.ReadFile(log)
	calls := string(b)
	if strings.Contains(calls, "notify") {
		t.Fatalf("cmux notify called: %q", calls)
	}
	if !strings.Contains(calls, "set-status pigtail-inbox 2 messages --icon envelope --priority 70 --workspace WS-1") {
		t.Fatalf("inbox pill: %q", calls)
	}
	if !strings.Contains(out.String(), "sent msg_1") || !strings.Contains(out.String(), "will NOT wake up on its own") || warn.Len() != 0 {
		t.Fatalf("out %q warn %q", out.String(), warn.String())
	}
}

// T3: one line per recipient, a total, and an error (exit 1) only when
// nothing was sent.
func TestReportBroadcastNamesEachOutcome(t *testing.T) {
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	var out, warn bytes.Buffer
	err := reportBroadcast(&out, &warn, &pigtail.BroadcastResponse{Sent: 2, Refused: 1, Results: []pigtail.BroadcastResult{
		{To: pigtail.PeerSession{Label: "alpha · a (mt)"}, MessageID: "msg_1", Delivery: pigtail.DeliveryWaiter},
		{To: pigtail.PeerSession{Label: "beta · b (mt)"}, MessageID: "msg_2", Delivery: pigtail.DeliveryNone},
		{To: pigtail.PeerSession{Label: "gamma · g (it)"}, Refused: pigtail.ErrRelayRefused, Reason: "gamma already has 5 unread messages"},
	}})
	if err != nil {
		t.Fatalf("partial success must not fail: %v", err)
	}
	s := out.String()
	for _, want := range []string{"✓ alpha · a (mt) — msg_1", "✓ beta · b (mt) — msg_2", "will NOT wake up on its own",
		"✗ gamma · g (it) — refused (RELAY_REFUSED): gamma already has 5 unread messages",
		"notice: 2 sent, 1 refused, 1 will not wake on its own"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestReportBroadcastFailsWhenNothingSent(t *testing.T) {
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	var out, warn bytes.Buffer
	if err := reportBroadcast(&out, &warn, &pigtail.BroadcastResponse{Results: []pigtail.BroadcastResult{}}); err == nil ||
		!strings.Contains(err.Error(), "no live session") {
		t.Fatalf("broadcast to nobody: %v", err)
	}
	err := reportBroadcast(&out, &warn, &pigtail.BroadcastResponse{Refused: 1, Results: []pigtail.BroadcastResult{
		{To: pigtail.PeerSession{Label: "x"}, Refused: pigtail.ErrScopeMismatch, Reason: "cannot tell"}}})
	if err == nil || !strings.Contains(err.Error(), "no notice sent") {
		t.Fatalf("all refused: %v", err)
	}
}

// C7: the master's forgotten --cross-scope is named, never silent; with
// nothing skipped, the hint does not appear.
func TestReportBroadcastNamesTheSkippedScopes(t *testing.T) {
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	var out, warn bytes.Buffer
	if err := reportBroadcast(&out, &warn, &pigtail.BroadcastResponse{Sent: 1, OtherScopesSkipped: 23, Results: []pigtail.BroadcastResult{
		{To: pigtail.PeerSession{Label: "m2"}, MessageID: "msg_1", Delivery: pigtail.DeliveryWaiter},
	}}); err != nil {
		t.Fatalf("own-scope success must not fail: %v", err)
	}
	if !strings.Contains(out.String(), "23 live sessions of other scopes not reached — add --cross-scope") {
		t.Errorf("output lacks the skipped hint:\n%s", out.String())
	}
	out.Reset()
	_ = reportBroadcast(&out, &warn, &pigtail.BroadcastResponse{Sent: 1, Results: []pigtail.BroadcastResult{
		{To: pigtail.PeerSession{Label: "m2"}, MessageID: "msg_1", Delivery: pigtail.DeliveryWaiter},
	}})
	if strings.Contains(out.String(), "--cross-scope") {
		t.Errorf("hint printed with nothing skipped:\n%s", out.String())
	}
}

// The send command carries the flags and the session-write annotation (the
// guard of TestScopedCommandsDeclareAccess reads it).
func TestMsgSendDeclaresBroadcastFlags(t *testing.T) {
	t.Parallel()
	cmd := msgSendCmd()
	for _, f := range []string{"all", "cross-scope", "kind"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("msg send lacks --%s", f)
		}
	}
	if cmd.Annotations[scopeAccessKey] != "session-write" {
		t.Errorf("msg send annotation = %q, want session-write", cmd.Annotations[scopeAccessKey])
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Error("msg send with no <to> and no --all must fail")
	}
	_ = cmd.Flags().Set("all", "true")
	if err := cmd.Args(cmd, []string{"hello"}); err != nil {
		t.Errorf("--all takes the text alone: %v", err)
	}
}

// C2: --all carries a notice only. The refusal comes before sessionClient:
// no daemon is reached (the socket below does not exist — a call past the
// check would fail on it with another error).
func TestMsgSendAllRefusesAnotherKind(t *testing.T) {
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	t.Setenv("PIGTAIL_SOCKET", filepath.Join(t.TempDir(), "none.sock"))
	cmd := msgSendCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--all", "--kind", "info", "x"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--all sends a service notice only") {
		t.Fatalf("--all --kind info = %v, want the C2 refusal", err)
	}
}

// D28: a verified pane is announced (⏰), never handed to Ada, and not
// counted among the sessions that will not wake.
func TestReportBroadcastAnnouncesWake(t *testing.T) {
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	var out, warn bytes.Buffer
	err := reportBroadcast(&out, &warn, &pigtail.BroadcastResponse{Sent: 1, Results: []pigtail.BroadcastResult{
		{To: pigtail.PeerSession{Label: "A"}, MessageID: "msg_1", Delivery: pigtail.DeliveryWake},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"  ⏰ A is idle: pigtail will wake it (screen guard).\n", "notice: 1 sent, 0 refused, 0 will not wake on its own"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "lis ta boîte pigtail") {
		t.Fatalf("a wake must not ask a human to type:\n%s", s)
	}
}
