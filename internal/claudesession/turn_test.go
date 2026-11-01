package claudesession

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLastTurn(t *testing.T) {
	p := writeAPITranscript(t,
		`{"type":"user","promptId":"p1","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","timestamp":"2026-10-05T09:00:05Z","message":{"role":"assistant","content":[]}}`,
		`{"type":"user","promptId":"p2","timestamp":"2026-10-05T09:10:00Z","message":{"role":"user","content":"second"}}`,
		`{"type":"user","promptId":"p2","timestamp":"2026-10-05T09:10:30Z","message":{"role":"user","content":[{"type":"tool_result","content":"x"}]}}`,
		`{"type":"assistant","timestamp":"2026-10-05T09:11:00Z","message":{"role":"assistant","content":[]}}`,
	)
	tr, ok := LastTurn(p)
	want, _ := time.Parse(time.RFC3339, "2026-10-05T09:10:00Z")
	if !ok || tr.ID != "p2" || !tr.Start.Equal(want) {
		t.Fatalf("turn = %+v %v", tr, ok)
	}
	// A9: the entrypoint of the turn's prompt entry (cli = interactive; sdk-* = claude -p / SDK)
	pp := writeAPITranscript(t, `{"type":"user","promptId":"x1","entrypoint":"sdk-cli","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"go"}}`)
	if tr, ok := LastTurn(pp); !ok || tr.Entrypoint != "sdk-cli" {
		t.Fatalf("entrypoint = %+v %v", tr, ok)
	}
	if _, ok := LastTurn(writeAPITranscript(t, `{"type":"user","message":{"content":"no prompt id"}}`)); ok {
		t.Fatal("a transcript without promptId has no turn: nothing is recorded, never a guess")
	}
	if _, ok := LastTurn(""); ok {
		t.Fatal("no transcript path: no turn")
	}
}

// PreviousTurnID is the turn before the one in progress, read from the whole
// file: a current turn larger than LastTurn's tail must not hide it. A turn
// counts once its Stop hooks ran (the stop_hook_summary the harness writes).
func TestPreviousTurnID(t *testing.T) {
	user := func(id, content string) string {
		return `{"type":"user","promptId":"` + id + `","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"` + content + `"}}`
	}
	asst := `{"type":"assistant","timestamp":"2026-10-05T09:00:05Z","message":{"role":"assistant","content":[]}}`
	stop := `{"type":"system","subtype":"stop_hook_summary","hookCount":1,"hasOutput":false}`
	p := writeAPITranscript(t, user("p1", "a"), asst, stop, user("p2", "b"), user("p2", "tool result"), asst, stop, user("p3", "c"), user("p3", "tool result"))
	if id, ok := PreviousTurnID(p); !ok || id != "p2" {
		t.Fatalf("three turns: %q %v", id, ok)
	}
	big := strings.Repeat("x", 300<<10)
	p = writeAPITranscript(t, user("p1", "a"), asst, stop, user("p2", "b"), user("p2", big), asst)
	if id, ok := PreviousTurnID(p); !ok || id != "p1" {
		t.Fatalf("current turn > 256 KiB: %q %v", id, ok)
	}
	if _, ok := PreviousTurnID(writeAPITranscript(t, user("p1", "a"), user("p1", "tool result"))); ok {
		t.Fatal("one turn (its tool_result shares the promptId): no previous turn")
	}
	// A prompt with no Stop (interrupted, a usage limit, a prompt a
	// UserPromptSubmit hook blocked such as /pb) is not a turn that ended.
	p = writeAPITranscript(t, user("p1", "a"), asst, stop, user("p2", "b"), asst, user("p2", "[Request interrupted by user]"), user("pb", "<command-name>/pb</command-name>"), user("p3", "c"))
	if id, ok := PreviousTurnID(p); !ok || id != "p1" {
		t.Fatalf("prompts without a Stop: %q %v", id, ok)
	}
	if _, ok := PreviousTurnID(filepath.Join(t.TempDir(), "none.jsonl")); ok {
		t.Fatal("missing file: no previous turn")
	}
	if _, ok := PreviousTurnID(""); ok {
		t.Fatal("no path: no previous turn")
	}
}

