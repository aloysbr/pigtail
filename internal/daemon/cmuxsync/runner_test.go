package cmuxsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	winA  = "6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5"
	winB  = "11111111-2222-4333-8444-555555555555"
	winC  = "33333333-4444-4555-8666-777777777777" // a third window, outside Config.Windows (focus D11)
	wsC   = "C0FFEE00-0000-4000-8000-000000000001"
	subID = "sub_side"
)

// cmuxWorld is a stateful fake cmux: renames change the listed titles, pills
// are kept per workspace, and every call is recorded.
type cmuxWorld struct {
	windows    map[string][]string // window → workspace ids, in order
	ws         map[string]*cmux.WorkspaceInfo
	status     map[string]map[string]cmux.StatusEntry
	surfaces   map[string][]string // workspace → open surfaces
	calls      []string
	down       error                             // every call fails with it
	treeDown   error                             // only the surface listing (cmux tree) fails with it
	listDown   error                             // the listings (list-windows, workspace-list, tree) fail with it
	statusDown error                             // only list-status (the reconcile) fails with it
	onList     func()                            // called by list-windows before it answers (a slow cmux advances the clock)
	onStatus   func(ws string)                   // called by list-status before it answers (a slow cmux advances the clock)
	onWrite    func(call, ws, key, value string) // called after a set-status / clear-status went through
	setDown    error                             // only set-status and clear-status fail with it
	onTree     func()                            // called by workspace-list and tree (ListWorkspaces, ListSurfaces) before they answer, like onList for list-windows
	panicOn    string
	writeDown  error // only the writes fail with it (the listings answer)
	// goneOn: workspaces closed under cmux's feet: their writes and their
	// list-status answer not_found (cmux is up, the tab is gone).
	goneOn map[string]bool
}

func newWorld() *cmuxWorld {
	return &cmuxWorld{windows: map[string][]string{}, ws: map[string]*cmux.WorkspaceInfo{},
		status: map[string]map[string]cmux.StatusEntry{}, surfaces: map[string][]string{}}
}

// openSurface adds a pane to a workspace.
func (c *cmuxWorld) openSurface(ws, surface string) {
	c.surfaces[ws] = append(c.surfaces[ws], surface)
}

// closeSurface closes a pane: the workspace stays open.
func (c *cmuxWorld) closeSurface(ws, surface string) {
	var keep []string
	for _, s := range c.surfaces[ws] {
		if s != surface {
			keep = append(keep, s)
		}
	}
	c.surfaces[ws] = keep
}

func (c *cmuxWorld) add(win, id, title string) *cmux.WorkspaceInfo {
	c.windows[win] = append(c.windows[win], id)
	w := &cmux.WorkspaceInfo{ID: id, Title: title}
	c.ws[id] = w
	c.status[id] = map[string]cmux.StatusEntry{}
	return w
}

func (c *cmuxWorld) close(id string) {
	for win, ids := range c.windows {
		var keep []string
		for _, x := range ids {
			if x != id {
				keep = append(keep, x)
			}
		}
		c.windows[win] = keep
	}
	delete(c.ws, id)
}

func (c *cmuxWorld) rec(s ...string) error {
	c.calls = append(c.calls, strings.Join(s, " "))
	if c.panicOn != "" && s[0] == c.panicOn {
		panic("injected")
	}
	if len(s) > 1 && c.goneOn[s[1]] && s[0] != "workspace-list" && s[0] != "tree" {
		return fmt.Errorf("cmux %s: exit status 1: Error: not_found: Workspace not found: %w", s[0], cmux.ErrWorkspaceNotFound)
	}
	if c.writeDown != nil {
		switch s[0] {
		case "list-windows", "workspace-list", "list-status", "tree":
		default:
			return c.writeDown
		}
	}
	return c.down
}

// writes lists the recorded calls that change something.
func (c *cmuxWorld) writes() []string {
	var out []string
	for _, s := range c.calls {
		switch strings.Fields(s)[0] {
		case "list-windows", "workspace-list", "list-status", "tree":
		default:
			out = append(out, s)
		}
	}
	return out
}

func (c *cmuxWorld) Rename(ws, t string) error {
	if err := c.rec("rename", ws, t); err != nil {
		return err
	}
	w := c.ws[ws]
	w.Title, w.CustomTitle, w.HasCustomTitle = t, t, true
	return nil
}
func (c *cmuxWorld) ClearName(ws string) error {
	if err := c.rec("clear-name", ws); err != nil {
		return err
	}
	w := c.ws[ws]
	w.CustomTitle, w.HasCustomTitle = "", false
	return nil
}
func (c *cmuxWorld) SetDescription(ws, d string) error {
	if err := c.rec("set-description", ws, d); err != nil {
		return err
	}
	c.ws[ws].Description = d
	return nil
}
func (c *cmuxWorld) ClearDescription(ws string) error {
	if err := c.rec("clear-description", ws); err != nil {
		return err
	}
	c.ws[ws].Description = ""
	return nil
}
func (c *cmuxWorld) SetStatus(ws string, e cmux.StatusEntry) error {
	if err := c.rec("set-status", ws, e.Key, e.Value); err != nil {
		return err
	}
	if c.setDown != nil {
		return c.setDown
	}
	if _, ok := c.ws[ws]; !ok {
		return fmt.Errorf("cmux set-status: workspace %s not found", ws)
	}
	c.status[ws][e.Key] = e
	if c.onWrite != nil {
		c.onWrite("set-status", ws, e.Key, e.Value)
	}
	return nil
}
func (c *cmuxWorld) ClearStatus(ws, key string) error {
	if err := c.rec("clear-status", ws, key); err != nil {
		return err
	}
	if c.setDown != nil {
		return c.setDown
	}
	if _, ok := c.ws[ws]; !ok {
		return fmt.Errorf("cmux clear-status: workspace %s not found", ws)
	}
	delete(c.status[ws], key)
	if c.onWrite != nil {
		c.onWrite("clear-status", ws, key, "")
	}
	return nil
}
func (c *cmuxWorld) ListStatus(ws string) ([]cmux.StatusEntry, error) {
	if err := c.rec("list-status", ws); err != nil {
		return nil, err
	}
	if c.onStatus != nil {
		c.onStatus(ws)
	}
	if c.statusDown != nil {
		return nil, c.statusDown
	}
	var out []cmux.StatusEntry
	for _, e := range c.status[ws] {
		out = append(out, e)
	}
	return out, nil
}
func (c *cmuxWorld) ListWindows() ([]cmux.Window, error) {
	if err := c.rec("list-windows"); err != nil {
		return nil, err
	}
	if c.onList != nil {
		c.onList()
	}
	if c.listDown != nil {
		return nil, c.listDown
	}
	var out []cmux.Window
	for _, id := range []string{winA, winB, winC} {
		if _, ok := c.windows[id]; ok {
			out = append(out, cmux.Window{ID: id})
		}
	}
	return out, nil
}
func (c *cmuxWorld) ListWorkspaces(win string) ([]cmux.WorkspaceInfo, error) {
	if err := c.rec("workspace-list", win); err != nil {
		return nil, err
	}
	if c.onTree != nil {
		c.onTree()
	}
	if c.listDown != nil {
		return nil, c.listDown
	}
	var out []cmux.WorkspaceInfo
	for _, id := range c.windows[win] {
		out = append(out, *c.ws[id])
	}
	return out, nil
}

