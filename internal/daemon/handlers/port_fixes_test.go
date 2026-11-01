package handlers

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/ports"
)

// stubChecker answers ResyncBlockers from a table keyed by (self path, port).
type stubChecker struct {
	blockers map[serverAt][]ports.Blocker // ws field = self path
	err      error
}

func (s stubChecker) ResyncBlockers(_ string, port int, self string, _ int) ([]ports.Blocker, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.blockers[serverAt{self, port}], nil
}

func nolog(string, ...any) {}

const (
	wPath = "/wt/w"
	hPath = "/wt/h"
)

func envDiv(ws, path, kind string, port, reg int) rammon.PortConflict {
	return rammon.PortConflict{WorkspaceID: ws, Path: path, Server: kind, Kind: kind, Port: port,
		RegisteredPort: reg, Reason: rammon.ConflictEnvDiverges}
}

func foreign(ws, path, kind string, port, reg int, holder string) rammon.PortConflict {
	return rammon.PortConflict{WorkspaceID: ws, Path: path, Server: kind, Kind: kind, Port: port,
		RegisteredPort: reg, Reason: rammon.ConflictForeignListener, HolderWorkspaceID: holder}
}

func reg(id string) ports.Blocker {
	return ports.Blocker{Reason: ports.BlockRegistered, WorkspaceID: id}
}

// (a) W runs on 3013 (its .env.local), H registers 3013 and sees W's
// listener: reassign H first, then resync W — one command, on both entries.
func TestPortFixesOrdersReassignBeforeResync(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		foreign("ws_h", hPath, "dev", 3013, 3013, "ws_w"),
	}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 3013}: {reg("ws_h")}}}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "dev"}, chk, nolog)
	want := "pigtail ports reassign ws_h --kind dev && pigtail ports resync ws_w"
	if !reflect.DeepEqual(got, []string{want, want}) {
		t.Fatalf("fixes %q, want both %q", got, want)
	}
}

// env_local: H's .env.local (not its registry) names W's port, and H sees
// W's listener — same ordered repair; H's own env_diverges on that port joins it.
func TestPortFixesEnvLocalBlockerIsOrderedToo(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_h", hPath, "dev", 3013, 3050),
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		foreign("ws_h", hPath, "dev", 3013, 3050, "ws_w"),
	}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{
		{wPath, 3013}: {{Reason: ports.BlockEnvLocal, Dir: hPath + "/"}},
		{hPath, 3013}: {{Reason: ports.BlockEnvLocal, Dir: wPath}},
	}}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "dev"}, chk, nolog)
	want := "pigtail ports reassign ws_h --kind dev && pigtail ports resync ws_w"
	if !reflect.DeepEqual(got, []string{want, want, want}) {
		t.Fatalf("fixes %q", got)
	}
}

// (b) the port is registered by something W cannot move: a dead row, a
// static server, a live H running its own server (no conflict for H), or a
// live H whose listener there is not W's. W takes a fresh port — stopping
// its server first when it runs on that port.
func TestPortFixesReassignsWhenThePortCannotBeRecovered(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		extra    []rammon.PortConflict
		blockers []ports.Blocker
		running  map[serverAt]string
		want     string
	}{
		{"dead row, W running", nil, []ports.Blocker{reg("ws_dead")}, map[serverAt]string{{"ws_w", 3013}: "dev"},
			"(cd /wt/w && pigtail stop --server dev) && pigtail ports reassign ws_w --kind dev"},
		{"static server, W stopped", nil, []ports.Blocker{{Reason: ports.BlockRegistered, Static: true}}, nil,
			"pigtail ports reassign ws_w --kind dev"},
		{"H runs its own server", nil, []ports.Blocker{reg("ws_h")}, nil,
			"pigtail ports reassign ws_w --kind dev"},
		{"H held by a third worktree", []rammon.PortConflict{foreign("ws_h", hPath, "dev", 3013, 3013, "ws_x")},
			[]ports.Blocker{reg("ws_h")}, nil, "pigtail ports reassign ws_w --kind dev"},
		{"H held by an unknown process", []rammon.PortConflict{foreign("ws_h", hPath, "dev", 3013, 3013, "")},
			[]ports.Blocker{reg("ws_h")}, nil, "pigtail ports reassign ws_w --kind dev"},
		{"anonymous registrant", []rammon.PortConflict{foreign("ws_h", hPath, "dev", 3013, 3013, "ws_w")},
			[]ports.Blocker{{Reason: ports.BlockRegistered}}, map[serverAt]string{{"ws_w", 3013}: "web"},
			"(cd /wt/w && pigtail stop --server web) && pigtail ports reassign ws_w --kind dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := append([]rammon.PortConflict{envDiv("ws_w", wPath, "dev", 3013, 3000)}, tc.extra...)
			chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 3013}: tc.blockers}}
			got := portFixes(cs, tc.running, chk, nolog)
			if got[0] != tc.want {
				t.Fatalf("W's fix %q, want %q", got[0], tc.want)
			}
		})
	}
}

