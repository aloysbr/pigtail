package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestPrintServersShowsRecentDeaths(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	resp := pigtail.ServersResponse{
		Servers: []pigtail.ServerView{{WorkspaceID: "ws1", Server: "dev", Port: 3005}},
		RecentDeaths: []pigtail.DeadServerView{
			{WorkspaceID: "ws2", Server: "dev", Port: 3016, AgoSeconds: 240},
		},
	}
	printServers(&buf, resp, nil)
	out := buf.String()
	if !strings.Contains(out, "ws2:dev") || !strings.Contains(out, "4m") {
		t.Fatalf("a recent death must be named with how long ago it happened, got:\n%s", out)
	}
	// The rest of this CLI is English-only; the death line must match, not
	// carry over the French wording from the plan it was drafted from.
	if !strings.Contains(out, "died") || !strings.Contains(out, "ago") {
		t.Fatalf("death line must be in English (died ... ago), got:\n%s", out)
	}
}

func TestPrintServersSaysNothingWithoutDeaths(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printServers(&buf, pigtail.ServersResponse{
		Servers: []pigtail.ServerView{{WorkspaceID: "ws1", Server: "dev", Port: 3005}},
	}, nil)
	if strings.Contains(buf.String(), "✝") {
		t.Fatal("no deaths must mean no death section at all")
	}
}

// TestShortOwner: a derived identity carries a host that is noise in a table
// where every line shares it. It is only worth printing when it differs.
func TestShortOwner(t *testing.T) {
	t.Parallel()
	cases := []struct{ owner, myHost, want string }{
		{"sid:4711@laptop", "laptop", "sid:4711"},
		{"sid:4711@desktop", "laptop", "sid:4711@desktop"},
		{"ws_e9fbd5", "laptop", "ws_e9fbd5"},
		{"", "laptop", "—"},
	}
	for _, tc := range cases {
		if got := shortOwner(tc.owner, tc.myHost); got != tc.want {
			t.Fatalf("shortOwner(%q, %q) = %q, want %q", tc.owner, tc.myHost, got, tc.want)
		}
	}
}

// TestPrintServersMarksDeadOwner: a dead owner is the reason --all may
// reclaim a server, so the table must show it rather than let the user
// discover it when the server disappears.
func TestPrintServersMarksDeadOwner(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printServers(&buf, pigtail.ServersResponse{Servers: []pigtail.ServerView{
		{WorkspaceID: "ws1", Server: "dev", Port: 3030, Owner: "sid:9999@laptop", OwnerAlive: false},
	}}, nil)
	if !strings.Contains(buf.String(), "†") {
		t.Fatalf("a dead owner must be marked, got:\n%s", buf.String())
	}
}

// TestPrintServersNamesEveryServerOfADeath: a death on a shared port names
// the process once, under all its names.
func TestPrintServersNamesEveryServerOfADeath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		death pigtail.DeadServerView
		want  string
	}{
		{
			name:  "shared port",
			death: pigtail.DeadServerView{WorkspaceID: "ws1", Server: "dev", Servers: []string{"dev", "web"}, Port: 3030, AgoSeconds: 60},
			want:  "✝ ws1:dev+web (:3030) died 1m ago",
		},
		{
			name:  "older daemon without servers",
			death: pigtail.DeadServerView{WorkspaceID: "ws1", Server: "dev", Port: 3030, AgoSeconds: 60},
			want:  "✝ ws1:dev (:3030) died 1m ago",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printServers(&buf, pigtail.ServersResponse{RecentDeaths: []pigtail.DeadServerView{tc.death}}, nil)
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("want line %q, got:\n%s", tc.want, buf.String())
			}
		})
	}
}

// TestPrintServersLabelsMemoryNotRSS: the column carries the physical
// footprint (top's MEM), so it must not be called RSS.
func TestPrintServersLabelsMemoryNotRSS(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printServers(&buf, pigtail.ServersResponse{Servers: []pigtail.ServerView{
		{WorkspaceID: "ws1", Server: "storybook", Port: 6006, RSSBytes: 2345 << 20},
	}}, nil)
	out := buf.String()
	if !strings.Contains(out, "MEM(MiB)") || strings.Contains(out, "RSS") {
		t.Fatalf("want a MEM(MiB) column and no RSS label, got:\n%s", out)
	}
	if !strings.Contains(out, "2345") {
		t.Fatalf("value not printed in MiB:\n%s", out)
	}
}

// TestPrintServersShowsPinColumn: every pin state reads at a glance.
func TestPrintServersShowsPinColumn(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		sv   pigtail.ServerView
		want string
	}{
		{"unpinned", pigtail.ServerView{}, "—"},
		{"durable", pigtail.ServerView{PinnedDurable: true}, "until unpinned"},
		{"timed", pigtail.ServerView{PinnedUnixSeconds: now.Add(65 * time.Minute).Unix()}, "1h05m"},
		{"timed, already past", pigtail.ServerView{PinnedUnixSeconds: now.Add(-time.Minute).Unix()}, "—"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinColumn(tc.sv, now); got != tc.want {
				t.Fatalf("pinColumn = %q, want %q", got, tc.want)
			}
		})
	}

	prev := serversNow
	serversNow = func() time.Time { return now }
	t.Cleanup(func() { serversNow = prev })
	var buf bytes.Buffer
	printServers(&buf, pigtail.ServersResponse{Servers: []pigtail.ServerView{
		{WorkspaceID: "ws1", Server: "storybook", Port: 6006, PinnedDurable: true},
	}}, nil)
	out := buf.String()
	if !strings.Contains(out, "PIN") || !strings.Contains(out, "until unpinned") {
		t.Fatalf("want a PIN column showing until unpinned, got:\n%s", out)
	}
}

