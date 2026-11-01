package waitdetail

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Typed answers (sub_x000000000000026, Task 8, spec § 4): which forms each
// wait type takes, and the French body the session reads.

const ghToken = "ghp_0123456789abcdefghij0123456789abcd"

func arbitrageWait() pigtail.Wait {
	return pigtail.Wait{ID: "wait_1", Type: "arbitrage", Reason: "Sidebar order", Source: pigtail.WaitSourceDeclared,
		Details: &pigtail.WaitDetails{Options: []pigtail.WaitOption{
			{ID: "A", Label: "Folders first", Reco: true}, {ID: "B", Label: "Flat"}}, Why: "cheaper"}}
}

func reviewWait(n int) pigtail.Wait {
	items := make([]pigtail.WaitItem, n)
	for i := range items {
		items[i] = pigtail.WaitItem{Label: fmt.Sprintf("item %d", i+1)}
	}
	return pigtail.Wait{ID: "wait_r", Type: "review", Reason: "Checklist", Details: &pigtail.WaitDetails{Items: items}}
}

func mustErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one containing %q", err, want)
	}
}

func TestParseAnswerChoiceReco(t *testing.T) {
	a, err := ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Choice: "reco"})
	if err != nil || a.Choice != "A" || a.Text != "" {
		t.Fatalf("answer = %+v, %v", a, err)
	}
	a, err = ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Choice: "B"})
	if err != nil || a.Choice != "B" {
		t.Fatalf("answer = %+v, %v", a, err)
	}
}

func TestParseAnswerUnknownChoice(t *testing.T) {
	_, err := ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Choice: "C"})
	mustErr(t, err, "C")
	q := pigtail.Wait{ID: "wait_q", Type: "question"}
	_, err = ParseAnswer(q, pigtail.WaitAnswerRequest{Choice: "1"})
	mustErr(t, err, "choice")
}

func TestParseAnswerItemsAllJudged(t *testing.T) {
	_, err := ParseAnswer(reviewWait(3), pigtail.WaitAnswerRequest{Items: []string{"1=ok", "3=ok"}})
	mustErr(t, err, "item 2 not judged")
	_, err = ParseAnswer(reviewWait(2), pigtail.WaitAnswerRequest{Items: []string{"1=ok", "1=ok", "2=ok"}})
	mustErr(t, err, "item 1")
	_, err = ParseAnswer(reviewWait(2), pigtail.WaitAnswerRequest{Items: []string{"1=ok", "2=ok", "3=ok"}})
	mustErr(t, err, "item 3")
	a, err := ParseAnswer(reviewWait(2), pigtail.WaitAnswerRequest{Items: []string{"2=ko:button hidden", "1=ok"}})
	if err != nil || len(a.Items) != 2 || a.Items[0] != (pigtail.WaitItemVerdict{N: 1, OK: true}) ||
		a.Items[1] != (pigtail.WaitItemVerdict{N: 2, OK: false, Note: "button hidden"}) {
		t.Fatalf("answer = %+v, %v", a, err)
	}
}

func TestParseAnswerKoNeedsNote(t *testing.T) {
	_, err := ParseAnswer(reviewWait(1), pigtail.WaitAnswerRequest{Items: []string{"1=ko"}})
	mustErr(t, err, "note")
	_, err = ParseAnswer(reviewWait(1), pigtail.WaitAnswerRequest{Items: []string{"1=ko:  "}})
	mustErr(t, err, "note")
	_, err = ParseAnswer(reviewWait(1), pigtail.WaitAnswerRequest{Items: []string{"1=maybe"}})
	mustErr(t, err, "ok|ko")
}

func TestParseAnswerOneFormOnly(t *testing.T) {
	_, err := ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Choice: "A", Text: "or B"})
	mustErr(t, err, "one form")
	_, err = ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{})
	mustErr(t, err, "one form")
	_, err = ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Reason: "only a reason"})
	mustErr(t, err, "one form")
}

