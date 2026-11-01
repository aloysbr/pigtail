package devserver

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// world is a fake process table: listeners per port, cwds, parents, liveness.
// Its Kill removes every non-immortal pid, then applies respawn (port → new
// pid). No real process is ever touched; pids are >= 9000001.
type world struct {
	listeners map[int][]int
	cwds      map[int]string
	parents   map[int]int
	alive     map[int]bool
	immortal  map[int]bool
	respawn   map[int]int

	cwdErr     error
	portErr    error
	parentsErr error
	kills      [][]int
}

func (w *world) deps() StopDeps {
	return StopDeps{
		PortBusy: func(port int) (bool, []int, error) {
			if w.portErr != nil {
				return false, nil, w.portErr
			}
			p := w.listeners[port]
			return len(p) > 0, append([]int(nil), p...), nil
		},
		Cwds: func(pids []int) (map[int]string, error) {
			if w.cwdErr != nil {
				return nil, w.cwdErr
			}
			m := map[int]string{}
			for _, p := range pids {
				if c, ok := w.cwds[p]; ok {
					m[p] = c
				}
			}
			return m, nil
		},
		Parents: func() (map[int]int, error) {
			if w.parentsErr != nil {
				return nil, w.parentsErr
			}
			return w.parents, nil
		},
		Kill: func(pids []int) {
			w.kills = append(w.kills, append([]int(nil), pids...))
			for _, p := range pids {
				if w.immortal[p] {
					continue
				}
				delete(w.alive, p)
				for port, ls := range w.listeners {
					var keep []int
					for _, l := range ls {
						if l != p {
							keep = append(keep, l)
						}
					}
					w.listeners[port] = keep
				}
			}
			for port, np := range w.respawn {
				w.listeners[port] = append(w.listeners[port], np)
				w.alive[np] = true
			}
		},
		Alive: func(pid int) bool { return w.alive[pid] },
		Sleep: func(time.Duration) {},
		Grace: time.Millisecond,
	}
}

func newWorld(wt string) *world {
	return &world{
		listeners: map[int][]int{3044: {9000001}},
		cwds:      map[int]string{9000001: wt},
		parents:   map[int]int{},
		alive:     map[int]bool{9000001: true},
		immortal:  map[int]bool{},
		respawn:   map[int]int{},
	}
}

type stopCase struct {
	name   string
	wt     string
	ports  map[string]int
	setup  func(w *world)
	kills  [][]int
	stops  []pigtail.StoppedServer
	reason string // substring of the first failure's reason; "" = success
	pids   []int  // want failure pids
}

func TestStopWorktreeServers(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "feat-y")
	sibling := filepath.Join(root, "feat-x")
	if err := mkdirs(wt, sibling); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(root, "gone-wt") // never created

	cases := []stopCase{
		{name: "nothing listening", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) { w.listeners = map[int][]int{} }},
		{name: "owned listener with two children", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) {
				w.parents = map[int]int{9000002: 9000001, 9000003: 9000001}
				w.alive[9000002], w.alive[9000003] = true, true
			},
			kills: [][]int{{9000001, 9000002, 9000003}},
			stops: []pigtail.StoppedServer{{Kinds: []string{"dev"}, Port: 3044, PIDs: []int{9000001, 9000002, 9000003}}}},
		{name: "two kinds share a port", wt: wt, ports: map[string]int{"dev": 3044, "web": 3044},
			kills: [][]int{{9000001}},
			stops: []pigtail.StoppedServer{{Kinds: []string{"dev", "web"}, Port: 3044, PIDs: []int{9000001}}}},
		{name: "foreign listener", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) { w.cwds[9000001] = sibling }},
		{name: "unknown owner refuses before any kill", wt: wt, ports: map[string]int{"dev": 3044, "sb": 6006},
			setup: func(w *world) {
				w.listeners[6006] = []int{9000005}
				w.alive[9000005] = true
				w.cwds[9000005] = wt
				// 9000005's cwd cannot be read: the probe fails for any batch containing it.
				w.cwdErr = nil
			},
			reason: "owner unknown"},
		{name: "port probe failed", wt: wt, ports: map[string]int{"dev": 3044},
			setup:  func(w *world) { w.portErr = errors.New("lsof boom") },
			reason: "port probe failed"},
		{name: "immortal listener", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) { w.immortal[9000001] = true },
			kills: [][]int{{9000001}}, reason: "still listening after SIGKILL", pids: []int{9000001}},
		{name: "supervisor respawn", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) { w.respawn[3044] = 9000009; w.cwds[9000009] = wt },
			kills: [][]int{{9000001}}, reason: "re-bound by pid 9000009"},
		{name: "child survives, port free", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) {
				w.parents = map[int]int{9000002: 9000001}
				w.alive[9000002] = true
				w.immortal[9000002] = true
			},
			kills: [][]int{{9000001, 9000002}}, reason: "process still alive after SIGKILL", pids: []int{9000002}},
		{name: "process table unreadable", wt: wt, ports: map[string]int{"dev": 3044},
			setup: func(w *world) { w.parentsErr = errors.New("ps boom") },
			kills: [][]int{{9000001}},
			stops: []pigtail.StoppedServer{{Kinds: []string{"dev"}, Port: 3044, PIDs: []int{9000001}}}},
		{name: "worktree gone", wt: gone, ports: map[string]int{"dev": 3044},
			setup: func(w *world) { w.cwds[9000001] = gone + "/node_modules/.vite" },
			kills: [][]int{{9000001}},
			stops: []pigtail.StoppedServer{{Kinds: []string{"dev"}, Port: 3044, PIDs: []int{9000001}}}},
		{name: "listener without cwd", wt: wt, ports: map[string]int{"dev": 3044},
			setup:  func(w *world) { w.cwds = map[int]string{9000001: ""} },
			reason: "owner unknown"},
		{name: "listener with an unmatched deleted cwd once the worktree is gone", wt: gone, ports: map[string]int{"dev": 3044},
			setup:  func(w *world) { w.cwds[9000001] = root + "/elsewhere/x (deleted)" },
			reason: "owner unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(c.wt)
			if c.setup != nil {
				c.setup(w)
			}
			if c.name == "unknown owner refuses before any kill" {
				d := w.deps()
				base := d.Cwds
				d.Cwds = func(pids []int) (map[int]string, error) {
					for _, p := range pids {
						if p == 9000005 {
							return nil, errors.New("lsof boom")
						}
					}
					return base(pids)
				}
				run(t, c, w, d)
				return
			}
			run(t, c, w, w.deps())
		})
	}
}