func (c *cmuxWorld) ListSurfaces(win string) (map[string][]string, error) {
	if err := c.rec("tree", win); err != nil {
		return nil, err
	}
	if c.onTree != nil {
		c.onTree()
	}
	if c.listDown != nil {
		return nil, c.listDown
	}
	if c.treeDown != nil {
		return nil, c.treeDown
	}
	out := map[string][]string{}
	for _, id := range c.windows[win] {
		out[id] = append([]string{}, c.surfaces[id]...)
	}
	return out, nil
}

type harness struct {
	t      *testing.T
	world  *cmuxWorld
	st     *store.Store
	now    time.Time
	r      *Runner
	events []pigtail.Event
	alive  map[string]bool
	// peersErr is what the registry snapshot (RefreshPeers) returns.
	peersErr error
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	return newHarnessOn(t, cfg, ":memory:")
}

// newHarnessOn is newHarness on the store at dbPath (a file, for the cases a
// second connection must see: storetest.FailFocusSeal).
func newHarnessOn(t *testing.T, cfg Config, dbPath string) *harness {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "ongoing", Scope: "perso", VaultLink: "Perso/Areas/p", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: subID, ThemeID: "thm_p", Title: "Enrichir la sidebar cmux", ShortTitle: "Sidebar pigtail",
		Slug: "sidebar", Status: pigtail.StatusInbox, CreatedAt: t0, UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, world: newWorld(), st: st, now: t0, alive: map[string]bool{}}
	h.r = New(cfg, Deps{
		Sidebar:      h.world,
		Store:        st,
		Alive:        func(p pigtail.PeerSession) bool { return h.alive[p.SessionID] },
		Publish:      func(e pigtail.Event) { h.events = append(h.events, e) },
		Now:          func() time.Time { return h.now },
		Sleep:        func(time.Duration) {},
		Logf:         func(string, ...any) {},
		RefreshPeers: func() ([]string, error) { return nil, h.peersErr },
	})
	return h
}

// step advances the clock by d, then runs one pass.
func (h *harness) step(d time.Duration) {
	h.now = h.now.Add(d)
	h.r.Step()
}

func (h *harness) bind(ws string) {
	h.t.Helper()
	if err := h.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: subID, Source: "attach", BoundAt: h.now}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) binding(ws string) pigtail.SubjectCmuxBinding {
	h.t.Helper()
	b, err := h.st.GetCmuxBinding(ws)
	if err != nil {
		h.t.Fatal(err)
	}
	return *b
}

// session registers a Claude session on its own surface "S-<id>", opened in
// the workspace's panes.
func (h *harness) session(id, ws, status string, alive bool) {
	h.t.Helper()
	h.alive[id] = alive
	h.world.openSurface(ws, "S-"+id)
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: ws, CmuxSurfaceID: "S-" + id, Status: status, UpdatedAt: h.now}); err != nil {
		h.t.Fatal(err)
	}
}

// setStatus changes a registered session's status (its surface stays).
func (h *harness) setStatus(id, ws, status string) {
	h.t.Helper()
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: ws, CmuxSurfaceID: "S-" + id, Status: status, UpdatedAt: h.now}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) notes() []string {
	h.t.Helper()
	es, err := h.st.ListLog(subID, time.Time{})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Text)
	}
	return out
}

func on() Config { return Config{Enabled: true} }

// Switched off (the default): not one cmux call, however long it runs.
func TestRunnerOffMakesNoCmuxCall(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Config{})
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.session("s1", wsA, "waiting", true)
	for i := 0; i < 120; i++ {
		h.step(time.Second)
	}
	if len(h.world.calls) != 0 {
		t.Fatalf("off loop called cmux: %q", h.world.calls)
	}
	if st := h.r.State(); st.Enabled || st.CmuxAvailable {
		t.Fatalf("state %+v", st)
	}
}

// Start: listings (workspaces, then their surfaces), then list-status on every workspace, then the ops — a
// stale pigtail pill persisted by cmux is cleared, a foreign one untouched.
func TestRunnerStartReconcilesThenWrites(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "✳ claude")
	h.world.add(winA, wsB, "lab")
	h.world.status[wsB][KeyAttention] = cmux.StatusEntry{Key: KeyAttention, Value: "stale"}
	h.world.status[wsB]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running"}
	h.session("s1", wsA, "waiting", true)

	h.step(0)
	want := []string{
		"list-windows", "workspace-list " + winA, "tree " + winA,
		"list-status " + wsA, "list-status " + wsB,
		"set-status " + wsA + " pigtail-attn Waiting for you",
		"clear-status " + wsB + " pigtail-attn",
	}
	if strings.Join(h.world.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(h.world.calls, "\n"), strings.Join(want, "\n"))
	}
	if _, ok := h.world.status[wsB]["claude_code"]; !ok {
		t.Fatal("foreign key touched")
	}
	// Idempotent: nothing more to write.
	h.world.calls = nil
	h.step(time.Second)
	if w := h.world.writes(); len(w) != 0 {
		t.Fatalf("second pass wrote %q", w)
	}
	if st := h.r.State(); !st.Enabled || !st.CmuxAvailable || st.LastReconcileAt == nil || len(st.Workspaces) != 2 {
		t.Fatalf("state %+v", st)
	}
}

// cmux down: no write, a backoff with no call before it expires, then a full
// listing and reconcile when cmux answers again.
func TestRunnerBacksOffWhileCmuxIsDown(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true)
	h.world.down = errors.New("socket not found")

	h.step(0)
	if len(h.world.calls) != 1 || len(h.world.writes()) != 0 {
		t.Fatalf("calls %q", h.world.calls)
	}
	st := h.r.State()
	if st.CmuxAvailable || st.ErrorsLastHour != 1 || len(st.Warnings) == 0 {
		t.Fatalf("state %+v", st)
	}
	h.world.calls = nil
	for i := 0; i < 4; i++ {
		h.step(time.Second) // within the 5 s backoff
	}
	if len(h.world.calls) != 0 {
		t.Fatalf("called cmux during backoff: %q", h.world.calls)
	}
	h.world.down = nil
	h.step(2 * time.Second)
	if got := h.world.calls; len(got) < 5 || got[0] != "list-windows" || got[2] != "tree "+winA || got[3] != "list-status "+wsA ||
		got[4] != "set-status "+wsA+" pigtail-attn Waiting for you" {
		t.Fatalf("recovery calls %q", got)
	}
	if !h.r.State().CmuxAvailable {
		t.Fatal("still unavailable")
	}
}

