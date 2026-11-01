package suggest

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestExtractPRRefs(t *testing.T) {
	cases := []struct {
		text string
		want []PRRef
	}{
		{"polish follow-up of merged #4242 (micro-fix)", []PRRef{{"", 4242}}},
		{"PR 3101/3102", []PRRef{{"", 3101}, {"", 3102}}},
		{"globex/station#4242", []PRRef{{"globex/station", 4242}}},
		{"see https://github.com/aloysbr/pigtail/pull/220 please", []PRRef{{"aloysbr/pigtail", 220}}},
		{"draft PR #4242 and PR 3102", []PRRef{{"", 4242}, {"", 3102}}},
		{"station#4242 merged", []PRRef{{"station", 4242}}},
		{"#1 is too short", nil},
	}
	for _, c := range cases {
		got := ExtractPRRefs(c.text)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ExtractPRRefs(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	for _, r := range ExtractPRRefs("sha a4242f, port :14242, #42420") {
		if r.Number == 4242 {
			t.Errorf("4242 found inside another number: %v", r)
		}
	}
}

func TestTokens(t *testing.T) {
	got := Tokens("Retouche du capteur calibration (schéma succès H1/H2) — D7, 2026, look-feel")
	want := []string{"retouche", "capteur", "calibration", "schema", "succes", "look-feel"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tokens = %v, want %v", got, want)
	}
}

func subj(id, title string) Subject {
	return Subject{ID: id, ShortTitle: title, Title: title, Scope: "globex", Status: "building"}
}

func byID(cs []Candidate) map[string]Candidate {
	m := map[string]Candidate{}
	for _, c := range cs {
		m[c.SubjectID] = c
	}
	return m
}

func strongIDs(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		if c.Strength == "strong" {
			out = append(out, c.SubjectID)
		}
	}
	return out
}

func TestRankPROwner(t *testing.T) {
	subjects := []Subject{subj("A", "Dashboard"), subj("B", "Other thing")}
	logs := []LogLine{
		{"A", "phase", "draft PR globex/station#4242"},
		{"B", "message", "as #4242 did"},
	}
	got := Rank(Query{Text: "fix #4242"}, subjects, logs, nil)
	if len(got) != 1 || got[0].SubjectID != "A" || got[0].Signal != "pr" || got[0].Strength != "strong" {
		t.Fatalf("got %+v, want A alone pr/strong", got)
	}
	if got[0].Evidence != "globex/station#4242" || got[0].PR != 4242 {
		t.Errorf("evidence %q pr %d", got[0].Evidence, got[0].PR)
	}

	subjects = []Subject{subj("A", "Dashboard"), subj("C", "Something")}
	logs = []LogLine{
		{"A", "handoff", "resume #4242 review"},
		{"C", "note", "remember #4242"},
	}
	got = Rank(Query{Text: "fix #4242"}, subjects, logs, nil)
	if ids := strongIDs(got); !reflect.DeepEqual(ids, []string{"A"}) || len(got) != 1 {
		t.Fatalf("handoff owner: got %+v", got)
	}
}

// tiersFixture reproduces the real shape of #4242: one owner, five subjects
// citing it as a dependency or overlap.
func tiersFixture(withDelivery, withBranch bool) ([]Subject, []LogLine) {
	subjects := []Subject{
		subj("A", "Gauge seal mount"), subj("B", "Badge"), subj("C", "Gate"),
		subj("D", "Row counters"), subj("E", "Row padding"), subj("F", "Regression window"),
	}
	var logs []LogLine
	if withDelivery {
		logs = append(logs, LogLine{"A", "delivery", "globex/station#4242 merged · gauge seal mount"})
	}
	logs = append(logs, LogLine{"A", "phase", "draft PR globex/station#4242"})
	if withBranch {
		logs = append(logs, LogLine{"A", "handoff", "état:\nworktree feat/gauge-seal-mount, PR station#4242\nreste: review"})
	} else {
		logs = append(logs, LogLine{"A", "handoff", "état:\nPR station#4242 open\nreste: review"})
	}
	logs = append(logs,
		LogLine{"B", "phase", "keeps badge prop for PR #4242"},
		LogLine{"B", "handoff", "waiting on #4242"},
		LogLine{"B", "note", "see #4242"},
		LogLine{"C", "progress", "gate x-after-4242 (fusion de station#4242)"},
		LogLine{"E", "phase", "#4242 gauge row padding"},
		LogLine{"E", "progress", "station#4242 sets the padding"},
		LogLine{"F", "phase", "regression window (PR #4242 gauge seal&mount)"},
		LogLine{"D", "phase", "#4242 padding override"},
	)
	if withBranch {
		logs = append(logs, LogLine{"D", "handoff", "état:\nbranch feat/row-counters, rebased on #4242 by the other session"})
	} else {
		logs = append(logs, LogLine{"D", "handoff", "état:\nrebased on #4242 by the other session"})
	}
	return subjects, logs
}

func TestRankPRTiers(t *testing.T) {
	subjects, logs := tiersFixture(true, true)
	got := Rank(Query{Text: "fix #4242"}, subjects, logs, nil)
	if len(got) != 1 || got[0].SubjectID != "A" || got[0].Signal != "pr" || got[0].Strength != "strong" || got[0].PR != 4242 {
		t.Fatalf("delivery tier: got %+v, want A alone", got)
	}

	subjects, logs = tiersFixture(false, true)
	got = Rank(Query{Text: "fix #4242"}, subjects, logs, nil)
	if ids := strongIDs(got); !reflect.DeepEqual(ids, []string{"A", "D"}) || len(got) != 2 {
		t.Fatalf("branch tier: got %+v, want A and D", got)
	}

	subjects, logs = tiersFixture(false, false)
	got = Rank(Query{Text: "fix #4242"}, subjects, logs, nil)
	if ids := strongIDs(got); len(ids) != 6 {
		t.Fatalf("rest tier: got %+v, want six strong", got)
	}

	// build/polish (a workflow track) and a docs path are not branches.
	subjects = []Subject{subj("A", "Alpha"), subj("B", "Beta")}
	logs = []LogLine{
		{"A", "handoff", "track build/polish, PR #4242"},
		{"B", "handoff", "plan docs/superpowers/plans/x.md for #4242"},
	}
	got = Rank(Query{Text: "fix #4242"}, subjects, logs, nil)
	if ids := strongIDs(got); !reflect.DeepEqual(ids, []string{"A", "B"}) {
		t.Fatalf("non-branches must stay at tier 3: got %+v", got)
	}
}

func TestRankPRTwoOwners(t *testing.T) {
	subjects := []Subject{subj("U", "Unified sensor panel"), subj("H", "Gauge hierarchy tab removal"), subj("X", "Third")}
	subjects[1].Status = "done"
	logs := []LogLine{
		{"U", "phase", "PR #3101 draft"},
		{"U", "delivery", "station#3101 merged"},
		{"H", "delivery", "station#3102 merged"},
		{"X", "handoff", "depends on #3101"},
	}
	got := Rank(Query{Text: "PR 3101/3102"}, subjects, logs, nil)
	if ids := strongIDs(got); !reflect.DeepEqual(ids, []string{"H", "U"}) || len(got) != 2 {
		t.Fatalf("got %+v, want H and U strong, X absent", got)
	}
}

func TestRankPRDroppedIgnored(t *testing.T) {
	subjects := []Subject{subj("A", "Alpha"), subj("Z", "Zed")}
	subjects[1].Status = "dropped"
	logs := []LogLine{
		{"A", "phase", "PR #4242"},
		{"Z", "delivery", "station#4242 merged"},
	}
	got := Rank(Query{Text: "#4242"}, subjects, logs, nil)
	if ids := strongIDs(got); !reflect.DeepEqual(ids, []string{"A"}) {
		t.Fatalf("got %+v, want A (a dropped subject neither owns nor shadows)", got)
	}
}

func TestRankPRRepoMismatch(t *testing.T) {
	subjects := []Subject{subj("A", "Alpha")}
	logs := []LogLine{{"A", "delivery", "globex/station#4242 merged"}}
	if got := Rank(Query{Text: "other/repo#4242"}, subjects, logs, nil); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
	if got := Rank(Query{Text: "station#4242"}, subjects, logs, nil); len(got) != 1 {
		t.Fatalf("same last segment must match: got %+v", got)
	}
}

func TestRankWorktree(t *testing.T) {
	subjects := []Subject{subj("A", "Alpha"), subj("B", "Beta"), subj("R", "Release")}
	logs := []LogLine{
		{"B", "phase", "implement on feat/qualify-suggest, plan reviewed"},
		{"B", "phase", "merged into main"},
		{"R", "phase", "cut on release"},
	}
	linked := []Linked{{SubjectID: "A", Path: "/dev/pigtail-wt/a"}}

	got := Rank(Query{Cwd: "/dev/pigtail-wt/a/internal"}, subjects, logs, linked)
	if len(got) != 1 || got[0].SubjectID != "A" || got[0].Signal != "worktree" || got[0].Strength != "strong" {
		t.Fatalf("cwd under linked: got %+v", got)
	}
	got = Rank(Query{Cwd: "/dev/pigtail-wt/ab"}, subjects, logs, linked)
	if len(got) != 0 {
		t.Fatalf("sibling path is not under: got %+v", got)
	}
	got = Rank(Query{Branch: "feat/qualify-suggest"}, subjects, logs, linked)
	if len(got) != 1 || got[0].SubjectID != "B" || got[0].Signal != "worktree" || got[0].Strength != "strong" {
		t.Fatalf("branch: got %+v", got)
	}
	if got[0].Evidence != "branche feat/qualify-suggest" {
		t.Errorf("evidence %q", got[0].Evidence)
	}
	if got = Rank(Query{Branch: "feat/qualify"}, subjects, logs, linked); len(got) != 0 {
		t.Fatalf("partial branch name: got %+v", got)
	}
	if got = Rank(Query{Branch: "main"}, subjects, logs, linked); len(got) != 0 {
		t.Fatalf("main: got %+v", got)
	}
	if got = Rank(Query{Branch: "release", DefaultBranch: "release"}, subjects, logs, linked); len(got) != 0 {
		t.Fatalf("default branch: got %+v", got)
	}
}

func TestRankWorktreeMainCheckoutNotStrong(t *testing.T) {
	subjects := []Subject{subj("P", "Pigtail thing"), subj("M", "Tools thing"), subj("W", "Dev thing")}
	logs := []LogLine{
		{"P", "phase", "work in pigtail main checkout"},
		{"M", "progress", "tools updated"},
		{"W", "phase", "cleanup of Developer folder"},
	}
	linked := []Linked{{SubjectID: "P", Path: "/Users/x/Developer/home/pigtail-wt/x"}}
	for _, q := range []Query{
		{Cwd: "/Users/x/Developer/home/pigtail", Branch: "main"},
		{Cwd: "/Users/x/Developer"},
	} {
		for _, c := range Rank(q, subjects, logs, linked) {
			if c.Strength == "strong" {
				t.Errorf("query %+v: strong candidate %+v", q, c)
			}
		}
	}
}

func TestRankBareShortPR(t *testing.T) {
	subjects := []Subject{subj("P", "Alpha"), subj("M", "Beta")}
	logs := []LogLine{
		{"P", "phase", "aloysbr/pigtail#52 opened"},
		{"M", "phase", "tools#52 opened"},
	}
	got := byID(Rank(Query{Text: "#52", Repo: "pigtail"}, subjects, logs, nil))
	if got["P"].Signal != "pr" || got["P"].Strength != "strong" || got["M"].Signal != "pr" || got["M"].Strength != "weak" {
		t.Fatalf("Repo=pigtail: got %+v", got)
	}
	if got["P"].PR != 52 || got["M"].PR != 0 {
		t.Errorf("PR field: P %d M %d", got["P"].PR, got["M"].PR)
	}
	got = byID(Rank(Query{Text: "#52"}, subjects, logs, nil))
	if got["P"].Strength != "weak" || got["M"].Strength != "weak" || len(got) != 2 {
		t.Fatalf("Repo empty: got %+v", got)
	}
	logs = []LogLine{{"P", "phase", "globex/station#4242 opened"}}
	got = byID(Rank(Query{Text: "#4242", Repo: "pigtail"}, subjects, logs, nil))
	if got["P"].Strength != "strong" {
		t.Fatalf("4 digits keeps the general rule: got %+v", got)
	}
}

func keywordFixture() []Subject {
	subjects := []Subject{
		subj("O1", "Calibration · offset"), subj("O2", "Calibration · drift"),
		subj("O3", "Calibration · humidity"), subj("O4", "Calibration · archive"),
	}
	subjects[3].Status = "done"
	for i := 0; i < 30; i++ {
		subjects = append(subjects, subj(fmt.Sprintf("F%02d", i), fmt.Sprintf("Filler topic %c", 'a'+i%26)))
	}
	return subjects
}

func TestRankKeyword(t *testing.T) {
	subjects := keywordFixture()
	got := Rank(Query{Text: "retouche capteur calibration (schema success H1/H2)"}, subjects, nil, nil)
	ids := map[string]bool{}
	for _, c := range got {
		if c.Signal != "keyword" || c.Strength != "weak" {
			t.Errorf("candidate %+v not keyword/weak", c)
		}
		ids[c.SubjectID] = true
	}
	if len(got) != 3 || !ids["O1"] || !ids["O2"] || !ids["O3"] {
		t.Fatalf("got %+v, want O1 O2 O3", got)
	}
	if got := Rank(Query{Text: "question: prévention prompt injection"}, subjects, nil, nil); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestKeywordIDF(t *testing.T) {
	if got, want := idf(34, 4), math.Log(35.0/5.0); math.Abs(got-want) > 1e-9 {
		t.Fatalf("idf(34,4) = %v, want %v", got, want)
	}
	// A word in 10 of 34 subjects alone is not a candidate.
	var subjects []Subject
	for i := 0; i < 34; i++ {
		title := fmt.Sprintf("Plain %c", 'a'+i%26)
		if i < 10 {
			title = "Common " + strings.Repeat("x", i+3)
		}
		subjects = append(subjects, subj(fmt.Sprintf("S%02d", i), title))
	}
	if idf(34, 10) >= KeywordMinScore {
		t.Fatalf("idf(34,10) = %v", idf(34, 10))
	}
	if got := Rank(Query{Text: "common"}, subjects, nil, nil); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestRankMergeAndOrder(t *testing.T) {
	subjects := keywordFixture()
	subjects = append(subjects, subj("N", "Gauge calibration"))
	logs := []LogLine{{"N", "delivery", "station#4242 merged"}}
	got := Rank(Query{Text: "calibration fix #4242"}, subjects, logs, nil)
	if len(got) == 0 || got[0].SubjectID != "N" || got[0].Signal != "pr" || got[0].Strength != "strong" {
		t.Fatalf("got %+v, want N first, pr/strong", got)
	}
	if got[0].Evidence != "station#4242, mots : calibration" {
		t.Errorf("evidence %q", got[0].Evidence)
	}
	n := 0
	for i, c := range got {
		if c.SubjectID == "N" {
			n++
		}
		if i > 0 && c.Strength == "strong" {
			t.Errorf("strong after weak: %+v", got)
		}
	}
	if n != 1 {
		t.Fatalf("N appears %d times", n)
	}
}

func TestSuggestSkipsCaptured(t *testing.T) {
	subjects := []Subject{subj("A", "Alpha"), subj("Z", "Alpha twin")}
	subjects[1].Status = "captured"
	logs := []LogLine{
		{"A", "phase", "PR #4242"},
		{"Z", "delivery", "station#4242 merged"},
	}
	if ids := strongIDs(Rank(Query{Text: "#4242"}, subjects, logs, nil)); !reflect.DeepEqual(ids, []string{"A"}) {
		t.Fatalf("PR signal: got %v, want only A (a captured subject is closed)", ids)
	}
	got := Rank(Query{Text: "alpha twin"}, subjects, nil, nil)
	for _, c := range got {
		if c.SubjectID == "Z" {
			t.Fatalf("keyword signal offered the captured subject: %+v", got)
		}
	}
}

// TestSuggestSkipsClosed: the keyword path offers no closed subject
// (parked, done, dropped, captured); the PR path skips dropped and captured
// but still offers done and parked (a PR reference names its delivered
// subject).
func TestSuggestSkipsClosed(t *testing.T) {
	closed := []string{"parked", "done", "dropped", "captured"}
	var subjects []Subject
	var logs []LogLine
	for i, st := range closed {
		s := subj("S_"+st, "Zephyr gadget "+st)
		s.Status = st
		subjects = append(subjects, s)
		logs = append(logs, LogLine{"S_" + st, "delivery", fmt.Sprintf("station#%d merged", 4300+i)})
	}
	for i := 0; i < 30; i++ {
		subjects = append(subjects, subj(fmt.Sprintf("F%02d", i), fmt.Sprintf("Filler topic %c", 'a'+i%26)))
	}
	for _, st := range closed {
		for _, c := range Rank(Query{Text: "zephyr gadget " + st}, subjects, nil, nil) {
			if c.SubjectID == "S_"+st {
				t.Errorf("keyword path offered the %s subject: %+v", st, c)
			}
		}
	}
	for i, st := range closed {
		got := byID(Rank(Query{Text: fmt.Sprintf("station#%d", 4300+i)}, subjects, logs, nil))
		_, offered := got["S_"+st]
		if want := st == "done" || st == "parked"; offered != want {
			t.Errorf("PR path: %s offered = %v, want %v (%+v)", st, offered, want, got)
		}
	}
}

// prTier counts owner kinds by exact equality: a handoff-auto line naming the
// PR is no ownership (plan task 7a).
func TestPRTierIgnoresHandoffAuto(t *testing.T) {
	q := PRRef{Number: 4242}
	if tier, _ := prTier(q, LogLine{Kind: "handoff-auto", Text: "PR #4242 on branch feat/x"}); tier != 0 {
		t.Fatalf("handoff-auto tier = %d, want 0", tier)
	}
	if tier, _ := prTier(q, LogLine{Kind: "handoff", Text: "PR #4242 on branch feat/x"}); tier == 0 {
		t.Fatal("control: a manual handoff must have a tier")
	}
	if ownerKinds["handoff-auto"] {
		t.Fatal("handoff-auto must not be an owner kind")
	}
}