func TestPrintServersShowsPortConflicts(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printServers(&b, pigtail.ServersResponse{PortConflicts: []pigtail.PortConflictView{{
		WorkspaceID: "ws_efb2e9", Server: "dev", Kind: "dev", Port: 3013, RegisteredPort: 3000,
		Reason: "env_diverges", Fix: "pigtail ports resync ws_efb2e9",
	}}}, nil)
	got := b.String()
	for _, want := range []string{"port conflicts", "ws_efb2e9", ".env.local :3013 ≠ registry :3000", "pigtail ports resync ws_efb2e9"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

// The fix command never depends on how long a holder path is: it sits on
// its own line under each conflict, at the same indent for every row.
func TestPrintServersPortConflictFixOnItsOwnLine(t *testing.T) {
	t.Parallel()
	long := "/Users/someone/Developer/customers/acme/bigapp-wt/a-very-long-worktree-name-for-a-feature"
	var b bytes.Buffer
	printServers(&b, pigtail.ServersResponse{PortConflicts: []pigtail.PortConflictView{
		{WorkspaceID: "ws_392f9b", Server: "dev", Kind: "dev", Port: 3013, Reason: "foreign_listener",
			HolderPath: long, Fix: "pigtail ports reassign ws_392f9b --kind dev"},
		{WorkspaceID: "ws_efb2e9", Server: "dev", Kind: "dev", Port: 3013, RegisteredPort: 3000,
			Reason: "env_diverges", Fix: "pigtail ports resync ws_efb2e9"},
	}}, nil)
	out := b.String()
	lines := strings.Split(out[strings.Index(out, "port conflicts:"):], "\n")
	want := []string{
		"port conflicts:",
		"  ws_392f9b  dev  :3013 held by " + long,
		"    → pigtail ports reassign ws_392f9b --kind dev",
		"  ws_efb2e9  dev  .env.local :3013 ≠ registry :3000",
		"    → pigtail ports resync ws_efb2e9",
	}
	if len(lines) < len(want) {
		t.Fatalf("output:\n%s", b.String())
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

func TestPrintServersMarksConflictedRows(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printServers(&b, pigtail.ServersResponse{
		Servers: []pigtail.ServerView{
			{WorkspaceID: "ws_efb2e9", Server: "dev", Port: 3013},
			{WorkspaceID: "ws_other", Server: "dev", Port: 3040},
		},
		PortConflicts: []pigtail.PortConflictView{{WorkspaceID: "ws_efb2e9", Server: "dev", Kind: "dev",
			Port: 3013, RegisteredPort: 3000, Reason: "env_diverges", Fix: "pigtail ports resync ws_efb2e9"}},
	}, nil)
	var marked, clean bool
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, "ws_efb2e9:dev") && strings.Contains(line, "⚠port") {
			marked = true
		}
		if strings.HasPrefix(line, "ws_other:dev") && !strings.Contains(line, "⚠port") {
			clean = true
		}
	}
	if !marked || !clean {
		t.Fatalf("want ⚠port on ws_efb2e9:dev only:\n%s", b.String())
	}
}

func TestPrintWorkspacesMarksPortConflict(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printWorkspaces(&b, []pigtail.Workspace{{ID: "ws_efb2e9", State: pigtail.StateBuilding, LastActivityAt: time.Now()}},
		[]pigtail.PortConflictView{{WorkspaceID: "ws_efb2e9", Fix: "pigtail ports resync ws_efb2e9"}})
	if !strings.Contains(b.String(), "⚠port") {
		t.Fatalf("got %q, want the ⚠port marker", b.String())
	}
	if strings.Contains(b.String(), "pigtail ports") {
		t.Fatalf("pigtail list must carry the marker only, no fix block: %q", b.String())
	}
}

// TestPrintWorkspacesMarksHooks (criterion 2, list side; criterion 3).
func TestPrintWorkspacesMarksHooks(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	now := time.Now()
	printWorkspaces(&b, []pigtail.Workspace{
		{ID: "ws_fail", State: pigtail.StateBuilding, LastActivityAt: now,
			Hooks: &pigtail.HooksResult{Status: pigtail.HooksFailed, Hook: "husky", Log: "/l/hooks.log"}},
		{ID: "ws_run", State: pigtail.StateBuilding, LastActivityAt: now, Hooks: &pigtail.HooksResult{Status: pigtail.HooksRunning}},
		{ID: "ws_ok", State: pigtail.StateBuilding, LastActivityAt: now, Hooks: &pigtail.HooksResult{Status: pigtail.HooksOK}},
		{ID: "ws_old", State: pigtail.StateBuilding, LastActivityAt: now}, // before migration 0023
	}, nil)
	lines := strings.Split(b.String(), "\n")
	line := func(id string) string {
		for _, l := range lines {
			if strings.Contains(l, id) {
				return l
			}
		}
		t.Fatalf("no line for %s in %q", id, b.String())
		return ""
	}
	if !strings.Contains(line("ws_fail"), "⚠hooks:husky") {
		t.Errorf("failed hook not marked: %q", line("ws_fail"))
	}
	if !strings.Contains(line("ws_run"), "…hooks") {
		t.Errorf("running hooks not marked: %q", line("ws_run"))
	}
	for _, id := range []string{"ws_ok", "ws_old"} {
		if strings.Contains(line(id), "hooks") {
			t.Errorf("%s marked although nothing failed: %q", id, line(id))
		}
	}
}