// A failed write is retried on a later pass, after the backoff.
func TestRunnerRetriesFailedWrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.step(0)
	h.session("s1", wsA, "waiting", true)
	h.world.down = errors.New("timeout")
	h.step(time.Second)
	h.world.down = nil
	h.world.calls = nil
	h.step(6 * time.Second)
	if w := h.world.writes(); len(w) != 1 || !strings.HasPrefix(w[0], "set-status "+wsA) {
		t.Fatalf("writes %q", w)
	}
}

// The theme's name is read each pass: a rename shows at the next pass.
func TestRunnerDescriptionFollowsThemeRename(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "Metric login")
	h.bind(wsA)
	h.step(0)
	if b := h.binding(wsA); b.AppliedDescription != "P · inbox" {
		t.Fatalf("first pass description %q, want %q", b.AppliedDescription, "P · inbox")
	}
	name := "Méthode"
	if err := h.st.UpdateTheme("thm_p", pigtail.ThemeUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	if b := h.binding(wsA); b.AppliedDescription != "Méthode · inbox" {
		t.Fatalf("after rename: description %q, want %q", b.AppliedDescription, "Méthode · inbox")
	}
}

// A theme that can no longer be read (deleted between two passes: foreign
// keys are not enforced) leaves the description on the scope; the pass goes on.
func TestRunnerDescriptionFallsBackWhenThemeGone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "Metric login")
	h.bind(wsA)
	h.step(0)
	if b := h.binding(wsA); b.AppliedDescription != "P · inbox" {
		t.Fatalf("first pass description %q, want %q", b.AppliedDescription, "P · inbox")
	}
	if err := h.st.DeleteTheme("thm_p"); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	if b := h.binding(wsA); b.AppliedDescription != "perso · inbox" || b.AppliedTitle != "Sidebar pigtail" {
		t.Fatalf("theme gone: title/description %q / %q, want %q / %q", b.AppliedTitle, b.AppliedDescription, "Sidebar pigtail", "perso · inbox")
	}
	if st := h.r.State(); !st.CmuxAvailable || st.ErrorsLastHour != 0 {
		t.Fatalf("pass interrupted: available=%v errors=%d", st.CmuxAvailable, st.ErrorsLastHour)
	}
}

// Binding: the title and description are posed, the previous title is
// captured first, and both are persisted in the link.
func TestRunnerRenamesBoundWorkspaceAndCapturesPrevTitle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	w := h.world.add(winA, wsA, "Metric login")
	w.CustomTitle, w.HasCustomTitle = "Metric login", true
	h.bind(wsA)
	h.step(0)

	if got := h.world.writes(); strings.Join(got, "|") != "rename "+wsA+" Sidebar pigtail|set-description "+wsA+" P · inbox" {
		t.Fatalf("writes %q", got)
	}
	b := h.binding(wsA)
	if b.PrevTitle != "Metric login" || !b.PrevTitleCustom || b.AppliedTitle != "Sidebar pigtail" || b.AppliedDescription != "P · inbox" {
		t.Fatalf("binding %+v", b)
	}

	// Detach: the custom title from before comes back, description cleared.
	if err := h.st.DeleteCmuxBinding(subID, wsA); err != nil {
		t.Fatal(err)
	}
	h.r.Released(b)
	h.world.calls = nil
	h.step(time.Second)
	if got := h.world.writes(); strings.Join(got, "|") != "rename "+wsA+" Metric login|clear-description "+wsA {
		t.Fatalf("restore writes %q", got)
	}
}

// An automatic title is given back by clearing the name (spec C1).
func TestRunnerRestoresAutomaticTitleByClearName(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "✳ claude")
	h.bind(wsA)
	h.step(0)
	b := h.binding(wsA)
	if b.PrevTitleCustom || b.PrevTitle != "✳ claude" {
		t.Fatalf("prev %+v", b)
	}
	h.r.SetEnabled(false)
	h.world.calls = nil
	h.step(time.Second)
	if got := h.world.writes(); strings.Join(got, "|") != "clear-name "+wsA+"|clear-description "+wsA {
		t.Fatalf("switch-off writes %q", got)
	}
	if b := h.binding(wsA); b.AppliedTitle != "" || b.PrevTitle != "" {
		t.Fatalf("memory not released %+v", b)
	}
}

// Switch-off: owned pills cleared, titles given back, then silence.
func TestRunnerSwitchOffClearsAndRestores(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Idle"}
	h.bind(wsB)
	h.session("s1", wsA, "waiting", true)
	h.step(0)
	h.r.SetEnabled(false)
	h.world.calls = nil
	h.step(time.Second)
	got := strings.Join(h.world.writes(), "|")
	want := "clear-status " + wsA + " pigtail-attn|clear-name " + wsB + "|clear-description " + wsB
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if len(h.world.status[wsA]) != 1 {
		t.Fatalf("foreign pill touched: %+v", h.world.status[wsA])
	}
	h.world.calls = nil
	for i := 0; i < 60; i++ {
		h.step(time.Second)
	}
	if len(h.world.calls) != 0 {
		t.Fatalf("off loop called cmux: %q", h.world.calls)
	}
	if h.r.State().Enabled {
		t.Fatal("still enabled")
	}
}

// The answer to a switch-off never shows the workspaces the loop is about
// to clear.
func TestRunnerStateEmptyOnceSwitchedOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true)
	h.step(0)
	if len(h.r.State().Workspaces) != 1 {
		t.Fatal("no view while on")
	}
	h.r.SetEnabled(false)
	if st := h.r.State(); st.Enabled || len(st.Workspaces) != 0 {
		t.Fatalf("state right after off: %+v", st)
	}
}

// A manual rename of a bound workspace is put back (D5), and the short title
// keeps following the subject. A retitle only hands back a description
// edited by hand: the title is always pigtail's.
func TestRunnerManualRenameRevertedAndRetitleKeepsDescription(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.step(0)

	w := h.world.ws[wsA]
	w.Title, w.CustomTitle = "Mon nom", "Mon nom"
	h.world.calls = nil
	h.step(31 * time.Second) // slow tick: fresh listing
	if b := h.binding(wsA); b.TitleOwner != pigtail.CmuxOwnerPigtail || b.DescriptionOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("binding %+v", b)
	}
	if got := h.world.writes(); len(got) != 1 || got[0] != "rename "+wsA+" Sidebar pigtail" {
		t.Fatalf("revert writes %q", got)
	}
	sub, _ := h.st.GetSubject(subID)
	sub.ShortTitle = "Autre titre"
	if err := h.st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	h.world.calls = nil
	h.step(time.Second)
	if got := h.world.writes(); len(got) != 1 || got[0] != "rename "+wsA+" Autre titre" {
		t.Fatalf("after short title change %q", got)
	}
	for _, n := range h.notes() {
		if strings.Contains(n, "titre cmux repris") {
			t.Fatalf("title takeover noted: %q", n)
		}
	}

	// Description edited by hand: the user's, until a retitle hands it back.
	w.Description = "à moi"
	h.step(31 * time.Second)
	if b := h.binding(wsA); b.DescriptionOwner != pigtail.CmuxOwnerUser || b.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("description takeover %+v", b)
	}
	h.world.calls = nil
	for i := 0; i < 70; i++ {
		h.step(time.Second)
	}
	for _, c := range h.world.writes() {
		if strings.HasPrefix(c, "set-description") || strings.HasPrefix(c, "rename") {
			t.Fatalf("wrote after description takeover: %q", c)
		}
	}
	if err := h.st.ResetCmuxOwners(wsA); err != nil {
		t.Fatal(err)
	}
	h.r.Nudge()
	h.world.calls = nil
	h.step(time.Second)
	if got := h.world.writes(); len(got) != 1 || got[0] != "set-description "+wsA+" P · inbox" {
		t.Fatalf("after retitle %q", got)
	}
}

