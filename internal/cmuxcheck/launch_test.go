package cmuxcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func ts(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

func writeSentinel(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "running.sentinel")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectLaunchCleanQuit(t *testing.T) {
	l, err := DetectLaunch(writeSentinel(t, "pid=10865\n"), []string{"testdata/missing.jsonl.1", "testdata/events-clean.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if l.PID != 10865 || l.Current.ID != "B2" || !l.Start.Equal(ts("2026-10-08T10:31:00Z")) {
		t.Fatalf("launch = %+v", l)
	}
	if l.Previous == nil || l.Previous.ID != "B1" || !l.Previous.CleanQuit() {
		t.Fatalf("previous = %+v, want B1 clean", l.Previous)
	}
}

func TestDetectLaunchCrash(t *testing.T) {
	l, err := DetectLaunch(writeSentinel(t, "pid=2"), []string{"testdata/events-crash.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Previous == nil || l.Previous.CleanQuit() {
		t.Fatalf("previous = %+v, want an unclean end", l.Previous)
	}
}

func TestDetectLaunchErrors(t *testing.T) {
	if _, err := DetectLaunch(writeSentinel(t, "garbage"), []string{"testdata/events-clean.jsonl"}); err == nil {
		t.Fatal("unparsable sentinel must fail")
	}
	if _, err := DetectLaunch(writeSentinel(t, "pid=1"), []string{"testdata/none.jsonl"}); err == nil {
		t.Fatal("no event log at all must fail, never 'no previous launch'")
	}
}

func photoWith(src string, at time.Time, ids ...string) Photo {
	p := Photo{Source: src, TakenAt: at}
	for _, id := range ids {
		p.Panels = append(p.Panels, Panel{SurfaceID: id, Binding: &Binding{AutoResume: true, CheckpointID: "sid-" + id}})
	}
	return p
}

func cleanLaunch() Launch {
	prev := Boot{ID: "B1", Last: ts("2026-10-08T10:30:00.816Z"), ClosedAt: ts("2026-10-08T10:30:00.814Z")}
	return Launch{PID: 7, Start: ts("2026-10-08T10:31:00Z"), Previous: &prev}
}

func TestChooseBeforeFreshPrevious(t *testing.T) {
	for _, at := range []string{"2026-10-08T10:30:01Z", "2026-10-08T10:31:00Z"} { // quit second (brief) or launch second (F2)
		got, notes := ChooseBefore(cleanLaunch(), photoWith("previous", ts(at), "S1"), nil, nil)
		if got.Source != "previous" || len(got.Panels) != 1 || len(notes) != 0 {
			t.Fatalf("at %s: %+v %v", at, got, notes)
		}
	}
}

func TestChooseBeforeCrashUsesPeers(t *testing.T) {
	l := cleanLaunch()
	l.Previous.ClosedAt = time.Time{}                                 // crash
	stale := photoWith("previous", ts("2026-10-07T08:00:00Z"), "OLD") // another launch's photo
	peers := []pigtail.PeerSession{
		{SessionID: "p1", ConfigDir: "/cfg/a", Cwd: "/w/p1", CmuxSurfaceID: "S9", CmuxTitle: "nine", UpdatedAt: ts("2026-10-08T10:29:40Z")},
		{SessionID: "p2", ConfigDir: "/cfg/a", CmuxSurfaceID: "S8", UpdatedAt: ts("2026-10-08T09:00:00Z")}, // gone long before
		{SessionID: "p3", ConfigDir: "/cfg/a", UpdatedAt: ts("2026-10-08T10:29:40Z")},                      // no surface
	}
	got, notes := ChooseBefore(l, stale, nil, peers)
	if got.Source != "peers" || len(got.Panels) != 1 || got.Panels[0].SurfaceID != "S9" {
		t.Fatalf("photo = %+v", got)
	}
	b := got.Panels[0].Binding
	if b == nil || !b.AutoResume || b.CheckpointID != "p1" || b.ConfigDir() != "/cfg/a" || b.Cwd != "/w/p1" || b.Command != "" {
		t.Fatalf("binding = %+v", b)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "H1") {
		t.Fatalf("notes = %v, want the H1 warning", notes)
	}
}

// The photo names a panel by the workspace title GET /peers serves: the
// sidebar loop's live one (« fait »), empty for a peer redacted for the
// caller — then the row falls back to the pane title.
func TestChooseBeforeCrashUsesPeersTitle(t *testing.T) {
	l := cleanLaunch()
	l.Previous.ClosedAt = time.Time{}
	peers := []pigtail.PeerSession{
		{SessionID: "p1", ConfigDir: "/cfg/a", CmuxSurfaceID: "S9", CmuxTitle: "fait", UpdatedAt: ts("2026-10-08T10:29:40Z")},
		{SessionID: "p2", ConfigDir: "/cfg/a", CmuxSurfaceID: "S7", CmuxTitle: "", UpdatedAt: ts("2026-10-08T10:29:40Z")},
	}
	got, _ := ChooseBefore(l, photoWith("previous", ts("2026-10-07T08:00:00Z"), "OLD"), nil, peers)
	titles := map[string]string{}
	for _, p := range got.Panels {
		titles[p.SurfaceID] = p.WorkspaceTitle
	}
	if titles["S9"] != "fait" || titles["S7"] != "" {
		t.Fatalf("titles = %v", titles)
	}
	if n := (Row{SurfaceID: "S7", Title: "pane", WorkspaceTitle: ""}).Name(); n != "pane" {
		t.Fatalf("Name() = %q", n)
	}
}

func TestChooseBeforeCrashMergesFreshPrevious(t *testing.T) {
	l := cleanLaunch()
	l.Previous.ClosedAt = time.Time{}
	prev := photoWith("previous", ts("2026-10-08T10:31:00Z"), "S1")
	peers := []pigtail.PeerSession{
		{SessionID: "sid-S1", ConfigDir: "/cfg/a", CmuxSurfaceID: "S1", UpdatedAt: ts("2026-10-08T10:29:40Z")},
		{SessionID: "p9", ConfigDir: "/cfg/a", CmuxSurfaceID: "S9", UpdatedAt: ts("2026-10-08T10:29:40Z")},
	}
	got, _ := ChooseBefore(l, prev, nil, peers)
	if got.Source != "previous+peers" || len(got.Panels) != 2 {
		t.Fatalf("photo = %+v, want S1 from the file and S9 from peers", got)
	}
}
