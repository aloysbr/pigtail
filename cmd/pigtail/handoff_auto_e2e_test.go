package main

// End to end, headless: the Stop hook (runWaitHook), a real relay on the same
// router and store as the subject routes, and the handoff CLI. No cmux, no tab,
// no focus.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/handoffdraft"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const e2eWorktree = "/wt/handoff-e2e"

// e2eRelayDraftDir is the DraftDir the test relay is given. The hook always
// writes to hookDraftDir; "" here cuts the daemon half (used once to see the
// tests red, see the plan).
var e2eRelayDraftDir = "use-hook-dir"

type handoffE2E struct {
	t         *testing.T
	s         *stubDaemon
	st        *store.Store
	rl        *handlers.Relay
	c         *pigtail.Client
	hookDir   string // where the Stop hook writes drafts
	mu        sync.Mutex
	clock     time.Time
	alive     map[string]bool
	hookEnvKV map[string]string
}

func (h *handoffE2E) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *handoffE2E) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

func (h *handoffE2E) setAlive(sid string, v bool) {
	h.mu.Lock()
	h.alive[sid] = v
	h.mu.Unlock()
}

func (h *handoffE2E) newRelay(st *store.Store, draftDir string) *handlers.Relay {
	return handlers.NewRelay(&handlers.Services{Store: st, Logf: h.t.Logf}, handlers.RelayConfig{
		DraftDir: draftDir,
		Now:      h.now,
		Alive: func(p pigtail.PeerSession) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return p.EndedAt == nil && h.alive[p.SessionID]
		},
	})
}

func newHandoffE2E(t *testing.T) *handoffE2E {
	t.Helper()
	// One scope (perso) end to end: the session's cell, the peers, the CLI.
	var persoDir string
	withCells(t, func(_, perso string) string { persoDir = perso; return perso })
	h := &handoffE2E{t: t, hookDir: filepath.Join(t.TempDir(), "handoff-draft"), alive: map[string]bool{},
		clock: time.Now().UTC().Truncate(time.Second)}
	relayDir := h.hookDir
	if e2eRelayDraftDir == "" {
		relayDir = ""
	}
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		h.rl = h.newRelay(st, relayDir)
		handlers.RegisterRelayRoutes(r, h.rl, envResponder{})
	})
	h.s, h.st = s, st
	withStubDaemon(t, s, func() {})
	h.c = newClient().WithScope("perso")
	h.hookEnvKV = map[string]string{
		"CLAUDE_HOOK_LOG_DIR":       t.TempDir(),
		"PIGTAIL_HANDOFF_DRAFT_DIR": h.hookDir,
		"CLAUDE_CONFIG_DIR":         persoDir,
	}
	// Bind worktree -> sub_p the way a peer is bound: a workspace at the
	// peer's cwd with an active implement phase of the subject.
	if err := st.CreateProject(&pigtail.Project{ID: "prj_p", Name: "p", RootPath: "/r", DefaultBackend: "git", PortRanges: map[string]pigtail.PortRange{}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(&pigtail.Workspace{ID: "ws_p", ProjectID: "prj_p", State: pigtail.StateBuilding, Path: e2eWorktree, BackendKind: "git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_p", SubjectID: "sub_p", Kind: pigtail.PhaseImplement,
		State: pigtail.PhaseActive, WorkspaceID: "ws_p", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return h
}

// register adds a live hook-sourced peer in the worktree bound to sub_p and
// checks it is bound (the control of the plan).
func (h *handoffE2E) register(sid string) {
	h.t.Helper()
	h.setAlive(sid, true)
	_, err := h.c.PostPeers(context.Background(), pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: sid, Cwd: e2eWorktree, Scope: "perso", CellID: "p", Source: "hook"}}})
	if err != nil {
		h.t.Fatal(err)
	}
	ps, err := h.c.Peers(context.Background(), false)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range ps {
		if p.SessionID == sid {
			if p.SubjectID != "sub_p" {
				h.t.Fatalf("control: %s bound to %q, want sub_p", sid, p.SubjectID)
			}
			return
		}
	}
	h.t.Fatalf("control: %s not in GET /peers: %+v", sid, ps)
}

// stop runs the real Stop hook for sid: an interactive turn promptID that began
// at start and ended with a TL;DR.
func (h *handoffE2E) stop(sid, promptID string, start time.Time, tldr string) {
	h.t.Helper()
	tr := turnTranscript(h.t, promptID, start)
	runWaitHook(strings.NewReader(draftStopPayload(sid, tr, "work\n**TL;DR**\n"+tldr, "")), &bytes.Buffer{}, envOf(h.hookEnvKV))
	if _, err := handoffdraft.Read(h.hookDir, sid); err != nil {
		h.t.Fatalf("hook left no draft for %s: %v", sid, err)
	}
}