// A listing read before (or less than 1.5 s after) pigtail's own rename is
// not trusted to detect a manual one.
func TestRunnerIgnoresListingReadBeforeOwnRename(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.step(0) // listing: unbound, title "a"
	h.bind(wsA)
	for i := 0; i < 5; i++ {
		h.step(time.Second) // renames, then keeps computing on the old listing
	}
	if b := h.binding(wsA); b.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("false takeover %+v", b)
	}
	h.step(30 * time.Second)
	if b := h.binding(wsA); b.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("false takeover after a fresh listing %+v", b)
	}
	if n := revertNotes(h); len(n) != 0 {
		t.Fatalf("false revert: %q", n)
	}
}

// A slow cmux listing (3 s) read before pigtail's own rename must not be
// taken for a manual one by the fast passes that compute on it (D2).
func TestRunnerSlowListingBeforeOwnRenameIsNoTakeover(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.world.onList = func() { h.now = h.now.Add(3 * time.Second) }
	h.step(0) // slow pass: the listing shows "a" and takes 3 s, then pigtail renames
	h.world.onList = nil
	for i := 0; i < 5; i++ {
		h.step(time.Second) // fast passes, on that listing
	}
	if b := h.binding(wsA); b.TitleOwner != pigtail.CmuxOwnerPigtail || b.DescriptionOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("false takeover %+v, notes %q", b, h.notes())
	}
	h.step(30 * time.Second) // a fresh listing shows pigtail's own text
	if b := h.binding(wsA); b.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("false takeover after a fresh listing %+v", b)
	}
	if n := revertNotes(h); len(n) != 0 {
		t.Fatalf("false revert: %q", n)
	}
}

// The guard must not swallow a real rename made after pigtail's write: it is
// put back, with one note (D5).
func TestRunnerManualRenameAfterSlowListingReverted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.world.onList = func() { h.now = h.now.Add(3 * time.Second) }
	h.step(0)
	h.world.onList = nil
	h.step(10 * time.Second)
	w := h.world.ws[wsA]
	w.Title, w.CustomTitle, w.HasCustomTitle = "Mon nom", "Mon nom", true
	h.world.calls = nil
	h.step(30 * time.Second) // slow tick: a listing started well after the write
	if got := h.world.writes(); countPrefix(got, "rename "+wsA+" Sidebar pigtail") != 1 {
		t.Fatalf("real rename missed: %q", got)
	}
	if b := h.binding(wsA); b.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("binding %+v", b)
	}
	if n := revertNotes(h); len(n) != 1 {
		t.Fatalf("notes %q", n)
	}
}

// A workspace missing from one listing keeps its link; missing from two
// consecutive ones, the link is dropped and noted.
func TestRunnerDropsLinkAfterTwoMissedListings(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.bind(wsA)
	h.step(0)
	h.world.close(wsA)
	h.step(30 * time.Second)
	if b := h.binding(wsA); b.MissingTicks != 1 {
		t.Fatalf("ticks %+v", b)
	}
	h.step(30 * time.Second)
	if _, err := h.st.GetCmuxBinding(wsA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link kept: %v", err)
	}
	if n := h.notes(); len(n) == 0 || !strings.Contains(n[len(n)-1], "fermé") {
		t.Fatalf("notes %q", n)
	}
}

func TestRunnerKeepsLinkWhenWorkspaceReturns(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.step(0)
	saved := *h.world.ws[wsA]
	h.world.close(wsA)
	h.step(30 * time.Second)
	h.world.windows[winA] = append(h.world.windows[winA], wsA)
	h.world.ws[wsA] = &saved
	h.step(30 * time.Second)
	if b := h.binding(wsA); b.MissingTicks != 0 {
		t.Fatalf("ticks %+v", b)
	}
}

// Panics are recovered and followed by a rest; the fifth within ten minutes
// switches the loop off and clears its pills.
func TestRunnerSwitchesOffAfterRepeatedPanics(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true)
	h.step(0)
	h.world.panicOn = "list-windows"
	for i := 0; i < 4; i++ {
		h.step(31 * time.Second)
		if st := h.r.State(); !st.Enabled || st.DisabledByPanic {
			t.Fatalf("off after %d panics", i+1)
		}
	}
	// During the 30 s rest after a panic, nothing is attempted.
	h.world.calls = nil
	h.step(10 * time.Second)
	if len(h.world.calls) != 0 {
		t.Fatalf("called during rest: %q", h.world.calls)
	}
	h.world.calls = nil
	h.step(31 * time.Second)
	st := h.r.State()
	if st.Enabled || !st.DisabledByPanic || len(st.Warnings) == 0 {
		t.Fatalf("state %+v", st)
	}
	if got := h.world.writes(); len(got) != 1 || got[0] != "clear-status "+wsA+" pigtail-attn" {
		t.Fatalf("pills not cleared: %q", got)
	}
	warned := 0
	for _, e := range h.events {
		if e.Type == pigtail.EventResourcesWarning && e.Data["source"] == "cmuxsync" {
			warned++
		}
	}
	if warned != 5 {
		t.Fatalf("%d warnings", warned)
	}
	// Switching on again lifts it.
	h.world.panicOn = ""
	h.r.SetEnabled(true)
	h.step(time.Second)
	if st := h.r.State(); !st.Enabled || st.DisabledByPanic {
		t.Fatalf("state %+v", st)
	}
}

// Attention: waiting goes up at once and down after the hold; a dead
// session lights "Dead" after two observations; a clean end lights nothing.
func TestRunnerAttention(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.world.add(winA, wsC, "c")
	h.session("s1", wsA, "waiting", true)
	h.session("s2", wsB, "idle", false)
	h.session("s3", wsC, "idle", true)
	if err := h.st.EndPeer("s3", h.now); err != nil {
		t.Fatal(err)
	}
	h.alive["s3"] = false
	h.step(0)
	h.step(time.Second)
	pill := func(ws string) string { return h.world.status[ws][KeyAttention].Value }
	if pill(wsA) != "Waiting for you" || pill(wsB) != "Dead" || pill(wsC) != "" {
		t.Fatalf("pills a=%q b=%q c=%q", pill(wsA), pill(wsB), pill(wsC))
	}
	h.session("s1", wsA, "busy", true)
	h.step(time.Second)
	if pill(wsA) == "" {
		t.Fatal("waiting dropped before the hold")
	}
	h.step(2 * time.Second)
	if pill(wsA) != "" {
		t.Fatalf("waiting kept: %q", pill(wsA))
	}
}

