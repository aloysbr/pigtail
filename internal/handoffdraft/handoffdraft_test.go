package handoffdraft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestExtractTLDRAfterLastMarker(t *testing.T) {
	msg := "intro\n**TL;DR**\nfirst\n\nmore\n**TL;DR**\nsecond one"
	tldr, tail := ExtractTLDR(msg)
	if tldr != "second one" || tail != "" {
		t.Fatalf("tldr=%q tail=%q", tldr, tail)
	}
	big := "**TL;DR**\n" + strings.Repeat("é", 5000)
	tldr, _ = ExtractTLDR(big)
	if len(tldr) > 4000 || !utf8.ValidString(tldr) {
		t.Fatalf("tldr len=%d valid=%v", len(tldr), utf8.ValidString(tldr))
	}
	if len(tldr) < 3998 {
		t.Fatalf("tldr cut too early: %d", len(tldr))
	}
}

func TestExtractTLDRNoMarkerFallsBackToTail(t *testing.T) {
	msg := strings.Repeat("a", 3000) + "END"
	tldr, tail := ExtractTLDR(msg)
	if tldr != "" {
		t.Fatalf("tldr=%q", tldr)
	}
	if n := len([]rune(tail)); n != 1500 || !strings.HasSuffix(tail, "END") {
		t.Fatalf("tail runes=%d", n)
	}
}

func TestWriteReadRoundTripAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hd")
	d := Draft{SessionID: "s1", TurnID: "t1", TurnAt: time.Unix(100, 0).UTC(), Cwd: "/x", TLDR: "hi"}
	if err := Write(dir, d); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir, "s1")
	if err != nil || got.TLDR != "hi" || got.TurnID != "t1" || !got.TurnAt.Equal(d.TurnAt) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp %s", e.Name())
		}
	}
	fi, _ := os.Stat(filepath.Join(dir, "s1.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
}

func TestWriteRejectsTraversalSessionID(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../x", "a/b", "..", ""} {
		if err := Write(dir, Draft{SessionID: id, TurnID: "t"}); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}

func TestWriteRejectsEmptyTurnID(t *testing.T) {
	if err := Write(t.TempDir(), Draft{SessionID: "s"}); err == nil {
		t.Fatal("accepted empty turn id")
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	ids, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

func TestRemoveMissingIsNil(t *testing.T) {
	if err := Remove(t.TempDir(), "ghost"); err != nil {
		t.Fatal(err)
	}
	if rm, err := RemoveIfTurn(t.TempDir(), "ghost", "t"); rm || err != nil {
		t.Fatalf("rm=%v err=%v", rm, err)
	}
}

func TestMoveAsideRenamesToBadAndListSkipsIt(t *testing.T) {
	dir := t.TempDir()
	if err := MoveAside(dir, "none"); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, Draft{SessionID: "s", TurnID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := MoveAside(dir, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s.json.bad")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, "s"); err == nil || !os.IsNotExist(err) {
		t.Fatalf("read after aside: %v", err)
	}
	ids, _ := List(dir)
	if len(ids) != 0 {
		t.Fatalf("ids=%v", ids)
	}
	// overwrites a previous .bad
	_ = Write(dir, Draft{SessionID: "s", TurnID: "t2"})
	if err := MoveAside(dir, "s"); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveIfTurnKeepsNewerTurn(t *testing.T) {
	dir := t.TempDir()
	_ = Write(dir, Draft{SessionID: "s", TurnID: "new"})
	rm, err := RemoveIfTurn(dir, "s", "old")
	if rm || err != nil {
		t.Fatalf("rm=%v err=%v", rm, err)
	}
	if _, err := Read(dir, "s"); err != nil {
		t.Fatal("file lost")
	}
	rm, err = RemoveIfTurn(dir, "s", "new")
	if !rm || err != nil {
		t.Fatalf("rm=%v err=%v", rm, err)
	}
}

func TestMarkHandledRoundTripAndListSkips(t *testing.T) {
	dir := t.TempDir()
	if got := ReadHandled(dir, "s"); got != "" {
		t.Fatalf("got %q", got)
	}
	if err := MarkHandled(dir, "s", "t9"); err != nil {
		t.Fatal(err)
	}
	if got := ReadHandled(dir, "s"); got != "t9" {
		t.Fatalf("got %q", got)
	}
	_ = Write(dir, Draft{SessionID: "a", TurnID: "t"})
	_ = os.WriteFile(filepath.Join(dir, "x.json.tmp"), []byte("{}"), 0o600)
	ids, _ := List(dir)
	if len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestPurgeStaleRemovesOldHandledAndBadOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	mk := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	old, young := 31*24*time.Hour, 29*24*time.Hour
	mk("a.handled", old)
	mk("b.json.bad", old)
	mk("c.handled", young)
	mk("d.json.bad", young)
	mk("e.json", old) // a live draft is never this function's business
	if err := PurgeStale(dir, 30*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"a.handled": false, "b.json.bad": false, "c.handled": true, "d.json.bad": true, "e.json": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Fatalf("%s present=%v, want %v", name, err == nil, want)
		}
	}
	if err := PurgeStale(filepath.Join(dir, "missing"), time.Hour, now); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}
