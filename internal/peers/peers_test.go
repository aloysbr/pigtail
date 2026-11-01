package peers

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/presence"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildJoinsCmuxNativeAndCells(t *testing.T) {
	cs, err := ParseCmuxSessions(fixture(t, "cmux_sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("codex row not filtered: %d", len(cs))
	}
	titles := ParseWorkspaceTitles(string(fixture(t, "workspace_list.txt")))
	if titles["29664DF9-67B9-4AAF-9419-B1FD8E3A8994"] != "◑ Agents avec playwright en environnement virtuel" {
		t.Fatalf("titles = %#v", titles)
	}
	cells := &claudeprofile.Table{Cells: map[string]claudeprofile.Cell{
		"w2": {ID: "w2", ConfigDir: "/Users/u/.claude-globex-max", Scope: "gx"},
		"h1": {ID: "h1", ConfigDir: "/Users/u/.claude-globex-ai", Scope: "gx"},
	}}
	natives := map[int]claudesession.Native{
		32855: {PID: 32855, SessionID: "a1171c31-d62c-4ffa-a997-5f8dc548a2da", Name: "globex-fa", Status: "busy"},
		40001: {PID: 40001, SessionID: "someone-else"}, // pid file names another session: not trusted
	}
	got := Build(Inputs{
		Cmux: cs, Titles: titles, Cells: cells,
		Live: []claudesession.Live{{PID: 777, SessionID: "outside-cmux", ConfigDir: "/Users/u/.claude-globex-ai", Cwd: "/tmp"}},
		NativeByPID: func(_ string, pid int) (claudesession.Native, bool) {
			n, ok := natives[pid]
			return n, ok
		},
		NativeBySession: func(string, string) (claudesession.Native, bool) { return claudesession.Native{}, false },
		Now:             time.Unix(0, 0),
	})
	if len(got) != 3 {
		t.Fatalf("got %d peers: %+v", len(got), got)
	}
	a := got[0]
	if a.Name != "globex-fa" || a.Status != "busy" || a.CellID != "w2" || a.Scope != "gx" ||
		a.CmuxTitle != "Agents avec playwright en environnement virtuel" || a.PID != 32855 {
		t.Fatalf("peer a = %+v", a)
	}
	if got[1].Name != "" || got[1].CellID != "h1" || got[1].CmuxTitle != "Garden bouton création visiteur" {
		t.Fatalf("peer b = %+v", got[1])
	}
	if got[2].SessionID != "outside-cmux" || got[2].PID != 777 || got[2].CellID != "h1" {
		t.Fatalf("peer c = %+v", got[2])
	}
}

func TestResolveOrderAndAmbiguity(t *testing.T) {
	ps := []pigtail.PeerSession{
		{SessionID: "aaaa1111-0000", SubjectSlug: "session-relay", SubjectID: "sub_1", CmuxTitle: "Relay work", CellID: "w2"},
		{SessionID: "bbbb2222-0000", WorkspaceName: "feat-panel", CmuxTitle: "Panel UX", Name: "panel"},
		{SessionID: "cccc3333-0000", CmuxTitle: "Panel review", Name: "reviewer"},
	}
	cases := []struct {
		addr, want string
	}{
		{"session-relay", "aaaa1111-0000"},
		{"sub_1", "aaaa1111-0000"},
		{"FEAT-PANEL", "bbbb2222-0000"},
		{"relay work", "aaaa1111-0000"},
		{"reviewer", "cccc3333-0000"},
		{"cccc", "cccc3333-0000"},
		{"bbbb2222-0000", "bbbb2222-0000"},
		{"panel", "bbbb2222-0000"}, // exact native name beats the title substring
	}
	for _, c := range cases {
		p, err := Resolve(ps, c.addr)
		if err != nil || p.SessionID != c.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", c.addr, p.SessionID, err, c.want)
		}
	}
	_, err := Resolve(ps, "pane")
	var amb *ErrAmbiguous
	if !errors.As(err, &amb) || amb.Level != "cmux title" || len(amb.Candidates) != 2 {
		t.Fatalf("pane: %v", err)
	}
	// The label's primary is the session's name: an unbound cmux workspace's title (D3).
	if !strings.Contains(err.Error(), "Panel UX · panel [bbbb2222]") || !strings.Contains(err.Error(), "Panel review · reviewer [cccc3333]") {
		t.Fatalf("candidates not listed: %v", err)
	}
	var unr *ErrUnresolved
	if _, err := Resolve(ps, "nothing"); !errors.As(err, &unr) {
		t.Fatalf("nothing: %v", err)
	}
	if _, err := Resolve(ps, "aa"); !errors.As(err, &unr) {
		t.Fatalf("2-char prefix must not resolve: %v", err)
	}
}