// Events: one per change of a workspace's wanted state, tagged with the
// bound subject's scope (work when none), never carrying a title.
func TestRunnerPublishesSidebarChanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.bind(wsA)
	h.session("s1", wsA, "waiting", true)
	h.session("s2", wsB, "waiting", true)
	h.step(0)
	h.step(time.Second)
	var got []pigtail.Event
	for _, e := range h.events {
		if e.Type == pigtail.EventCmuxSidebarChanged {
			got = append(got, e)
		}
	}
	if len(got) != 2 {
		t.Fatalf("events %+v", got)
	}
	for _, e := range got {
		ws := e.Data["cmux_workspace_id"]
		switch ws {
		case wsA:
			if e.Scope != "perso" || e.Data["subject_id"] != subID {
				t.Fatalf("bound event %+v", e)
			}
		case wsB:
			if e.Scope != "globex" || e.Data["subject_id"] != "" {
				t.Fatalf("unbound event %+v", e)
			}
		}
		if s := fmt.Sprint(e.Data); strings.Contains(s, "Sidebar pigtail") {
			t.Fatalf("event carries a title: %s", s)
		}
	}
}

// Restricted to one window: nothing outside it is listed for pills or
// written, while the link rule still sees every window.
func TestRunnerWindowFilter(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Config{Enabled: true, Windows: []string{strings.ToLower(winB)}})
	h.world.add(winA, wsA, "a")
	h.world.add(winB, wsB, "b")
	h.bind(wsA)
	h.session("s1", wsA, "waiting", true)
	h.session("s2", wsB, "waiting", true)
	h.step(0)
	for _, c := range h.world.calls {
		if strings.Contains(c, wsA) {
			t.Fatalf("touched a workspace outside the window: %q", c)
		}
	}
	if h.world.status[wsB][KeyAttention].Value == "" {
		t.Fatal("allowed window not written")
	}
	h.step(30 * time.Second)
	if b := h.binding(wsA); b.MissingTicks != 0 {
		t.Fatalf("link outside the window counted missing: %+v", b)
	}
}

// Shutdown clears the owned pills (titles stay: the link remembers them).
func TestRunnerShutdownClearsPills(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "waiting", true)
	h.bind(wsA)
	h.step(0)
	h.world.calls = nil
	h.r.shutdown()
	if got := h.world.writes(); len(got) != 1 || got[0] != "clear-status "+wsA+" pigtail-attn" {
		t.Fatalf("shutdown writes %q", got)
	}
}

// A dead session whose pane was closed lights nothing, even though
// `cmux sessions list` (RefreshPeers) still returns it: that list is a
// persisted history, not the live sessions (bug of 2026-10-01). A dead
// session whose pane is still open reads "Dead".
func TestRunnerDeadNeedsAnOpenSurface(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.r.deps.RefreshPeers = func() ([]string, error) { return []string{"live", "gone", "listed"}, nil }
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.session("live", wsA, "idle", true)
	h.session("gone", wsA, "idle", false)
	h.world.closeSurface(wsA, "S-gone")
	h.session("listed", wsB, "idle", false)
	h.step(time.Second)
	h.step(time.Second)
	h.step(time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "" {
		t.Fatalf("closed pane's session lit %q", v)
	}
	if v := h.world.status[wsB][KeyAttention].Value; v != "Dead" {
		t.Fatalf("dead session on an open pane: %q", v)
	}
}

// Closing the pane of a dead session clears its pill at the next listing.
func TestRunnerDeadClearsWhenItsSurfaceCloses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("live", wsA, "idle", true)
	h.session("gone", wsA, "idle", false)
	h.step(time.Second)
	h.step(time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Dead" {
		t.Fatalf("before closing: %q", v)
	}
	h.world.closeSurface(wsA, "S-gone")
	h.step(30 * time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "" {
		t.Fatalf("after closing the pane: %q", v)
	}
}

// The surface listing never answered: no dead pill can be proven, so none
// shows — waiting still does.
func TestRunnerSurfaceListingDownNeverDead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.treeDown = errors.New("tree: unexpected output")
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.session("gone", wsA, "idle", false)
	h.session("asks", wsB, "waiting", true)
	for i := 0; i < 6; i++ {
		h.step(30 * time.Second)
	}
	if v := h.world.status[wsA][KeyAttention].Value; v != "" {
		t.Fatalf("dead pill without a surface listing: %q", v)
	}
	if v := h.world.status[wsB][KeyAttention].Value; v != "Waiting for you" {
		t.Fatalf("waiting must not depend on the surface listing: %q", v)
	}
	if st := h.r.State(); !st.CmuxAvailable {
		t.Fatalf("a failing tree alone must not mark cmux unreachable: %+v", st)
	}
}

// A transient failure of the surface listing keeps the last set: a real
// "Dead" neither blinks off nor is rewritten, and a pane closed before the
// failure stays closed.
func TestRunnerSurfaceListingFailureKeepsLastSet(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.session("dead", wsA, "idle", false)
	h.session("closed", wsB, "idle", false)
	h.world.closeSurface(wsB, "S-closed")
	h.step(time.Second)
	h.step(time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Dead" {
		t.Fatalf("setup: %q", v)
	}
	h.world.treeDown = errors.New("tree: timeout")
	h.world.calls = nil
	for i := 0; i < 3; i++ {
		h.step(30 * time.Second)
	}
	h.world.treeDown = nil
	h.step(30 * time.Second)
	if got := h.world.writes(); len(got) != 0 {
		t.Fatalf("a transient tree failure caused writes: %q", got)
	}
	if v := h.world.status[wsB][KeyAttention].Value; v != "" {
		t.Fatalf("a pane closed before the failure lit %q", v)
	}
}

// A surface listing that stays down past its age bound is no longer trusted:
// the pill goes once, and does not come back while the listing fails.
func TestRunnerStaleSurfaceSetIsDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("dead", wsA, "idle", false)
	h.step(time.Second)
	h.step(time.Second)
	if v := h.world.status[wsA][KeyAttention].Value; v != "Dead" {
		t.Fatalf("setup: %q", v)
	}
	h.world.treeDown = errors.New("tree: unexpected output")
	h.world.calls = nil
	for i := 0; i < 12; i++ {
		h.step(30 * time.Second)
	}
	if v := h.world.status[wsA][KeyAttention].Value; v != "" {
		t.Fatalf("stale surface set still trusted: %q", v)
	}
	if got := h.world.writes(); len(got) != 1 {
		t.Fatalf("want exactly one clear, got %q", got)
	}
}