func (h *handoffE2E) end(sid string) {
	h.t.Helper()
	if _, err := h.c.PostPeers(context.Background(), pigtail.PeersRequest{End: true,
		Peers: []pigtail.PeerSession{{SessionID: sid, Scope: "perso"}}}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *handoffE2E) autoLines() []pigtail.SubjectLogEntry {
	h.t.Helper()
	logs, err := h.c.Log(context.Background(), "sub_p")
	if err != nil {
		h.t.Fatal(err)
	}
	var out []pigtail.SubjectLogEntry
	for _, l := range logs {
		if l.Kind == pigtail.LogHandoffAuto {
			out = append(out, l)
		}
	}
	return out
}

func (h *handoffE2E) wantOneAuto(id, contains string) {
	h.t.Helper()
	got := h.autoLines()
	if len(got) != 1 {
		h.t.Fatalf("handoff-auto lines = %+v, want exactly 1", got)
	}
	if got[0].ID != id {
		h.t.Fatalf("handoff-auto id = %q, want %q", got[0].ID, id)
	}
	if !strings.Contains(got[0].Text, contains) {
		h.t.Fatalf("handoff-auto text lacks %q:\n%s", contains, got[0].Text)
	}
}

func (h *handoffE2E) draftGone(sid string) {
	h.t.Helper()
	if d, err := handoffdraft.Read(h.hookDir, sid); err == nil {
		h.t.Fatalf("draft of %s still there after promotion: %+v", sid, d)
	}
}

func TestE2EHandoffAutoCleanEnd(t *testing.T) {
	h := newHandoffE2E(t)
	h.register("s-fake")
	h.stop("s-fake", "p-1", h.now().Add(-10*time.Minute), "shipped the thing")
	h.end("s-fake")
	h.wantOneAuto("handoff-auto:s-fake:p-1", "shipped the thing")
	h.draftGone("s-fake")
	h.end("s-fake") // a second SessionEnd adds nothing
	if n := len(h.autoLines()); n != 1 {
		t.Fatalf("after a second end: %d lines, want 1", n)
	}
}

func TestE2EHandoffAutoGoneNoSessionEnd(t *testing.T) {
	h := newHandoffE2E(t)
	h.register("s-fake-2")
	h.stop("s-fake-2", "p-2", h.now().Add(-10*time.Minute), "killed mid-air")
	h.setAlive("s-fake-2", false) // the process is gone, no SessionEnd came
	h.rl.SweepHandoffDraftsForTest(false)
	if n := len(h.autoLines()); n != 0 {
		t.Fatalf("one reading promoted (%d lines): death must be confirmed twice", n)
	}
	h.advance(3 * time.Second)
	h.rl.SweepHandoffDraftsForTest(false)
	h.wantOneAuto("handoff-auto:s-fake-2:p-2", "killed mid-air")
	h.draftGone("s-fake-2")
}

func TestE2EHandoffAutoStartupCatchUp(t *testing.T) {
	h := newHandoffE2E(t)
	h.register("s-fake-3")
	h.stop("s-fake-3", "p-3", h.now().Add(-10*time.Minute), "daemon was down")
	h.setAlive("s-fake-3", false)
	// pigtaild restarts: a new Relay over the same store and draft dir.
	relayDir := h.hookDir
	if e2eRelayDraftDir == "" {
		relayDir = ""
	}
	rl2 := h.newRelay(h.st, relayDir)
	rl2.SweepHandoffDraftsForTest(true)
	if n := len(h.autoLines()); n != 0 {
		t.Fatalf("start-up reading promoted at once (%d lines)", n)
	}
	h.advance(3 * time.Second)
	rl2.SweepHandoffDraftsForTest(false)
	h.wantOneAuto("handoff-auto:s-fake-3:p-3", "fin: startup")
	h.draftGone("s-fake-3")
}

func TestE2EHandoffAutoManualGuard(t *testing.T) {
	h := newHandoffE2E(t)
	h.register("s-fake")
	h.stop("s-fake", "p-4", h.now().Add(-10*time.Minute), "auto text")
	// The agent wrote a handoff itself during the turn (after its start).
	if _, err := runCLIOut(t, h.s, handoffCmd, "write", "sub_p", "manual handoff text"); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Second)
	h.end("s-fake")
	if got := h.autoLines(); len(got) != 0 {
		t.Fatalf("a manual handoff newer than the turn start was doubled: %+v", got)
	}
	out, err := runCLIOut(t, h.s, handoffCmd, "show", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "manual handoff text") || strings.Contains(out, "automatique") {
		t.Fatalf("show = %q, want the manual handoff alone", out)
	}
}

func TestE2EHandoffAutoShowOrder(t *testing.T) {
	h := newHandoffE2E(t)
	h.register("s-fake")
	manualAt := h.now().Add(-time.Hour)
	if _, err := h.c.AppendLog(context.Background(), "sub_p", pigtail.SubjectLogEntry{Kind: "handoff", Text: "manual first", Timestamp: manualAt}); err != nil {
		t.Fatal(err)
	}
	h.stop("s-fake", "p-5", h.now().Add(-10*time.Minute), "turn after the manual one")
	h.end("s-fake")
	h.wantOneAuto("handoff-auto:s-fake:p-5", "turn after the manual one")
	out, err := runCLIOut(t, h.s, handoffCmd, "show", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(out, "manual first"), strings.Index(out, "# handoff automatique")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("show must print the manual handoff then the automatique block:\n%s", out)
	}
	if !strings.Contains(out[j:], "turn after the manual one") {
		t.Fatalf("automatique block lacks the TL;DR:\n%s", out)
	}
}
