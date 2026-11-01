package cmuxsync

import (
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// srvWS is the pigtail workspace (worktree) the servers below run in; the
// cmux workspace they show under is their owner.
const srvWS = "ws_srv"

func up(name string, port int, owner string) ServerObs {
	return ServerObs{WorkspaceID: srvWS, Server: name, Port: port, Owner: owner}
}

func died(at time.Time, port int, owner string, names ...string) ServerDeath {
	return ServerDeath{WorkspaceID: srvWS, Servers: names, Port: port, At: at, Owner: owner}
}

// The pigtail-srv pill of one workspace, from the servers it owns (spec
// §4.1, lot 3): one line, sorted by name, coloured by the worst state.
func TestComputeServerPill(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		servers []Server
		want    *cmux.StatusEntry
	}{
		{"no server: no pill", nil, nil},
		{"up", []Server{{WorkspaceID: wsA, Name: "dev", Port: 5173, State: ServerUp}},
			&cmux.StatusEntry{Key: KeyServers, Value: "dev :5173 up", Icon: "play.fill", Color: "#8E8E93", Priority: 60}},
		{"stopped", []Server{{WorkspaceID: wsA, Name: "dev", Port: 5173, State: ServerStopped}},
			&cmux.StatusEntry{Key: KeyServers, Value: "dev :5173 stopped", Icon: "stop.fill", Color: "#8E8E93", Priority: 60}},
		{"dead", []Server{{WorkspaceID: wsA, Name: "dev", Port: 5173, State: ServerDead}},
			&cmux.StatusEntry{Key: KeyServers, Value: "dev :5173 dead", Icon: "xmark.circle", Color: "#FF3B30", Priority: 60}},
		{"several on one line, sorted by name, the worst state colours it", []Server{
			{WorkspaceID: wsA, Name: "storybook", Port: 6006, State: ServerDead},
			{WorkspaceID: wsA, Name: "dev", Port: 5173, State: ServerUp},
		}, &cmux.StatusEntry{Key: KeyServers, Value: "dev :5173 up · sb :6006 dead", Icon: "xmark.circle", Color: "#FF3B30", Priority: 60}},
		{"up beats stopped for the colour", []Server{
			{WorkspaceID: wsA, Name: "web", Port: 3000, State: ServerStopped},
			{WorkspaceID: wsA, Name: "api", Port: 8080, State: ServerUp},
		}, &cmux.StatusEntry{Key: KeyServers, Value: "api :8080 up · web :3000 stopped", Icon: "play.fill", Color: "#8E8E93", Priority: 60}},
		{"A5 two servers up: grey", []Server{{WorkspaceID: wsA, Name: "api", Port: 8090, State: ServerUp}, {WorkspaceID: wsA, Name: "web", Port: 5174, State: ServerUp}},
			&cmux.StatusEntry{Key: KeyServers, Value: "api :8090 up · web :5174 up", Icon: "play.fill", Color: "#8E8E93", Priority: 60}},
		{"another workspace's server is not ours", []Server{{WorkspaceID: wsB, Name: "dev", Port: 5173, State: ServerUp}}, nil},
		{"a server whose workspace is closed lights nothing", []Server{{WorkspaceID: "CLOSED-0000-4000-8000-000000000009", Name: "dev", Port: 5173, State: ServerUp}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Compute(Input{Now: t0, Workspaces: []Workspace{live(wsA), live(wsB)}, Servers: c.servers})
			got, ok := res.Desired[wsA].Statuses()[KeyServers]
			switch {
			case c.want == nil && ok:
				t.Fatalf("unexpected pill %+v", got)
			case c.want != nil && (!ok || got != *c.want):
				t.Fatalf("pill %+v, want %+v", got, *c.want)
			}
		})
	}
}

// The server pill sits next to the attention pill, never instead of it.
func TestComputeServerPillWithAttention(t *testing.T) {
	t.Parallel()
	res := Compute(Input{
		Now:        t0,
		Workspaces: []Workspace{live(wsA)},
		Sessions:   []Session{sess("s1", wsA, "S1", "waiting", true)},
		Servers:    []Server{{WorkspaceID: wsA, Name: "dev", Port: 5173, State: ServerUp}},
	})
	st := res.Desired[wsA].Statuses()
	if st[KeyAttention].Value == "" || st[KeyServers].Value != "dev :5173 up" {
		t.Fatalf("statuses %+v", st)
	}
}

