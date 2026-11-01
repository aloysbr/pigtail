package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/store/storetest"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// pigtaild --cmux-folders wires the folder arrangement (Task 7 of
// sub_x000000000000041): the real folder CLI, the D6 order « Standby » last
// (D15, time 2), the D11 icons, the probe constants, the shell
// proof, and the folded state next to the daemon's database.
func TestServerWiresCmuxFolders(t *testing.T) {
	spaces, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	dbDir := t.TempDir()
	s, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(dbDir, "pigtail.db"), Spaces: spaces, CmuxFolders: true},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.cmuxSyncConfig()
	deps := s.cmuxSyncDeps()
	if !cfg.Folders {
		t.Fatal("--cmux-folders: Config.Folders false")
	}
	if cfg.GroupMoves != !cmux.ReorderMovesGroups || cfg.AddNeedsRemove != !cmux.AddMovesFromGroup {
		t.Fatalf("probe constants: GroupMoves=%v AddNeedsRemove=%v", cfg.GroupMoves, cfg.AddNeedsRemove)
	}
	if _, ok := deps.Folders.(cmux.FoldersCLI); !ok {
		t.Fatalf("Deps.Folders = %T, want cmux.FoldersCLI", deps.Folders)
	}
	want := spaces.FolderOrder()
	if deps.FolderOrder == nil || !slices.Equal(deps.FolderOrder(), want) {
		t.Fatalf("FolderOrder = %q, want %q", deps.FolderOrder(), want)
	}
	if got := deps.FolderOrder(); got[len(got)-1] != space.Standby {
		t.Fatalf("D6: « Standby » is the last folder: %q", got)
	}
	if got := deps.FolderIcon("Allotment"); got != "house.fill" {
		t.Fatalf("FolderIcon(Allotment) = %q", got)
	}
	if got := deps.FolderIcon("Inbox"); got != "tray" { // D9
		t.Fatalf("FolderIcon(Inbox) = %q, want tray", got)
	}
	if order := deps.FolderOrder(); order[len(order)-3] != "Inbox" || order[len(order)-2] != space.MacMaintenance {
		t.Fatalf("D9: FolderOrder = %q, want … Inbox, Maintenance, Standby", order)
	}
	if got := deps.FolderIcon(space.MacMaintenance); got != space.MacMaintenanceIcon {
		t.Fatalf("FolderIcon(Maintenance) = %q, want %q (G1)", got, space.MacMaintenanceIcon)
	}
	if deps.MacNeed == nil {
		t.Fatal("Deps.MacNeed nil: no « Maintenance » folder would ever be filled")
	}
	if got, err := deps.MacNeed(); err != nil || len(got) != 0 {
		t.Fatalf("MacNeed on an empty store = %v, %v", got, err)
	}
	if got := deps.FolderIcon(space.Standby); got != space.StandbyIcon {
		t.Fatalf("FolderIcon(Standby) = %q, want %q (D11)", got, space.StandbyIcon)
	}
	if deps.ShellAtPrompt == nil {
		t.Fatal("Deps.ShellAtPrompt nil: no anchor would ever be closable")
	}
	if deps.SurfaceNeverRendered == nil {
		t.Fatal("Deps.SurfaceNeverRendered nil: a never-rendered anchor would never close (D22)")
	}
	if deps.LoadCollapse == nil || deps.SaveCollapse == nil {
		t.Fatal("folded state not wired")
	}
	if err := deps.SaveCollapse(map[string]bool{"Allotment": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dbDir, "cmux-folders.json")); err != nil {
		t.Fatalf("the folded state is born next to the database: %v", err)
	}
	if m, err := deps.LoadCollapse(); err != nil || !m["Allotment"] {
		t.Fatalf("LoadCollapse = %v, %v", m, err)
	}

	off, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(t.TempDir(), "pigtail.db"), Spaces: spaces},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if off.cmuxSyncConfig().Folders {
		t.Fatal("without --cmux-folders, Config.Folders must be false")
	}
}

// The birth clock lives next to the database, like the folded state.
func TestServerWiresBirthsFile(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	dbDir := t.TempDir()
	s, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(dbDir, "pigtail.db"), CmuxFolders: true, CmuxBirthGrace: 2 * time.Minute},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.LoadBirths == nil || deps.SaveBirths == nil {
		t.Fatal("birth clock not wired")
	}
	if _, present, err := deps.LoadBirths(); err != nil || present {
		t.Fatalf("fresh dir: present=%v err=%v", present, err)
	}
	when := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if err := deps.SaveBirths(map[string]time.Time{"WS-1": when}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dbDir, "cmux-births.json")); err != nil {
		t.Fatalf("cmux-births.json not next to the database: %v", err)
	}
	if deps.BirthsModTime == nil {
		t.Fatal("birth file age not wired")
	}
	if mod, err := deps.BirthsModTime(); err != nil || time.Since(mod) > time.Minute {
		t.Fatalf("birth file mtime = %v, %v", mod, err)
	}
	m, present, err := deps.LoadBirths()
	if err != nil || !present || !m["WS-1"].Equal(when) {
		t.Fatalf("read back: %v %v %v", m, present, err)
	}
	if got := s.cmuxSyncConfig().BirthGrace; got != 2*time.Minute {
		t.Fatalf("BirthGrace = %v, want 2m0s", got)
	}
}