// Review focus 1: H plus a dead row both register the port — moving H alone
// would leave the resync refused, so W takes a fresh port.
func TestPortFixesTwoRegistrantsFallBackToReassign(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		foreign("ws_h", hPath, "dev", 3013, 3013, "ws_w"),
	}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 3013}: {reg("ws_dead"), reg("ws_h")}}}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "dev"}, chk, nolog)
	if got[0] != "(cd /wt/w && pigtail stop --server dev) && pigtail ports reassign ws_w --kind dev" ||
		got[1] != "pigtail ports reassign ws_h --kind dev" {
		t.Fatalf("fixes %q", got)
	}
}

// (c) W's .env.local port is someone else's listener: one fix for both of
// W's entries — reassign W. Never a resync (HeldElsewhere refuses it).
func TestPortFixesEnvAndForeignOnOnePortIsOneReassign(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		foreign("ws_w", wPath, "dev", 3013, 3000, "ws_h"),
	}
	got := portFixes(cs, nil, stubChecker{}, nolog)
	want := "pigtail ports reassign ws_w --kind dev"
	if !reflect.DeepEqual(got, []string{want, want}) {
		t.Fatalf("fixes %q", got)
	}
}

// Nothing blocks, or only an allocation in flight (a race): resync.
func TestPortFixesResyncWhenNothingBlocks(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{envDiv("ws_w", wPath, "dev", 3013, 3000)}
	for _, bs := range [][]ports.Blocker{nil, {{Reason: ports.BlockPending}}} {
		chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 3013}: bs}}
		if got := portFixes(cs, nil, chk, nolog); got[0] != "pigtail ports resync ws_w" {
			t.Fatalf("blockers %v → %q", bs, got[0])
		}
	}
}

// Review focus 4: resync covers every kind at once, so blocked kinds are
// reassigned first; kinds and stops come out sorted whatever the input order.
func TestPortFixesMultiKindOrdersReassignBeforeResync(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_w", wPath, "storybook", 6013, 6000),
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		envDiv("ws_w", wPath, "api", 4013, 4000),
	}
	static := []ports.Blocker{{Reason: ports.BlockRegistered, Static: true}}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 6013}: static, {wPath, 4013}: static}}
	running := map[serverAt]string{{"ws_w", 6013}: "storybook", {"ws_w", 4013}: "api"}
	want := "(cd /wt/w && pigtail stop --server api && pigtail stop --server storybook) && " +
		"pigtail ports reassign ws_w --kind api --kind storybook && pigtail ports resync ws_w"
	got := portFixes(cs, running, chk, nolog)
	if !reflect.DeepEqual(got, []string{want, want, want}) {
		t.Fatalf("fixes %q", got)
	}
}

// Review focus 3: H's conflict on W's port joins W's repair; H's conflict on
// another kind keeps H's own, without the kind W's repair already moves.
func TestPortFixesAbsorbedWorkspaceKeepsItsOtherConflict(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		foreign("ws_h", hPath, "dev", 3013, 3013, "ws_w"),
		foreign("ws_h", hPath, "storybook", 6006, 6006, "ws_x"),
	}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 3013}: {reg("ws_h")}}}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "dev"}, chk, nolog)
	ordered := "pigtail ports reassign ws_h --kind dev && pigtail ports resync ws_w"
	if !reflect.DeepEqual(got, []string{ordered, ordered, "pigtail ports reassign ws_h --kind storybook"}) {
		t.Fatalf("fixes %q", got)
	}
}