func TestLabel(t *testing.T) {
	cases := []struct {
		p    pigtail.PeerSession
		want string
	}{
		// Bound: the short title, never the slug, whatever cmux shows now.
		{pigtail.PeerSession{SessionID: "abcdef123456", SubjectSlug: "relay", SubjectShortTitle: "Relais de messages", CmuxBound: true,
			CmuxTitle: "fait", WorkspaceName: "wt", Name: "globex-fa", CellID: "w2"}, "Relais de messages · globex-fa (w2)"},
		// Unbound workspace with a subject deduced from the worktree: its cmux title.
		{pigtail.PeerSession{SessionID: "abcdef123456", SubjectSlug: "relay", SubjectShortTitle: "Relais", CmuxTitle: "nas", Name: "x-1"}, "nas · x-1"},
		// Not in cmux, subject deduced from the worktree: its short title.
		{pigtail.PeerSession{SessionID: "abcdef123456", SubjectSlug: "relay", SubjectShortTitle: "Relais", WorkspaceName: "wt", CellID: "w2"}, "Relais · abcdef12 (w2)"},
		{pigtail.PeerSession{SessionID: "abcdef123456", WorkspaceName: "wt", Name: "globex-fa", CellID: "w2"}, "wt · globex-fa (w2)"},
		{pigtail.PeerSession{SessionID: "abcdef123456", Name: "globex-fa", CellID: "w2"}, "globex-fa (w2)"},
		{pigtail.PeerSession{SessionID: "abcdef123456"}, "abcdef12"},
		{pigtail.PeerSession{SessionID: "abcdef123456", CmuxTitle: "Agents avec playwright en environnement virtuel", Name: "globex-d4", CellID: "h1"},
			"Agents avec playwright en envir… · globex-d4 (h1)"},
		// The cut drops a trailing space before "…": the 31st rune of this title is a space
		// (43 runes; the base gives "Débogage du relais de messages … · x-1").
		{pigtail.PeerSession{SessionID: "abcdef123456", CmuxTitle: "Débogage du relais de messages cmux du soir", Name: "x-1"}, "Débogage du relais de messages… · x-1"},
	}
	for _, c := range cases {
		if got := Label(c.p); got != c.want {
			t.Errorf("Label(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestResolveByNameAndStaleLabel(t *testing.T) {
	ps := []pigtail.PeerSession{
		{SessionID: "aaaa1111-0000", SubjectID: "sub_1", SubjectSlug: "relay", SubjectShortTitle: "Relance cmux sûre", CmuxBound: true, CmuxTitle: "Relance cmux sûre", Name: "globex-fa", CellID: "w2"},
		{SessionID: "bbbb2222-0000", SubjectID: "sub_2", SubjectSlug: "other", SubjectShortTitle: "Autre", CmuxBound: true, CmuxTitle: "Autre", Name: "globex-bb", CellID: "w2"},
	}
	for addr, want := range map[string]string{
		"Relance cmux sûre":            "aaaa1111-0000", // the name, exact
		"relay":                        "aaaa1111-0000", // the slug stays an address
		"relay · globex-fa (w2)":       "aaaa1111-0000", // a label written before the change
		"Ancien titre · aaaa1111 (w2)": "aaaa1111-0000", // stale primary, id handle ≥ 8
		Label(ps[0]):                   "aaaa1111-0000",
		"Relance… · globex-fa":         "aaaa1111-0000", // truncated by hand
		"Autre":                        "bbbb2222-0000",
	} {
		p, err := Resolve(ps, addr)
		if err != nil || p.SessionID != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", addr, p.SessionID, err, want)
		}
	}
	var unr *ErrUnresolved
	for _, addr := range []string{"Ancien titre · aaaa (w2)", "relay · globex-fa (h1)"} { // short id handle, wrong cell
		if _, err := Resolve(ps, addr); !errors.As(err, &unr) {
			t.Errorf("Resolve(%q) = %v; want unresolved", addr, err)
		}
	}
}

// Two sessions in one cmux workspace share its title: the E2E defect of the
// first relay run, where both read "Agents avec playwright en environnement…".
func sameWorkspacePeers() []pigtail.PeerSession {
	const title = "Agents avec playwright en environnement virtuel"
	return []pigtail.PeerSession{
		{SessionID: "d4d4d4d4-0000", CmuxTitle: title, Name: "globex-d4", CellID: "w2"},
		{SessionID: "e5e5e5e5-0000", CmuxTitle: title, Name: "globex-e5", CellID: "h1"},
		{SessionID: "f6f6f6f6-0000", CmuxTitle: title, Name: "globex-f6", CellID: "h1"},
	}
}

func TestLabelsUniqueInOneWorkspace(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range sameWorkspacePeers() {
		l := Label(p)
		if seen[l] {
			t.Fatalf("duplicate label %q", l)
		}
		seen[l] = true
	}
}

func TestResolveLabelForms(t *testing.T) {
	ps := sameWorkspacePeers()
	cases := []struct{ addr, want string }{
		{"globex-e5", "e5e5e5e5-0000"},
		{Label(ps[2]), "f6f6f6f6-0000"},                                   // pasted label, truncated title and all
		{"agents avec playwright en envir… · globex-d4", "d4d4d4d4-0000"}, // without the cell
		{"Agents avec playwright (w2)", "d4d4d4d4-0000"},                  // title part + cell
		{"playwright · e5e5e5e5", "e5e5e5e5-0000"},                        // handle as an id prefix
		{"globex-f6 (h1)", "f6f6f6f6-0000"},
	}
	for _, c := range cases {
		p, err := Resolve(ps, c.addr)
		if err != nil || p.SessionID != c.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", c.addr, p.SessionID, err, c.want)
		}
	}
	// A wrong handle or cell resolves nothing rather than falling back to the title.
	var unr *ErrUnresolved
	for _, addr := range []string{"Agents avec playwright · globex-zz", "globex-d4 (h1)"} {
		if _, err := Resolve(ps, addr); !errors.As(err, &unr) {
			t.Errorf("Resolve(%q) = %v; want unresolved", addr, err)
		}
	}
	// The title alone is still ambiguous, and the error names each session by
	// its unique label.
	_, err := Resolve(ps, "Agents avec playwright")
	var amb *ErrAmbiguous
	if !errors.As(err, &amb) || len(amb.Candidates) != 3 {
		t.Fatalf("title alone: %v", err)
	}
	for _, want := range []string{
		"Agents avec playwright en envir… · globex-d4 (w2) [d4d4d4d4]",
		"Agents avec playwright en envir… · globex-e5 (h1) [e5e5e5e5]",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	// Title + cell still ambiguous when the cell holds two of them.
	if _, err := Resolve(ps, "Agents avec playwright (h1)"); !errors.As(err, &amb) || amb.Level != "label" || len(amb.Candidates) != 2 {
		t.Fatalf("title (h1): %v", err)
	}
}

func TestAliveTreatsEndedAndUnknown(t *testing.T) {
	now := time.Now()
	if Alive(pigtail.PeerSession{SessionID: "x", EndedAt: &now}) {
		t.Fatal("ended peer alive")
	}
	if !Alive(pigtail.PeerSession{SessionID: "x"}) {
		t.Fatal("peer without pid must count as alive")
	}
	if Alive(pigtail.PeerSession{SessionID: "x", PID: 999999, ConfigDir: t.TempDir()}) {
		t.Fatal("dead pid alive")
	}
}

func TestResolveLastWorktree(t *testing.T) {
	ps := []pigtail.PeerSession{
		{SessionID: "aaaa1111-0000", Name: "globex-21", CmuxTitle: "Error UI",
			LastWorktree: "/c/tws/feat-tws-error-pages-ds"},
		{SessionID: "bbbb2222-0000", Name: "globex-cd", WorkspaceName: "feat-role-tools-panel",
			LastWorktree: "/c/tws/feat-other-place"},
	}
	p, level, err := ResolveLevel(ps, "FEAT-TWS-ERROR-PAGES-DS")
	if err != nil || p.SessionID != "aaaa1111-0000" || level != LevelLastWorktree {
		t.Fatalf("got %q level %q err %v", p.SessionID, level, err)
	}
	// The cwd-derived worktree level still decides first.
	if _, level, _ := ResolveLevel(ps, "feat-role-tools-panel"); level != "worktree" {
		t.Fatalf("cwd worktree level = %q", level)
	}
	// R5: a session WITH a cwd-derived worktree is not addressable by its
	// marker — the column shows the cwd one, so only that one resolves.
	if _, _, err := ResolveLevel(ps, "feat-other-place"); err == nil {
		t.Fatal("marker of a session with a cwd worktree must not resolve")
	}
	// A substring of the path is not an address: exact basename only.
	if _, _, err := ResolveLevel(ps, "error-pages"); err == nil {
		t.Fatal("substring of a last worktree must not resolve")
	}
}

func TestResolveLastWorktreeAmbiguous(t *testing.T) {
	ps := []pigtail.PeerSession{
		{SessionID: "aaaa1111-0000", Name: "globex-fa", LastWorktree: "/c/monorepo/main"},
		{SessionID: "bbbb2222-0000", Name: "globex-96", LastWorktree: "/c/tws/main"},
	}
	_, _, err := ResolveLevel(ps, "main")
	var amb *ErrAmbiguous
	if !errors.As(err, &amb) || amb.Level != LevelLastWorktree || len(amb.Candidates) != 2 {
		t.Fatalf("two sessions with a 'main' last worktree must be ambiguous, got %v", err)
	}
}

func TestAttachMarkersAndAge(t *testing.T) {
	at := time.Date(2026, 9, 30, 19, 0, 0, 0, time.UTC)
	ps := []pigtail.PeerSession{{SessionID: "s1"}, {SessionID: "s2"}, {SessionID: "s3"}}
	AttachMarkers(ps, map[string]presence.Marker{
		"s1": {Path: "/wt/x", At: at},
		"s3": {Path: "/wt/deleted", At: at, Gone: true}, // R1: attached, so the store learns it
	})
	if ps[0].LastWorktree != "/wt/x" || ps[0].LastWorktreeAt == nil || !ps[0].LastWorktreeAt.Equal(at) {
		t.Fatalf("s1 = %+v", ps[0])
	}
	if ps[1].LastWorktree != "" || ps[1].LastWorktreeAt != nil {
		t.Fatalf("s2 got a marker it does not have: %+v", ps[1])
	}
	if ps[2].LastWorktree != "/wt/deleted" {
		t.Fatalf("gone marker not attached: %+v", ps[2])
	}
	for d, want := range map[time.Duration]string{
		20 * time.Second: "<1m", 12 * time.Minute: "12m", 3*time.Hour + 5*time.Minute: "3h", 50 * time.Hour: "2d",
	} {
		if got := Age(d); got != want {
			t.Errorf("Age(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestAliveFalseForParkedSession: the relay's liveness (behind `session ls`'s
// pane-less rows) drops a session whose pid file was parked as a background job.
func TestAliveFalseForParkedSession(t *testing.T) {
	cfg := t.TempDir()
	if err := os.MkdirAll(cfg+"/sessions", 0o700); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	body := []byte(`{"pid":` + strconv.Itoa(pid) + `,"sessionId":"old","status":"busy","parkedJobId":"job-1"}`)
	if err := os.WriteFile(cfg+"/sessions/"+strconv.Itoa(pid)+".json", body, 0o600); err != nil {
		t.Fatal(err)
	}
	if Alive(pigtail.PeerSession{SessionID: "old", PID: pid, ConfigDir: cfg}) {
		t.Fatal("parked session reported alive")
	}
}

func TestParseCmuxSessionsReadsActiveFlags(t *testing.T) {
	b := []byte(`{"sessions":[{"session_id":"s1","surface_id":"S","pid":7,"agent":"claude","active_for_surface":true,"active_surface_session_id":"s1"}]}`)
	cs, err := ParseCmuxSessions(b)
	if err != nil || len(cs) != 1 || !cs[0].ActiveForSurface || cs[0].ActiveSurfaceSessionID != "s1" {
		t.Fatalf("got %+v %v", cs, err)
	}
}

func TestLiveOnlyKeepsAliveAndSelfInOrder(t *testing.T) {
	ps := []pigtail.PeerSession{{SessionID: "a"}, {SessionID: "dead"}, {SessionID: "me"}, {SessionID: "b"}}
	alive := func(p pigtail.PeerSession) bool { return p.SessionID == "a" || p.SessionID == "b" }
	got := LiveOnly(ps, alive, "me")
	if len(got) != 3 || got[0].SessionID != "a" || got[1].SessionID != "me" || got[2].SessionID != "b" {
		t.Fatalf("LiveOnly = %+v", got)
	}
	if got := LiveOnly(ps, alive, ""); len(got) != 2 {
		t.Fatalf("no keep: %+v", got)
	}
}