// A workspace detached while the loop is off gets its title back once the
// loop is switched on again.
func TestRunnerRestoresDetachWhileOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Config{})
	h.world.add(winA, wsA, "a")
	b := pigtail.SubjectCmuxBinding{CmuxWorkspaceID: wsA, SubjectID: subID, AppliedTitle: "Sidebar pigtail",
		PrevTitle: "Mon nom", PrevTitleCustom: true, TitleOwner: "pigtail", DescriptionOwner: "pigtail"}
	h.r.Released(b)
	h.step(time.Second)
	if len(h.world.calls) != 0 {
		t.Fatalf("off loop called cmux: %q", h.world.calls)
	}
	h.r.SetEnabled(true)
	h.step(time.Second)
	if got := h.world.writes(); len(got) != 1 || got[0] != "rename "+wsA+" Mon nom" {
		t.Fatalf("writes %q", got)
	}
}

// TestRunnerMaintenancePillTick: the pill through a real tick, against the
// simulated cmux of this file (cmuxWorld, newHarness).
func TestRunnerMaintenancePillTick(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	var (
		st  maintenance.State
		ok  bool
		err error
	)
	h.r.deps.Maintenance = func() (maintenance.State, bool, error) { return st, ok, err }

	st, ok = maintenance.State{ID: "mnt_a", On: true, Freeze: true, Since: h.now}, true
	h.step(0)
	if !hasCall(h.world.writes(), "set-status "+wsA+" pigtail-mnt Maintenance · freeze") {
		t.Fatalf("on: no pill written: %q", h.world.writes())
	}
	if e := h.world.status[wsA][KeyMaintenance]; e.Priority != 80 || e.Color != "#FF9500" || e.Icon != "wrench.and.screwdriver.fill" {
		t.Fatalf("pill entry %+v", e)
	}

	h.world.calls = nil
	st, ok, err = maintenance.State{}, false, errors.New("unexpected EOF")
	h.step(time.Second)
	if !hasCall(h.world.writes(), "set-status "+wsA+" pigtail-mnt Maintenance · state error") || h.world.status[wsA][KeyMaintenance].Color != "#FF3B30" {
		t.Fatalf("error pill: %q", h.world.writes())
	}

	h.world.calls = nil
	err = nil // lifted: no state, no pill
	h.step(time.Second)
	if !hasCall(h.world.writes(), "clear-status "+wsA+" pigtail-mnt") {
		t.Fatalf("lift: pill not cleared: %q", h.world.writes())
	}
}

// TestRunnerMaintenanceOwnerPillTick: the owner's tile through real ticks —
// marked, back to the common pill when the owner ends, never on the error
// pill, cleared on lift.
func TestRunnerMaintenanceOwnerPillTick(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.session("sid-owner", wsA, "busy", true)
	h.session("sid-b", wsB, "busy", true)
	var (
		st  maintenance.State
		ok  bool
		err error
	)
	h.r.deps.Maintenance = func() (maintenance.State, bool, error) { return st, ok, err }

	st, ok = maintenance.State{ID: "mnt_a", On: true, Freeze: true, Since: h.now, Owner: "sid-owner"}, true
	h.step(0)
	if !hasCall(h.world.writes(), "set-status "+wsA+" pigtail-mnt Maintenance · freeze · set here") {
		t.Fatalf("owner tile not marked: %q", h.world.writes())
	}
	if e := h.world.status[wsA][KeyMaintenance]; e.Icon != "key.fill" || e.Color != "#FF9500" || e.Priority != 80 {
		t.Fatalf("owner entry %+v", e)
	}
	if e := h.world.status[wsB][KeyMaintenance]; e.Value != "Maintenance · freeze" || e.Icon != "wrench.and.screwdriver.fill" {
		t.Fatalf("other tile %+v", e)
	}

	// Unreadable state: the owner is unreadable too, no key anywhere.
	h.world.calls = nil
	st, ok, err = maintenance.State{}, false, errors.New("unexpected EOF")
	h.step(time.Second)
	if e := h.world.status[wsA][KeyMaintenance]; e.Value != "Maintenance · state error" || e.Icon != "wrench.and.screwdriver.fill" {
		t.Fatalf("error pill on owner tile %+v", e)
	}

	// Readable again, then the owner ends (D2): back to the common pill.
	err = nil
	st, ok = maintenance.State{ID: "mnt_a", On: true, Freeze: true, Since: h.now, Owner: "sid-owner"}, true
	h.step(time.Second)
	if e := h.world.status[wsA][KeyMaintenance]; e.Icon != "key.fill" {
		t.Fatalf("owner tile not marked again %+v", e)
	}
	h.world.calls = nil
	h.alive["sid-owner"] = false
	if err := h.st.EndPeer("sid-owner", h.now); err != nil {
		t.Fatal(err)
	}
	h.step(31 * time.Second)
	if e := h.world.status[wsA][KeyMaintenance]; e.Value != "Maintenance · freeze" || e.Icon != "wrench.and.screwdriver.fill" {
		t.Fatalf("ended owner still marked %+v (writes %q)", e, h.world.writes())
	}

	// Lift: cleared everywhere.
	h.world.calls = nil
	st, ok = maintenance.State{}, false
	h.step(time.Second)
	for _, ws := range []string{wsA, wsB} {
		if !hasCall(h.world.writes(), "clear-status "+ws+" pigtail-mnt") {
			t.Fatalf("lift: %s not cleared: %q", ws, h.world.writes())
		}
	}
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// D1 on an unbound workspace: a live session's cell is its description,
// cleared when the session ends and on switch-off; binding the workspace
// later captures "" as the previous description, not pigtail's cell text.
func TestRunnerUnboundCellDescriptionLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	cellPeer := func(id string) {
		h.t.Helper()
		h.session(id, wsA, "busy", true)
		if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: wsA, CmuxSurfaceID: "S-" + id, Status: "busy", CellID: "w1", UpdatedAt: h.now}); err != nil {
			h.t.Fatal(err)
		}
	}
	cellPeer("s1")
	h.step(0)
	if got := strings.Join(h.world.writes(), "|"); !strings.Contains(got, "set-description "+wsA+" w1") {
		t.Fatalf("cell not posed: %q", got)
	}

	// The session ends: pigtail's cell text goes.
	h.world.calls = nil
	if err := h.st.EndPeer("s1", h.now); err != nil {
		t.Fatal(err)
	}
	h.step(31 * time.Second)
	if got := strings.Join(h.world.writes(), "|"); !strings.Contains(got, "clear-description "+wsA) {
		t.Fatalf("cell text not cleared on end: %q", got)
	}

	// Live again, then switch-off: what pigtail posed is given back.
	cellPeer("s2")
	h.step(31 * time.Second)
	if h.world.ws[wsA].Description != "w1" {
		t.Fatalf("cell not posed again: %q", h.world.ws[wsA].Description)
	}
	h.r.SetEnabled(false)
	h.world.calls = nil
	h.step(time.Second)
	if got := strings.Join(h.world.writes(), "|"); !strings.Contains(got, "clear-description "+wsA) {
		t.Fatalf("switch-off kept the cell text: %q", got)
	}

	// On again with the cell posed, then bound: nothing to give back.
	h.r.SetEnabled(true)
	h.step(time.Second)
	if h.world.ws[wsA].Description != "w1" {
		t.Fatalf("cell not posed after switch-on: %q", h.world.ws[wsA].Description)
	}
	h.bind(wsA)
	h.step(31 * time.Second)
	if b := h.binding(wsA); b.PrevDescription != "" || b.AppliedDescription != "P · inbox · w1" {
		t.Fatalf("binding %+v", b)
	}
}

