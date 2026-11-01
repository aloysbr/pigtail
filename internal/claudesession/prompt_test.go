package claudesession

// Shapes measured 2026-10-23 on local transcripts (shapes only): a typed
// prompt is a "user" entry with origin.kind "human"; a prompt typed during a
// turn is an "attachment" entry (queued_command) with attachment.origin.kind
// "human" and no "user" entry; task notifications, peers and auto
// continuations carry another origin; tool results, local commands and
// compact summaries carry none.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const promptFixture = "testdata/transcript-prompts.jsonl"

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func texts(ps []Prompt) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Text
	}
	return out
}

func tempCopy(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(promptFixture)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendRaw(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func humanJSON(ts, text string) string {
	return `{"type":"user","timestamp":"` + ts + `","origin":{"kind":"human"},"message":{"role":"user","content":"` + text + `"}}`
}

func TestPromptsAfterKeepsHumanPromptsOnly(t *testing.T) {
	st, _ := os.Stat(promptFixture)
	got, next, err := PromptsAfter(promptFixture, 0, at("2026-10-23T10:00:02Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d prompts %q", len(got), texts(got))
	}
	if got[0].Text != "look" || !got[0].Image {
		t.Errorf("0 = %+v", got[0])
	}
	if got[1].Text != "" || !got[1].Image {
		t.Errorf("1 = %+v", got[1])
	}
	for i, w := range []string{"go on", "queued one", "queued two"} {
		if got[i+2].Text != w {
			t.Errorf("%d = %q want %q", i+2, got[i+2].Text, w)
		}
	}
	if !strings.Contains(got[5].Text, "<command-name>/open</command-name>") {
		t.Errorf("5 = %q", got[5].Text)
	}
	if next != st.Size() {
		t.Errorf("next = %d, size %d", next, st.Size())
	}
}

func TestPromptsAfterSinceIsStrict(t *testing.T) {
	got, _, _ := PromptsAfter(promptFixture, 0, at("2026-10-23T10:00:13Z"))
	if g := texts(got); len(g) < 1 || g[0] != "queued one" {
		t.Errorf("go on kept or queued lost: %q", g)
	}
	got, _, _ = PromptsAfter(promptFixture, 0, at("2026-10-23T10:00:14Z"))
	if g := texts(got); len(g) < 1 || g[0] != "queued two" {
		t.Errorf("queued stamp not strict: %q", g)
	}
}

func TestPromptsAfterResumesAtItsOffset(t *testing.T) {
	p := tempCopy(t)
	_, next, err := PromptsAfter(p, 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	appendRaw(t, p, humanJSON("2026-10-23T10:01:00Z", "later")+"\n")
	got, _, _ := PromptsAfter(p, next, time.Time{})
	if g := texts(got); len(g) != 1 || g[0] != "later" {
		t.Errorf("got %q", g)
	}
}

func TestPromptsAfterLeavesAPartialLine(t *testing.T) {
	p := tempCopy(t)
	st, _ := os.Stat(p)
	appendRaw(t, p, humanJSON("2026-10-23T10:01:00Z", "half"))
	got, next, _ := PromptsAfter(p, st.Size(), time.Time{})
	if len(got) != 0 || next != st.Size() {
		t.Fatalf("got %q next %d want 0 / %d", texts(got), next, st.Size())
	}
	appendRaw(t, p, "\n")
	got, _, _ = PromptsAfter(p, next, time.Time{})
	if g := texts(got); len(g) != 1 || g[0] != "half" {
		t.Errorf("got %q", g)
	}
}

func TestPromptsAfterFromOnALineStartKeepsItsFirstLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	l1 := humanJSON("2026-10-23T10:00:01Z", "one") + "\n"
	l2 := humanJSON("2026-10-23T10:00:02Z", "two") + "\n"
	if err := os.WriteFile(p, []byte(l1+l2), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, _ := PromptsAfter(p, int64(len(l1)), time.Time{})
	if g := texts(got); len(g) != 1 || g[0] != "two" {
		t.Errorf("got %q", g)
	}
}

func TestPromptsAfterFromMidLineSkipsTheRest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	l1 := humanJSON("2026-10-23T10:00:01Z", "one") + "\n"
	l2 := humanJSON("2026-10-23T10:00:02Z", "two") + "\n"
	if err := os.WriteFile(p, []byte(l1+l2), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, _ := PromptsAfter(p, 1, time.Time{})
	if g := texts(got); len(g) != 1 || g[0] != "two" {
		t.Errorf("got %q", g)
	}
}

func TestPromptsAfterNegativeFromReadsTheTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	tr := `{"type":"user","timestamp":"2026-10-23T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("x", 1000) + `"}]}}` + "\n"
	body := strings.Repeat(tr, 300) + humanJSON("2026-10-23T10:05:00Z", "tail") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := PromptsAfter(p, -1, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if g := texts(got); len(g) != 1 || g[0] != "tail" {
		t.Errorf("got %q", g)
	}
}

func TestPromptsAfterRereadsAShorterFile(t *testing.T) {
	p := tempCopy(t)
	got, _, err := PromptsAfter(p, 1<<30, at("2026-10-23T10:00:12Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Text != "go on" {
		t.Errorf("got %q", texts(got))
	}
}

func TestPromptsAfterCapsOneRead(t *testing.T) {
	old := promptReadMax
	promptReadMax = 1 << 10
	t.Cleanup(func() { promptReadMax = old })

	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(humanJSON("2026-10-23T10:00:01Z", "first")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	from := st.Size()
	tr := `{"type":"user","timestamp":"2026-10-23T10:00:03Z","message":{"role":"user","content":"` + strings.Repeat("x", 200) + `"}}` + "\n"
	appendRaw(t, p, strings.Repeat(tr, 16)+humanJSON("2026-10-23T10:05:00Z", "last")+"\n")
	got, next, _ := PromptsAfter(p, from, time.Time{})
	st, _ = os.Stat(p)
	if g := texts(got); len(g) != 1 || g[0] != "last" || next != st.Size() {
		t.Fatalf("got %q next %d size %d", g, next, st.Size())
	}

	q := filepath.Join(t.TempDir(), "q.jsonl")
	if err := os.WriteFile(q, []byte(strings.Repeat("y", 2<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, next, _ = PromptsAfter(q, 0, time.Time{})
	if next != 2<<10 {
		t.Errorf("next = %d, want size", next)
	}
}

func TestPromptsAfterMissingFile(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "nope.jsonl")} {
		_, next, err := PromptsAfter(p, 7, time.Time{})
		if !errors.Is(err, ErrNoTranscript) || next != 7 {
			t.Errorf("%q: err %v next %d", p, err, next)
		}
	}
}