// Deps.Standby is the rangement's read of the Standby table (D30): session
// ids, one query for the whole table.
func TestServerWiresStandbyFolder(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(t.TempDir(), "pigtail.db"), CmuxFolders: true},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.Standby == nil {
		t.Fatal("Deps.Standby nil")
	}
	if got, err := deps.Standby(); err != nil || len(got) != 0 {
		t.Fatalf("empty table: %v, %v", got, err)
	}
	now := time.Now()
	if err := st.SetStandby("s1", now); err != nil {
		t.Fatal(err)
	}
	if got, err := deps.Standby(); err != nil || !got["s1"] || len(got) != 1 {
		t.Fatalf("Standby = %v, %v", got, err)
	}
	if _, err := st.ClearStandby("s1", now); err != nil {
		t.Fatal(err)
	}
	if got, err := deps.Standby(); err != nil || len(got) != 0 {
		t.Fatalf("after ClearStandby: %v, %v", got, err)
	}
}

// The loop reads the open focus from the store through FocusSource (Tâche 5
// of sub_x000000000000007): a focus past its deadline is gone at read, also
// when its seal fails (logged, never the read's error).
func TestServerWiresFocus(t *testing.T) {
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "pigtail.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t), DBPath: dbPath, CmuxFolders: true}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.Focus == nil {
		t.Fatal("Deps.Focus not wired")
	}
	open := pigtail.Focus{Name: "Open one", Scope: "perso"}
	if err := st.CreateFocus(&open, time.Now()); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		t.Helper()
		fs, err := deps.Focus()
		if err != nil {
			t.Fatalf("Deps.Focus: %v", err)
		}
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Open one"}) {
		t.Fatalf("open focus = %q, want [Open one]", got)
	}
	restore := storetest.FailFocusSeal(t, dbPath)
	// A focus created an hour ago whose deadline passed half an hour ago.
	past := time.Now().Add(-time.Hour)
	ends := past.Add(30 * time.Minute)
	due := pigtail.Focus{Name: "Due one", Scope: "perso", EndsAt: &ends}
	if err := st.CreateFocus(&due, past); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"Open one"}) {
		t.Fatalf("past its deadline, seal failing: open focus = %q, want [Open one]", got)
	}
	if f, _, err := st.GetFocus(due.ID, time.Now()); err != nil || f.EndedAt != nil && f.EndReason == "" {
		t.Fatalf("instrument: the due focus reads %+v, %v", f, err)
	}
	restore()
	if got := names(); !slices.Equal(got, []string{"Open one"}) {
		t.Fatalf("sealed: open focus = %q", got)
	}
	if f, _, err := st.GetFocus(due.ID, time.Now()); err != nil || f.EndedAt == nil || !f.EndedAt.Equal(ends.Truncate(time.Millisecond)) {
		t.Fatalf("restored, the due focus is sealed at its deadline: %+v, %v", f, err)
	}
}

// The drag writes (lot 3 of sub_x000000000000033, Tâche 7): the subject's and
// the off-topic declaration's space through the store's traced writes, the
// space's scope from the spaces table (« Inbox » is no space).
func TestCmuxsyncSpaceDragWired(t *testing.T) {
	spaces, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(t.TempDir(), "pigtail.db"), Spaces: spaces, CmuxFolders: true},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.SubjectSpaceSet == nil || deps.OffTopicSpaceSet == nil || deps.SpaceScope == nil {
		t.Fatal("drag Deps not wired")
	}
	for name, want := range map[string]string{"Vineyard": "globex", "Allotment": "perso"} {
		if got, ok := deps.SpaceScope(name); !ok || got != want {
			t.Fatalf("SpaceScope(%s) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{space.NoSubject, space.Standby, "Gone"} {
		if _, ok := deps.SpaceScope(name); ok {
			t.Fatalf("SpaceScope(%s) ok: not a space", name)
		}
	}
	now := time.Now()
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_w", Name: "T", Kind: "ongoing", Scope: "perso", VaultLink: "Notes/Areas/w", Space: "Allotment", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_w", ThemeID: "thm_w", Title: "S", ShortTitle: "S", Slug: "sub_w", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := deps.SubjectSpaceSet("sub_w", "Garden", "Allotment"); err != nil {
		t.Fatal(err)
	}
	if sub, err := st.GetSubject("sub_w"); err != nil || sub.Space != "Garden" {
		t.Fatalf("subject space %q (%v)", sub.Space, err)
	}
	if js, err := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: pigtail.JournalKindSubjectSpaceDrag}); err != nil || len(js) != 1 {
		t.Fatalf("journal %+v (%v)", js, err)
	}
	if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: "WS-W", Scope: "perso", Space: "Allotment", Reason: "r", DeclaredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := deps.OffTopicSpaceSet("WS-W", "Garden", "Allotment"); err != nil {
		t.Fatal(err)
	}
	if o, err := st.GetOffTopic("WS-W"); err != nil || o.Space != "Garden" {
		t.Fatalf("off-topic space %q (%v)", o.Space, err)
	}
}