// A description written by hand over pigtail's cell text is the user's:
// switch-off leaves it.
func TestRunnerUnboundCellTextTakenByHand(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.session("s1", wsA, "busy", true)
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: wsA, CmuxSurfaceID: "S-s1", Status: "busy", CellID: "w1", UpdatedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.step(0)
	h.world.ws[wsA].Description = "mes notes"
	h.step(31 * time.Second)
	h.r.SetEnabled(false)
	h.world.calls = nil
	h.step(time.Second)
	if got := strings.Join(h.world.writes(), "|"); strings.Contains(got, "description") {
		t.Fatalf("hand-written description touched: %q", got)
	}
}

// F4: each recorded wait goes to its own slot by source — a held one to
// Held with its holder pid, never to Declared; a source the loop does not
// know goes nowhere.
func TestPlaceWaitBySource(t *testing.T) {
	t.Parallel()
	var s Session
	if !placeWait(&s, pigtail.SessionWait{Source: pigtail.WaitSourceHeld, Type: "mac", Since: t0, HolderPID: 777}) {
		t.Fatal("held refused")
	}
	if s.Held.Type != "mac" || s.Held.HolderPID != 777 || s.Declared.Set() || s.Auto.Set() {
		t.Fatalf("held placed as %+v", s)
	}
	var u Session
	if placeWait(&u, pigtail.SessionWait{Source: "later", Type: "question", Since: t0}) {
		t.Fatal("unknown source accepted")
	}
	if u.Auto.Set() || u.Declared.Set() || u.Held.Set() {
		t.Fatalf("unknown source placed as %+v", u)
	}
}

// A held Mac wait reads "Waiting for Mac", outlives the turns,
// and ends — in the store too — once its holder dies.
func TestRunnerHeldWaitEndsWithItsHolder(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	holderAlive := true
	h.r.deps.PidAlive = func(pid int) bool { return pid != 777 || holderAlive }
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "idle", h.now.Add(-time.Minute))
	h.step(0)
	if err := h.st.SetSessionWait("s1", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, Since: h.now, HolderPID: 777}); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if p := h.pill(); p != "Waiting for Mac" {
		t.Fatalf("held pill = %q", p)
	}
	h.nativeWait("s1", "busy", "", h.now.Add(time.Second))
	h.step(2 * time.Second)
	h.nativeWait("s1", "idle", "", h.now.Add(time.Second))
	h.step(2 * time.Second)
	if p := h.pill(); p != "Waiting for Mac" {
		t.Fatalf("held pill after a turn = %q", p)
	}
	holderAlive = false
	h.step(time.Second)
	if p := h.pill(); strings.HasPrefix(p, "Waiting for Mac") {
		t.Fatalf("pill after the holder died = %q", p)
	}
	if ws := h.waits("s1"); len(ws) != 0 {
		t.Fatalf("store kept %+v", ws)
	}
}

func TestCloseMissingClearsOffTopic(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	calls := 0
	h.r.deps.MarkersChanged = func() { calls++ }
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	for _, ws := range []string{wsA, wsB} {
		if err := h.st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: "perso", DeclaredAt: h.now}); err != nil {
			t.Fatal(err)
		}
	}
	h.step(0)
	savedB := *h.world.ws[wsB]
	h.world.close(wsA)
	h.world.close(wsB)
	h.step(30 * time.Second) // 1st listing without both (e.g. a cmux restart)
	for _, ws := range []string{wsA, wsB} {
		if o, err := h.st.GetOffTopic(ws); err != nil || o.MissingTicks != 1 {
			t.Fatalf("%s after one missed listing = %+v %v, want kept with missing_ticks 1", ws, o, err)
		}
	}
	h.world.windows[winA] = append(h.world.windows[winA], wsB) // wsB comes back, same UUID
	h.world.ws[wsB] = &savedB
	h.step(30 * time.Second) // 2nd listing: wsA still missing, wsB seen again
	if _, err := h.st.GetOffTopic(wsA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wsA kept after two missed listings: %v", err)
	}
	if o, err := h.st.GetOffTopic(wsB); err != nil || o.MissingTicks != 0 {
		t.Fatalf("wsB not reset when seen again: %+v %v", o, err)
	}
	if calls != 1 {
		t.Fatalf("MarkersChanged calls = %d, want 1 (only the drop of wsA)", calls)
	}
}

func TestCloseMissingOffTopicSeesEveryWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Config{Enabled: true, Windows: []string{strings.ToLower(winB)}})
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsC, "c")
	h.world.add(winB, wsB, "b")
	for _, ws := range []string{wsA, wsC} {
		if err := h.st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: "perso", DeclaredAt: h.now}); err != nil {
			t.Fatal(err)
		}
	}
	h.step(0)
	h.world.close(wsA) // closed outside the driven window
	h.step(30 * time.Second)
	if o, err := h.st.GetOffTopic(wsA); err != nil || o.MissingTicks != 1 {
		t.Fatalf("wsA closed outside the window = %+v %v, want 1", o, err)
	}
	if o, err := h.st.GetOffTopic(wsC); err != nil || o.MissingTicks != 0 {
		t.Fatalf("live declaration outside the window counted missing: %+v %v", o, err)
	}
}

func TestCloseMissingLinkDropSyncsMarkers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	calls := 0
	h.r.deps.MarkersChanged = func() { calls++ }
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.bind(wsA)
	h.step(0)
	h.world.close(wsA)
	h.step(30 * time.Second) // 1st missed listing: link kept (missing_ticks 1), nothing to rewrite
	if calls != 0 {
		t.Fatalf("MarkersChanged calls after one missed listing = %d, want 0", calls)
	}
	h.step(30 * time.Second) // 2nd: the link is dropped
	if _, err := h.st.GetCmuxBinding(wsA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link kept after two missed listings: %v", err)
	}
	if calls != 1 {
		t.Fatalf("MarkersChanged calls after the link drop = %d, want 1", calls)
	}
}