func TestParseAnswerArbitrageText(t *testing.T) {
	a, err := ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Text: "neither: C", Reason: "r"})
	if err != nil || a.Text != "neither: C" || a.Choice != "" || a.Reason != "r" {
		t.Fatalf("answer = %+v, %v", a, err)
	}
	_, err = ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Seen: true})
	mustErr(t, err, "seen")
}

func TestParseAnswerBlockedSeenOrText(t *testing.T) {
	b := pigtail.Wait{ID: "wait_b", Type: "blocked", Reason: "/login"}
	if a, err := ParseAnswer(b, pigtail.WaitAnswerRequest{Seen: true}); err != nil || !a.Seen {
		t.Fatalf("seen = %+v, %v", a, err)
	}
	if a, err := ParseAnswer(b, pigtail.WaitAnswerRequest{Text: "logged in"}); err != nil || a.Text != "logged in" {
		t.Fatalf("text = %+v, %v", a, err)
	}
	_, err := ParseAnswer(b, pigtail.WaitAnswerRequest{Choice: "A"})
	mustErr(t, err, "choice")
	r := pigtail.Wait{ID: "wait_rp", Type: "review", Details: &pigtail.WaitDetails{URL: "https://x.test/pr/1", Lead: "read it"}}
	if a, err := ParseAnswer(r, pigtail.WaitAnswerRequest{Seen: true}); err != nil || !a.Seen {
		t.Fatalf("review report seen = %+v, %v", a, err)
	}
	_, err = ParseAnswer(r, pigtail.WaitAnswerRequest{Items: []string{"1=ok"}})
	mustErr(t, err, "items")
	_, err = ParseAnswer(reviewWait(1), pigtail.WaitAnswerRequest{Seen: true})
	mustErr(t, err, "seen")
}

func TestParseAnswerRefusesDoneWrappedWorking(t *testing.T) {
	withDoneCommand(t, "")
	for _, typ := range []string{"done", "wrapped"} {
		_, err := ParseAnswer(pigtail.Wait{ID: "wait_d", Type: typ}, pigtail.WaitAnswerRequest{Seen: true})
		mustErr(t, err, "a "+typ+" wait takes no answer: the session is over — close it")
		if strings.Contains(err.Error(), " with ") {
			t.Fatalf("%s: names a command with none set: %v", typ, err)
		}
	}
	_, err := ParseAnswer(pigtail.Wait{ID: "wait_w", Type: "working"}, pigtail.WaitAnswerRequest{Seen: true})
	mustErr(t, err, "agent's turn")
}

func TestParseAnswerRefusesSecretInText(t *testing.T) {
	_, err := ParseAnswer(pigtail.Wait{ID: "wait_b", Type: "blocked"}, pigtail.WaitAnswerRequest{Text: "token " + ghToken})
	mustErr(t, err, "text")
	_, err = ParseAnswer(pigtail.Wait{ID: "wait_b", Type: "blocked"}, pigtail.WaitAnswerRequest{Seen: true, Reason: ghToken})
	mustErr(t, err, "reason")
}

func TestParseAnswerRefusesSecretInNote(t *testing.T) {
	_, err := ParseAnswer(reviewWait(2), pigtail.WaitAnswerRequest{Items: []string{"1=ok", "2=ko:" + ghToken}})
	mustErr(t, err, "items[2].note")
}

func TestRenderAnswerEnglish(t *testing.T) {
	w := arbitrageWait()
	a, err := ParseAnswer(w, pigtail.WaitAnswerRequest{Choice: "reco", Reason: "cheaper to undo"})
	if err != nil {
		t.Fatal(err)
	}
	want := "Answer from Ada to your wait arbitrage « Sidebar order » (wait_1): A « Folders first »\nreason=cheaper to undo"
	if got := RenderAnswer(w, a, "Ada"); got != want {
		t.Fatalf("body\n got %q\nwant %q", got, want)
	}
	r := reviewWait(3)
	a, _ = ParseAnswer(r, pigtail.WaitAnswerRequest{Items: []string{"1=ok", "2=ko:hidden", "3=ok"}})
	if got, want := RenderAnswer(r, a, "Ada"), "Answer from Ada to your wait review « Checklist » (wait_r): 2 ok, 1 ko — 2: hidden"; got != want {
		t.Fatalf("review body\n got %q\nwant %q", got, want)
	}
	b := pigtail.Wait{ID: "wait_b", Type: "blocked"}
	a, _ = ParseAnswer(b, pigtail.WaitAnswerRequest{Seen: true})
	if got, want := RenderAnswer(b, a, "Ada"), "Answer from Ada to your wait blocked (wait_b): seen"; got != want {
		t.Fatalf("blocked body\n got %q\nwant %q", got, want)
	}
}