// D-B2 (verify N2 of PR #210, round 2): /compact is a local command — its
// user entries (the command, the compact summary, its stdout) carry a
// promptId of their own but no Stop runs. The turn before the next prompt is
// still the last one that ended with a Stop. Fixture: a real /compact,
// anonymised (keys kept, text cut).
func TestPreviousTurnIDSkipsCompact(t *testing.T) {
	compact, err := os.ReadFile(filepath.Join("testdata", "transcript-compact.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	p := writeAPITranscript(t,
		`{"type":"user","promptId":"p1","timestamp":"2026-10-14T22:58:35Z","message":{"role":"user","content":"a"}}`,
		`{"type":"assistant","timestamp":"2026-10-14T22:58:37Z","message":{"role":"assistant","content":[]}}`,
		strings.TrimSpace(string(compact)),
		`{"type":"user","promptId":"p2","timestamp":"2026-10-14T22:59:19Z","message":{"role":"user","content":"/standby"}}`,
	)
	if id, ok := PreviousTurnID(p); !ok || id != "p1" {
		t.Fatalf("after /compact: %q %v, want p1 (the compact has no Stop)", id, ok)
	}
}

// D-B (verify N2 of PR #210): a heavy first turn (a new session, after
// /clear or /compact) writes lines after its prompt that outweigh the tail
// LastAPIError reads; the turn's prompt entry is then > 256 KiB from the
// end. LastTurn still finds it.
func TestLastTurnPromptBeyondTail(t *testing.T) {
	heavy := `{"type":"system","subtype":"api-request-blob","content":"` + strings.Repeat("x", 400<<10) + `"}`
	p := writeAPITranscript(t,
		`{"type":"user","promptId":"p1","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"first"}}`,
		`{"type":"user","promptId":"p2","entrypoint":"cli","timestamp":"2026-10-05T09:10:00Z","message":{"role":"user","content":"second"}}`,
		heavy,
		`{"type":"assistant","timestamp":"2026-10-05T09:11:00Z","message":{"role":"assistant","content":[]}}`,
	)
	tr, err := ReadLastTurn(p)
	want, _ := time.Parse(time.RFC3339, "2026-10-05T09:10:00Z")
	if err != nil || tr.ID != "p2" || !tr.Start.Equal(want) || tr.Entrypoint != "cli" {
		t.Fatalf("turn = %+v %v; want p2 from 09:10", tr, err)
	}
	if tr2, ok := LastTurn(p); !ok || tr2 != tr {
		t.Fatalf("LastTurn = %+v %v, ReadLastTurn = %+v", tr2, ok, tr)
	}
}

// The scan is bounded and says why it found nothing: never an empty turn
// silently taken for another one.
func TestReadLastTurnFailuresAreNamed(t *testing.T) {
	if _, err := ReadLastTurn(""); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("no path: %v", err)
	}
	if _, err := ReadLastTurn(filepath.Join(t.TempDir(), "missing.jsonl")); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := ReadLastTurn(writeAPITranscript(t, `{"type":"user","message":{"content":"no prompt id"}}`)); !errors.Is(err, ErrNoTurn) {
		t.Fatalf("no promptId: %v", err)
	}
	old := lastTurnScanMax
	lastTurnScanMax = 64 << 10
	t.Cleanup(func() { lastTurnScanMax = old })
	p := writeAPITranscript(t,
		`{"type":"user","promptId":"p1","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"first"}}`,
		`{"type":"system","content":"`+strings.Repeat("y", 200<<10)+`"}`,
	)
	_, err := ReadLastTurn(p)
	if !errors.Is(err, ErrTurnBeyondScan) || !strings.Contains(err.Error(), "KiB") {
		t.Fatalf("beyond the bound: %v", err)
	}
}

// TurnStartOf is the first user entry of the promptId, read on the whole file.
func TestTurnStartOfFirstUserEntry(t *testing.T) {
	p := writeAPITranscript(t,
		`{"type":"user","promptId":"p1","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"first"}}`,
		`{"type":"user","promptId":"p2","timestamp":"2026-10-05T09:10:00Z","message":{"role":"user","content":"second"}}`,
		`{"type":"user","promptId":"p2","timestamp":"2026-10-05T09:10:30Z","message":{"role":"user","content":[{"type":"tool_result","content":"x"}]}}`,
	)
	got, ok := TurnStartOf(p, "p2")
	want, _ := time.Parse(time.RFC3339, "2026-10-05T09:10:00Z")
	if !ok || !got.Equal(want) {
		t.Fatalf("got %v %v, want %v", got, ok, want)
	}
	if _, ok := TurnStartOf(p, "nope"); ok {
		t.Fatal("unknown promptId must be ok=false")
	}
	if _, ok := TurnStartOf("", "p2"); ok {
		t.Fatal("no path must be ok=false")
	}
}