// TestRunnerPublishesOffTopicPill breaks if sync stops feeding the store's declarations into Input.OffTopic (S9).
func TestRunnerPublishesOffTopicPill(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	if err := h.st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: wsA, Scope: "perso", DeclaredAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.step(0)
	if v := h.world.status[wsA][KeyOffTopic].Value; v != "Off-topic" {
		t.Fatalf("off-topic pill on wsA = %q, want \"Off-topic\" (sync lost ListOffTopic → Input.OffTopic?)", v)
	}
	if err := h.st.DeleteOffTopic(wsA); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if e, ok := h.world.status[wsA][KeyOffTopic]; ok {
		t.Fatalf("pill kept after the lift: %+v", e)
	}

	w := newHarness(t, Config{Enabled: true, Windows: []string{strings.ToLower(winB)}})
	w.world.add(winA, wsA, "a")
	w.world.add(winB, wsB, "b")
	for _, ws := range []string{wsA, wsB} {
		if err := w.st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: "perso", DeclaredAt: w.now}); err != nil {
			t.Fatal(err)
		}
	}
	w.step(0)
	if v := w.world.status[wsB][KeyOffTopic].Value; v != "Off-topic" {
		t.Fatalf("pill in the driven window = %q, want \"Off-topic\"", v) // positive control
	}
	if e, ok := w.world.status[wsA][KeyOffTopic]; ok {
		t.Fatalf("pill written outside the driven window: %+v", e)
	}
}

// TestListOffTopicLogsOncePerError: an unreadable table logs one line, not one per pass.
func TestListOffTopicLogsOncePerError(t *testing.T) {
	t.Parallel()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close() // every read now fails with the same "sql: database is closed"
	var logged []string
	r := &Runner{deps: Deps{Store: st, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}}
	for i := 0; i < 2; i++ {
		if ots := r.listOffTopic(); ots != nil {
			t.Fatalf("closed store read = %+v, want none", ots)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "cmuxsync: list off-topic: ") {
		t.Fatalf("off-topic log lines = %d %q, want 1 per distinct error", len(logged), logged)
	}
}

// --- Task 16: a Standby whose workspace is gone is purged -----------------

type goneCalls struct {
	h     *harness
	calls []string
}

// standbyGone wires Deps.StandbyGone like the daemon (the row goes) and
// records each call as "session/workspace".
func standbyGone(h *harness) *goneCalls {
	g := &goneCalls{h: h}
	h.r.deps.StandbyGone = func(session, ws string) error {
		g.calls = append(g.calls, session+"/"+ws)
		_, err := h.st.ClearStandby(session, h.now)
		return err
	}
	return g
}

func (h *harness) standbyTicks(session string) int {
	h.t.Helper()
	sb, err := h.st.GetStandby(session)
	if err != nil || sb == nil {
		h.t.Fatalf("Standby of %s: %+v, %v", session, sb, err)
	}
	return sb.MissingTicks
}

func standbyOn(h *harness, session, ws string) {
	h.t.Helper()
	h.world.add(winA, ws, "w")
	h.session(session, ws, "idle", true)
	if err := h.st.SetStandby(session, h.now); err != nil {
		h.t.Fatal(err)
	}
}

func TestStandbyPurgedWhenWorkspaceGone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	g := standbyGone(h)
	h.world.add(winA, wsB, "b") // keeps a window listed
	standbyOn(h, "s1", wsA)
	h.step(0)
	h.world.close(wsA)
	h.step(30 * time.Second) // 1st listing without it
	if len(g.calls) != 0 || h.standbyTicks("s1") != 1 {
		t.Fatalf("after one listing: calls %v", g.calls)
	}
	h.step(30 * time.Second)
	if len(g.calls) != 1 || g.calls[0] != "s1/"+wsA {
		t.Fatalf("after two listings: calls %v", g.calls)
	}
	if sb, _ := h.st.GetStandby("s1"); sb != nil {
		t.Fatalf("row kept: %+v", sb)
	}
}

func TestStandbyKeptWhenWorkspaceBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	g := standbyGone(h)
	h.world.add(winA, wsB, "b")
	standbyOn(h, "s1", wsA)
	h.step(0)
	h.world.close(wsA)
	h.step(30 * time.Second)
	if h.standbyTicks("s1") != 1 {
		t.Fatal("no tick")
	}
	h.world.add(winA, wsA, "w") // back
	h.step(30 * time.Second)
	if h.standbyTicks("s1") != 0 {
		t.Fatalf("ticks = %d after the workspace came back", h.standbyTicks("s1"))
	}
	h.world.close(wsA)
	h.step(30 * time.Second) // one missed listing only
	if len(g.calls) != 0 {
		t.Fatalf("purged on a single miss: %v", g.calls)
	}
}

func TestStandbyKeptWhenListingFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	g := standbyGone(h)
	h.world.add(winA, wsB, "b")
	standbyOn(h, "s1", wsA)
	h.step(0)
	h.world.close(wsA)
	h.world.listDown = errors.New("cmux: socket refused")
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	if len(g.calls) != 0 {
		t.Fatalf("purged on failed listings: %v", g.calls)
	}
	if sb, _ := h.st.GetStandby("s1"); sb == nil || sb.MissingTicks != 0 {
		t.Fatalf("row = %+v", sb)
	}
}

func TestStandbyWithoutWorkspaceNeverPurged(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	g := standbyGone(h)
	h.world.add(winA, wsB, "b")
	if err := h.st.SetStandby("s2", h.now); err != nil { // never registered a workspace
		t.Fatal(err)
	}
	h.step(0)
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	if len(g.calls) != 0 {
		t.Fatalf("purged without proof: %v", g.calls)
	}
}

func TestStandbyKeptForEndedSessionWithWorkspace(t *testing.T) { // D27
	t.Parallel()
	h := newHarness(t, on())
	g := standbyGone(h)
	standbyOn(h, "s1", wsA)
	h.step(0)
	if err := h.st.EndPeer("s1", h.now); err != nil {
		t.Fatal(err)
	}
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	if len(g.calls) != 0 {
		t.Fatalf("purged though the workspace is open: %v", g.calls)
	}
}

func TestStandbyPurgeSkippedWithoutSeam(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsB, "b")
	standbyOn(h, "s1", wsA)
	h.step(0)
	h.world.close(wsA)
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	if sb, _ := h.st.GetStandby("s1"); sb == nil {
		t.Fatal("no StandbyGone seam: nothing is purged")
	}
}

// TestSidebarSubjectShowsStatusNotPhase (D6, D28): GET /cmux/sidebar's
// subject carries its status, never the open phase.
func TestSidebarSubjectShowsStatusNotPhase(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	sub, err := h.st.GetSubject(subID)
	if err != nil {
		t.Fatal(err)
	}
	sub.Status = pigtail.StatusBuilding
	if err := h.st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	if err := h.st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_side", SubjectID: subID, Kind: pigtail.PhaseImplement, State: pigtail.PhaseActive, CreatedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.step(0)
	h.step(time.Second)
	var got *pigtail.CmuxSidebarSubject
	for _, w := range h.r.State().Workspaces {
		if w.CmuxWorkspaceID == wsA {
			got = w.Subject
		}
	}
	if got == nil {
		t.Fatalf("no subject on the bound workspace: %+v", h.r.State().Workspaces)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	_ = json.Unmarshal(b, &keys)
	if keys["status"] != "building" {
		t.Errorf("status = %v, want building: %s", keys["status"], b)
	}
	if _, ok := keys["phase"]; ok {
		t.Errorf("phase key present: %s", b)
	}
}