func run(t *testing.T, c stopCase, w *world, d StopDeps) {
	t.Helper()
	stops, err := StopWorktreeServers(c.wt, c.ports, d)
	if !reflect.DeepEqual(w.kills, c.kills) && (len(w.kills) != 0 || len(c.kills) != 0) {
		t.Fatalf("kills = %v, want %v", w.kills, c.kills)
	}
	if c.reason == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if (len(stops) != 0 || len(c.stops) != 0) && !reflect.DeepEqual(stops, c.stops) {
			t.Fatalf("stops = %+v, want %+v", stops, c.stops)
		}
		return
	}
	var sre *ServersRunningError
	if !errors.As(err, &sre) {
		t.Fatalf("err = %v, want *ServersRunningError", err)
	}
	if len(sre.Failures) == 0 || !strings.Contains(sre.Failures[0].Reason, c.reason) {
		t.Fatalf("failures = %+v, want reason containing %q", sre.Failures, c.reason)
	}
	if c.pids != nil {
		got := append([]int(nil), sre.Failures[0].PIDs...)
		sort.Ints(got)
		if !reflect.DeepEqual(got, c.pids) {
			t.Fatalf("failure pids = %v, want %v", got, c.pids)
		}
	}
}

func mkdirs(dirs ...string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func TestServersRunningErrorMessage(t *testing.T) {
	e := &ServersRunningError{Failures: []ServerStopFailure{
		{Kinds: []string{"dev"}, Port: 3044, PIDs: []int{9000001}, Reason: "still listening after SIGKILL"},
	}}
	want := "dev server still running — dev :3044 (pid 9000001): still listening after SIGKILL"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	e.Failures = append(e.Failures, ServerStopFailure{Kinds: []string{"sb", "web"}, Port: 6006, PIDs: []int{9000002, 9000003}, Reason: "x"})
	want += "; sb+web :6006 (pid 9000002,9000003): x"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// A deadline never skips the check: even with a nanosecond of grace the
// verification round runs once.
func TestStopWorktreeServersChecksOnceBeforeTheDeadline(t *testing.T) {
	wt := t.TempDir()
	w := newWorld(wt)
	w.immortal[9000001] = true
	d := w.deps()
	d.Grace = time.Nanosecond
	_, err := StopWorktreeServers(wt, map[string]int{"dev": 3044}, d)
	var sre *ServersRunningError
	if !errors.As(err, &sre) || !strings.Contains(sre.Failures[0].Reason, "still listening after SIGKILL") {
		t.Fatalf("err = %v, want still listening", err)
	}
}

// The grace period starts after Kill returns: a slow Kill (SIGTERM wait)
// must not eat the verification window. The port is released only after the
// first Sleep, so a deadline counted from before the Kill would refuse.
func TestStopWorktreeServersGraceStartsAfterKill(t *testing.T) {
	wt := t.TempDir()
	w := newWorld(wt)
	d := w.deps()
	now := time.Unix(0, 0)
	d.Now = func() time.Time { return now }
	d.Kill = func(pids []int) {
		now = now.Add(10 * time.Second) // Kill itself outlasts the grace
		w.kills = append(w.kills, pids)
	}
	d.Sleep = func(dur time.Duration) {
		now = now.Add(dur)
		delete(w.listeners, 3044)
		delete(w.alive, 9000001)
	}
	d.Grace = time.Second
	stops, err := StopWorktreeServers(wt, map[string]int{"dev": 3044}, d)
	if err != nil || len(stops) != 1 {
		t.Fatalf("stops=%v err=%v, want one stop", stops, err)
	}
}

func TestIsZombieStat(t *testing.T) {
	for stat, want := range map[string]bool{"Z": true, "Z+": true, "S": false, "R+": false, "Ss": false, "": false} {
		if got := isZombieStat(stat); got != want {
			t.Errorf("isZombieStat(%q) = %v, want %v", stat, got, want)
		}
	}
	if got := parseStat([]byte("  Z+ \n")); got != "Z+" {
		t.Errorf("parseStat = %q", got)
	}
}