func TestRenderAnswerDefaultName(t *testing.T) {
	b := pigtail.Wait{ID: "wait_b", Type: "blocked"}
	a, _ := ParseAnswer(b, pigtail.WaitAnswerRequest{Seen: true})
	if got, want := RenderAnswer(b, a, ""), "Answer from the user to your wait blocked (wait_b): seen"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderAnswerClipsToBody(t *testing.T) {
	r := reviewWait(30)
	var items []string
	for i := 1; i <= 30; i++ {
		items = append(items, fmt.Sprintf("%d=ko:%s", i, strings.Repeat("é", 100))) // 200 bytes
	}
	a, err := ParseAnswer(r, pigtail.WaitAnswerRequest{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	body := RenderAnswer(r, a, "Ada")
	if len(body) > relay.MaxBodyBytes {
		t.Fatalf("body is %d bytes, the limit is %d", len(body), relay.MaxBodyBytes)
	}
	if !strings.Contains(body, "pigtail wait show wait_r") || !strings.Contains(body, "…") {
		t.Fatalf("clipped body lacks its pointer:\n%s", body)
	}
	if !strings.HasPrefix(body, "Answer from Ada to your wait review") {
		t.Fatalf("clipped body lost its head:\n%s", body)
	}
	if relay.CheckBody(body) != nil {
		t.Fatalf("the clipped body is refused by the relay: %v", relay.CheckBody(body))
	}
}

// TestParseAnswerUsesAnswerable: ParseAnswer takes an answer exactly for the
// types waittype.Answerable names.
func TestParseAnswerUsesAnswerable(t *testing.T) {
	for _, typ := range waittype.Declarable {
		w := pigtail.Wait{ID: "wait_x", Type: string(typ)}
		req := pigtail.WaitAnswerRequest{Text: "ok"}
		if typ == waittype.Arbitrage {
			w = arbitrageWait()
		}
		_, err := ParseAnswer(w, req)
		if got := err == nil; got != waittype.Answerable(typ) {
			t.Errorf("%s: accepted = %v (err %v), Answerable = %v", typ, got, err, waittype.Answerable(typ))
		}
	}
}

// TestParseAnswerEnvelope: the answer carries its envelope (D28 b, c).
func TestParseAnswerEnvelope(t *testing.T) {
	a, err := ParseAnswer(arbitrageWait(), pigtail.WaitAnswerRequest{Choice: "reco"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "arbitrage" || a.Schema != pigtail.WaitAnswerSchema {
		t.Fatalf("envelope = %q/%d", a.Kind, a.Schema)
	}
}

// withDoneCommand sets the user's done command for one test (process state:
// serial only).
func withDoneCommand(t *testing.T, cmd string) {
	t.Helper()
	testutil.SerialOnly(t)
	prev := waittype.Configured()
	c := prev
	c.Done = cmd
	waittype.Configure(c)
	t.Cleanup(func() { waittype.Configure(prev) })
}

// With a done command set, the refusal of a done or wrapped answer names it.
func TestParseAnswerDoneNamesConfiguredCommand(t *testing.T) {
	withDoneCommand(t, "/finish")
	for _, typ := range []string{"done", "wrapped"} {
		_, err := ParseAnswer(pigtail.Wait{ID: "wait_d", Type: typ}, pigtail.WaitAnswerRequest{Seen: true})
		mustErr(t, err, "close it with /finish")
	}
}