// Review focus 5: the registry cannot be read — never a guessed resync.
func TestPortFixesCheckerErrorProposesReassign(t *testing.T) {
	t.Parallel()
	var logged int
	cs := []rammon.PortConflict{envDiv("ws_w", wPath, "dev", 3013, 3000)}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "dev"}, stubChecker{err: errors.New("db locked")},
		func(string, ...any) { logged++ })
	if got[0] != "(cd /wt/w && pigtail stop --server dev) && pigtail ports reassign ws_w --kind dev" || logged != 1 {
		t.Fatalf("fix %q, logged %d", got[0], logged)
	}
}

// No allocator wired (handler tests only — the daemon always wires one):
// nothing to ask, the divergence alone decides.
func TestPortFixesNilCheckerResyncs(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{envDiv("ws_w", wPath, "dev", 3013, 3000)}
	if got := portFixes(cs, nil, nil, nolog); got[0] != "pigtail ports resync ws_w" {
		t.Fatalf("fix %q", got[0])
	}
}

// Review focus 6: H registers 3013 under dev (W's kind), but H's listener
// entry there is on storybook — reassigning H's storybook would leave
// dev:3013 registered, so W gives the port up and H keeps its own fix.
func TestPortFixesBlockerOfAnotherKindFallsBackToReassign(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{
		envDiv("ws_w", wPath, "dev", 3013, 3000),
		foreign("ws_h", hPath, "storybook", 3013, 3013, "ws_w"),
	}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{wPath, 3013}: {reg("ws_h")}}}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "dev"}, chk, nolog)
	want := []string{
		"(cd /wt/w && pigtail stop --server dev) && pigtail ports reassign ws_w --kind dev",
		"pigtail ports reassign ws_h --kind storybook",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixes %q, want %q", got, want)
	}
}

// resyncTable asks the allocator once for every question of the request and
// answers the planner from that table; a question it was not asked is an
// error — the planner then gives the port up, never guesses a resync.
func TestResyncTableAnswersFromOneBatch(t *testing.T) {
	t.Parallel()
	a := ports.New(regStore{ids: map[int][]string{3013: {"ws_h"}}}, ports.WithBindProbe(func(int) bool { return true }))
	cs := []rammon.PortConflict{envDiv("ws_w", wPath, "dev", 3013, 3000), foreign("ws_h", hPath, "dev", 3013, 3013, "ws_w")}
	chk := resyncTable(a, cs, nolog)
	if got, err := chk.ResyncBlockers("dev", 3013, wPath, 3000); err != nil || !reflect.DeepEqual(got, []ports.Blocker{reg("ws_h")}) {
		t.Fatalf("dev:3013 → %+v %v", got, err)
	}
	if _, err := chk.ResyncBlockers("dev", 3099, wPath, 3000); err == nil {
		t.Fatal("a question the table was not asked must be an error")
	}
	if resyncTable(nil, cs, nolog) != nil {
		t.Fatal("no allocator: nil checker (portFixes then assumes an unblocked resync, as before)")
	}
}

// Review focus 2: every word put in a command — path, server, kind, id —
// stays one shell word.
func TestQuoteWord(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/wt/feat-x_1":    "/wt/feat-x_1",
		"ws_efb2e9":       "ws_efb2e9",
		"storybook":       "storybook",
		"/wt/my feature":  "'/wt/my feature'",
		"/wt/it's":        `'/wt/it'\''s'`,
		"/wt/$(rm -rf ~)": "'/wt/$(rm -rf ~)'",
		"web app":         "'web app'",
		"dev;rm":          "'dev;rm'",
		"":                "''",
	} {
		if got := quoteWord(in); got != want {
			t.Errorf("quoteWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// The words of a repair go through quoteWord: a server name or a path with
// a space stays one word inside the stop sub-shell.
func TestPortFixesQuotesEveryWord(t *testing.T) {
	t.Parallel()
	cs := []rammon.PortConflict{envDiv("ws_w", "/wt/my w", "dev", 3013, 3000)}
	chk := stubChecker{blockers: map[serverAt][]ports.Blocker{{"/wt/my w", 3013}: {reg("ws_dead")}}}
	got := portFixes(cs, map[serverAt]string{{"ws_w", 3013}: "web app"}, chk, nolog)
	want := "(cd '/wt/my w' && pigtail stop --server 'web app') && pigtail ports reassign ws_w --kind dev"
	if got[0] != want {
		t.Fatalf("fix %q, want %q", got[0], want)
	}
}
