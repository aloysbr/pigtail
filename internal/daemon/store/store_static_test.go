package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestStaticServerMigrationCreatesTable(t *testing.T) {
	s := newTestStore(t)
	var got string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, "static_servers",
	).Scan(&got)
	if err != nil {
		t.Fatalf("table static_servers not found: %v", err)
	}
}

func TestStaticServerCreateGet(t *testing.T) {
	s := newTestStore(t)

	ss := &pigtail.StaticServer{
		ID:        "ss_abc",
		Name:      "docs",
		Dir:       "/tmp/site/public",
		Port:      4100,
		CreatedAt: time.UnixMilli(2000).UTC(),
	}
	if err := s.CreateStaticServer(ss); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetStaticServerByName("docs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "ss_abc" || got.Dir != "/tmp/site/public" || got.Port != 4100 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Command != "" {
		t.Fatalf("command should default to empty, got %q", got.Command)
	}
	if !got.CreatedAt.Equal(time.UnixMilli(2000).UTC()) {
		t.Fatalf("created_at mismatch: %v", got.CreatedAt)
	}

	if _, err := s.GetStaticServerByName("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStaticServerNameUnique(t *testing.T) {
	s := newTestStore(t)
	a := &pigtail.StaticServer{ID: "ss_1", Name: "docs", Dir: "/tmp/a", Port: 4100, CreatedAt: time.UnixMilli(1).UTC()}
	b := &pigtail.StaticServer{ID: "ss_2", Name: "docs", Dir: "/tmp/b", Port: 4101, CreatedAt: time.UnixMilli(2).UTC()}
	if err := s.CreateStaticServer(a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := s.CreateStaticServer(b); err == nil {
		t.Fatalf("expected unique-name violation on second insert, got nil")
	}
}

func TestStaticServerCommandRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ss := &pigtail.StaticServer{
		ID: "ss_cmd", Name: "custom", Dir: "/tmp/c", Port: 4200,
		Command: "python -m http.server", CreatedAt: time.UnixMilli(3).UTC(),
	}
	if err := s.CreateStaticServer(ss); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetStaticServerByName("custom")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Command != "python -m http.server" {
		t.Fatalf("command mismatch: %q", got.Command)
	}
}

func TestStaticServerListAndDelete(t *testing.T) {
	s := newTestStore(t)
	for i, ss := range []*pigtail.StaticServer{
		{ID: "ss_1", Name: "a", Dir: "/tmp/a", Port: 4100, CreatedAt: time.UnixMilli(10).UTC()},
		{ID: "ss_2", Name: "b", Dir: "/tmp/b", Port: 4101, CreatedAt: time.UnixMilli(20).UTC()},
	} {
		if err := s.CreateStaticServer(ss); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	list, err := s.ListStaticServers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("list order/content mismatch: %+v", list)
	}

	if err := s.DeleteStaticServer("ss_1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = s.ListStaticServers()
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 1 || list[0].Name != "b" {
		t.Fatalf("delete did not remove the right row: %+v", list)
	}

	if err := s.DeleteStaticServer("ss_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: want ErrNotFound, got %v", err)
	}
}

// TestUsedPortsIncludesStaticServers proves a static server's port shows up as
// "used" so the allocator won't double-allocate it — for every kind, since a
// static server binds one concrete TCP port not tied to any workspace kind.
func TestUsedPortsIncludesStaticServers(t *testing.T) {
	s := newTestStore(t)

	// A workspace occupies a "dev" port, a static server occupies another.
	proj := &pigtail.Project{
		ID: "prj_p", Name: "p", RootPath: "/tmp/p", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	ws := &pigtail.Workspace{
		ID: "ws_p", ProjectID: proj.ID, Intent: "x", State: pigtail.StateBuilding,
		Path: "/tmp/p-feat", Ports: map[string]int{"dev": 3001}, BackendKind: "git",
	}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	ss := &pigtail.StaticServer{ID: "ss_p", Name: "docs", Dir: "/tmp/d", Port: 4242, CreatedAt: time.UnixMilli(1).UTC()}
	if err := s.CreateStaticServer(ss); err != nil {
		t.Fatalf("create static server: %v", err)
	}

	dev, err := s.UsedPorts("dev")
	if err != nil {
		t.Fatalf("used ports dev: %v", err)
	}
	if !dev[3001] {
		t.Fatalf("workspace dev port 3001 missing from used set: %v", dev)
	}
	if !dev[4242] {
		t.Fatalf("static server port 4242 missing from used(dev): %v", dev)
	}

	// The static server port is also "used" when allocating an unrelated kind,
	// since it is a concrete TCP port independent of workspace kinds.
	other, err := s.UsedPorts("storybook")
	if err != nil {
		t.Fatalf("used ports storybook: %v", err)
	}
	if !other[4242] {
		t.Fatalf("static server port 4242 missing from used(storybook): %v", other)
	}
	if other[3001] {
		t.Fatalf("workspace dev port leaked into storybook kind: %v", other)
	}
}

// PortRegistrants names every row registering (kind, port) — live or not —
// and whether a static server holds the number (any kind), mirroring UsedPorts.
func TestPortRegistrantsNamesRowsAndStaticServers(t *testing.T) {
	s := newTestStore(t)
	proj := &pigtail.Project{ID: "prj_p", Name: "p", RootPath: "/tmp/p", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}}}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []*pigtail.Workspace{
		{ID: "ws_b", ProjectID: proj.ID, State: pigtail.StateBuilding, Path: "/gone/b", Ports: map[string]int{"dev": 3013}, BackendKind: "git"},
		{ID: "ws_a", ProjectID: proj.ID, State: pigtail.StateBuilding, Path: "/gone/a", Ports: map[string]int{"dev": 3013}, BackendKind: "git"},
		{ID: "ws_c", ProjectID: proj.ID, State: pigtail.StateBuilding, Path: "/gone/c", Ports: map[string]int{"storybook": 3013}, BackendKind: "git"},
	} {
		if err := s.CreateWorkspace(ws); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateStaticServer(&pigtail.StaticServer{ID: "ss_p", Name: "docs", Dir: "/tmp/d", Port: 4242, CreatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	ids, static, err := s.PortRegistrants("dev", 3013)
	if err != nil || static || !reflect.DeepEqual(ids, []string{"ws_a", "ws_b"}) {
		t.Fatalf("dev:3013 → ids %v static %v err %v; want [ws_a ws_b] (sorted, other kinds excluded)", ids, static, err)
	}
	ids, static, err = s.PortRegistrants("dev", 4242)
	if err != nil || !static || len(ids) != 0 {
		t.Fatalf("dev:4242 → ids %v static %v err %v; want the static server alone", ids, static, err)
	}
}