// The tracker turns what pigtaild sees (listening servers, recent deaths)
// into states: up, dead (a death after the last time it was seen up), stopped
// (seen up, gone, no death) for StoppedRetention, then nothing.
func TestServerTrackerStates(t *testing.T) {
	t.Parallel()
	type step struct {
		at   time.Duration
		snap ServerSnapshot
		want []Server
	}
	dev := func(st ServerState) []Server {
		return []Server{{WorkspaceID: wsA, Name: "dev", Port: 5173, State: st}}
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"up while it listens", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}, dev(ServerUp)},
		}},
		{"up then dead on a death", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}, dev(ServerUp)},
			{5 * time.Second, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(4*time.Second), 5173, wsA, "dev")}}, dev(ServerDead)},
			{29 * time.Minute, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(4*time.Second), 5173, wsA, "dev")}}, dev(ServerDead)},
		}},
		{"dead is forgotten past the death window, never turned into stopped", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}, dev(ServerUp)},
			{5 * time.Second, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(4*time.Second), 5173, wsA, "dev")}}, dev(ServerDead)},
			{31 * time.Minute, ServerSnapshot{}, nil},
			{32 * time.Minute, ServerSnapshot{}, nil},
		}},
		{"a death seen without ever seeing it up (daemon restarted) still reads dead", []step{
			{0, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(-time.Minute), 5173, wsA, "dev")}}, dev(ServerDead)},
		}},
		{"up then gone without a death: stopped for 5 minutes, then nothing", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{}, dev(ServerStopped)},
			{30*time.Second + 4*time.Minute, ServerSnapshot{}, dev(ServerStopped)},
			{30*time.Second + 5*time.Minute, ServerSnapshot{}, nil},
		}},
		{"gone, rammon not swept since: still up, then stopped once a sweep judged no death", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}, SweptAt: t0}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{SweptAt: t0.Add(20 * time.Second)}, dev(ServerUp)},
			{40 * time.Second, ServerSnapshot{SweptAt: t0.Add(35 * time.Second)}, dev(ServerStopped)},
			{40*time.Second + 4*time.Minute, ServerSnapshot{SweptAt: t0.Add(4 * time.Minute)}, dev(ServerStopped)},
			{40*time.Second + 5*time.Minute, ServerSnapshot{SweptAt: t0.Add(5 * time.Minute)}, nil},
		}},
		{"crash: never a false stopped, up until the death is judged", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}, SweptAt: t0}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{SweptAt: t0.Add(20 * time.Second)}, dev(ServerUp)},
			{36 * time.Second, ServerSnapshot{SweptAt: t0.Add(35 * time.Second),
				Deaths: []ServerDeath{died(t0.Add(35*time.Second), 5173, wsA, "dev")}}, dev(ServerDead)},
		}},
		{"rammon stalled: stopped anyway once the wait bound passes", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}, SweptAt: t0}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{SweptAt: t0}, dev(ServerUp)},
			{30*time.Second + SweepWaitMax, ServerSnapshot{SweptAt: t0}, dev(ServerStopped)},
		}},
		{"stopped then dead when the death is announced late", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{}, dev(ServerStopped)},
			{35 * time.Second, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(34*time.Second), 5173, wsA, "dev")}}, dev(ServerDead)},
		}},
		{"dead then up when it restarts", []step{
			{0, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(-time.Second), 5173, wsA, "dev")}}, dev(ServerDead)},
			{30 * time.Second, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}, Deaths: []ServerDeath{died(t0.Add(-time.Second), 5173, wsA, "dev")}}, dev(ServerUp)},
		}},
		{"restarted after a death, then stopped on purpose: stopped, not dead", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, wsA)}, Deaths: []ServerDeath{died(t0.Add(-time.Minute), 5173, wsA, "dev")}}, dev(ServerUp)},
			{30 * time.Second, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(-time.Minute), 5173, wsA, "dev")}}, dev(ServerStopped)},
		}},
		{"twins sharing a port are one process: one entry, names joined", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("web", 3000, wsA), up("dev", 3000, wsA)}},
				[]Server{{WorkspaceID: wsA, Name: "dev+web", Port: 3000, State: ServerUp}}},
			{5 * time.Second, ServerSnapshot{Deaths: []ServerDeath{died(t0.Add(4*time.Second), 3000, wsA, "dev", "web")}},
				[]Server{{WorkspaceID: wsA, Name: "dev+web", Port: 3000, State: ServerDead}}},
		}},
		// Seen live on 2026-10-01: the owner marker is per name, and the
		// twin `dev` of a large monorepo carried none while `web` named the workspace.
		{"twins whose owner only one name carries: shown under that owner", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 3000, ""), up("web", 3000, wsA)}},
				[]Server{{WorkspaceID: wsA, Name: "dev+web", Port: 3000, State: ServerUp}}},
		}},
		{"no owner: dropped; any other owner is kept, Compute shows it only on a live workspace", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, ""), up("api", 8080, "posix-1234")}},
				[]Server{{WorkspaceID: "POSIX-1234", Name: "api", Port: 8080, State: ServerUp}}},
		}},
		{"owner case is normalised to cmux's upper-case UUIDs", []step{
			{0, ServerSnapshot{Up: []ServerObs{up("dev", 5173, "aaaaaaaa-0000-4000-8000-000000000001")}}, dev(ServerUp)},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tr ServerTracker
			for i, s := range c.steps {
				got := tr.Observe(t0.Add(s.at), s.snap)
				if len(got) == 0 && len(s.want) == 0 {
					continue
				}
				if !reflect.DeepEqual(got, s.want) {
					t.Fatalf("step %d: %+v, want %+v", i, got, s.want)
				}
			}
		})
	}
}

func TestServerPillConflict(t *testing.T) {
	t.Parallel()
	var tr ServerTracker
	now := time.Now()
	got := tr.Observe(now, ServerSnapshot{
		Up:        []ServerObs{{WorkspaceID: "ws1", Server: "storybook", Port: 6006, Owner: wsA}},
		Conflicts: []ServerObs{{WorkspaceID: "ws1", Server: "dev", Port: 3013, Owner: wsA}},
	})
	e, ok := serversPill(got)
	if !ok || e.Value != "dev :3013 conflict · sb :6006 up" || e.Color != waittype.Orange.Color() {
		t.Fatalf("pill %+v", e)
	}
	if e.Key != KeyServers {
		t.Fatalf("key %q: only the owned servers key may be written", e.Key)
	}
	if again := tr.Observe(now.Add(time.Second), ServerSnapshot{Up: []ServerObs{{WorkspaceID: "ws1", Server: "storybook", Port: 6006, Owner: wsA}}}); len(again) != 1 {
		t.Fatalf("a resolved conflict must leave at once, got %+v", again)
	}
}
