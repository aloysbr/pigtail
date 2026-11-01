package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/suggest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// suggestFixture seeds one work theme (thm_t) and one perso theme (thm_p),
// no subject.
func suggestFixture(t *testing.T) (*chi.Mux, *store.Store) {
	t.Helper()
	r, st, _ := newTestRouterSvc(t)
	now := time.UnixMilli(1).UTC()
	for _, th := range []*pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: now},
		{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: now},
	} {
		if err := st.CreateTheme(th); err != nil {
			t.Fatal(err)
		}
	}
	return r, st
}

var suggestLogSeq int

func addSuggestSubject(t *testing.T, st *store.Store, id, theme, title, status string, logs ...[2]string) {
	t.Helper()
	testutil.SerialOnly(t) // bumps suggestLogSeq
	now := time.UnixMilli(1).UTC()
	if err := st.CreateSubject(&pigtail.Subject{ID: id, ThemeID: theme, Title: title, Slug: id, Status: status, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		suggestLogSeq++
		e := &pigtail.SubjectLogEntry{ID: "log_" + id + "_" + strconv.Itoa(suggestLogSeq),
			SubjectID: id, Timestamp: time.UnixMilli(int64(suggestLogSeq)).UTC(), Kind: l[0], Text: l[1]}
		if err := st.AppendLog(e); err != nil {
			t.Fatal(err)
		}
	}
}

func declareRow(t *testing.T, st *store.Store, ws, scope, reason string) {
	t.Helper()
	if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: scope, Reason: reason, DeclaredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func suggestAs(t *testing.T, r http.Handler, c caller, req pigtail.SuggestRequest) (*httptest.ResponseRecorder, pigtail.SuggestResult) {
	t.Helper()
	rec := doAs(t, r, c, "POST", "/subjects/suggest", req)
	var res pigtail.SuggestResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
	return rec, res
}

func strongOf(res pigtail.SuggestResult) []string {
	var out []string
	for _, c := range res.Candidates {
		if c.Strength == "strong" {
			out = append(out, c.SubjectID)
		}
	}
	return out
}

func candidateOf(res pigtail.SuggestResult, id string) (pigtail.SuggestCandidate, bool) {
	for _, c := range res.Candidates {
		if c.SubjectID == id {
			return c, true
		}
	}
	return pigtail.SuggestCandidate{}, false
}

// seedTiers9225 seeds the real shape of #9225 in the work scope: sub_a owns it
// (delivery), five subjects cite it in their phase/progress/handoff lines.
func seedTiers9225(t *testing.T, st *store.Store) {
	t.Helper()
	addSuggestSubject(t, st, "sub_a", "thm_t", "Wind vane housing", pigtail.StatusBuilding,
		[2]string{"delivery", "globex/monorepo#9225 merged · wind vane housing"},
		[2]string{"phase", "draft PR globex/monorepo#9225"},
		[2]string{"handoff", "état:\nworktree feat/wind-vane-housing, PR monorepo#9225"},
		[2]string{"message", "as #9225 did"})
	addSuggestSubject(t, st, "sub_b", "thm_t", "Badge", pigtail.StatusBuilding,
		[2]string{"phase", "keeps badge prop for PR #9225"}, [2]string{"handoff", "waiting on #9225"})
	addSuggestSubject(t, st, "sub_c", "thm_t", "Gate", pigtail.StatusBuilding,
		[2]string{"progress", "gate x-after-9225 (fusion de monorepo#9225)"})
	addSuggestSubject(t, st, "sub_d", "thm_t", "Row counters", pigtail.StatusBuilding,
		[2]string{"phase", "#9225 padding override"}, [2]string{"handoff", "branch feat/row-counters, rebased on #9225"})
	addSuggestSubject(t, st, "sub_e", "thm_t", "Row padding", pigtail.StatusBuilding,
		[2]string{"phase", "#9225 newsfeed row padding"}, [2]string{"progress", "monorepo#9225 sets the padding"})
	addSuggestSubject(t, st, "sub_f", "thm_t", "Regression window", pigtail.StatusBuilding,
		[2]string{"phase", "regression window (PR #9225 newsfeed look&feel)"})
}

func TestSuggestRouteNotShadowed(t *testing.T) {
	t.Parallel()
	r, _ := suggestFixture(t)
	rec, _ := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "hello"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"candidates":[]`) {
		t.Fatalf("POST /subjects/suggest = %d %s, want 200 with candidates:[]", rec.Code, rec.Body)
	}
}

func TestSuggestAutoUnique(t *testing.T) {
	r, st := suggestFixture(t)
	seedTiers9225(t, st)
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225"})
	if res.Auto != "sub_a" || len(strongOf(res)) != 1 {
		t.Fatalf("got %+v, want auto sub_a, one strong", res)
	}
	c, _ := candidateOf(res, "sub_a")
	if !c.Writable || c.PR != 9225 || c.Signal != "pr" || c.ShortTitle != "Wind vane housing" {
		t.Fatalf("candidate %+v", c)
	}
}

func TestSuggestNoAutoTwoStrong(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_u", "thm_t", "Unified sensor panel", pigtail.StatusBuilding,
		[2]string{"phase", "PR #6286 draft"}, [2]string{"delivery", "globex-workspace#6286 merged"})
	addSuggestSubject(t, st, "sub_h", "thm_t", "Gauge hierarchy tab removal", pigtail.StatusDone,
		[2]string{"delivery", "globex-workspace#6307 merged"})
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "PR 6286/6307"})
	if res.Auto != "" || len(strongOf(res)) != 2 {
		t.Fatalf("got %+v, want two strong, no auto", res)
	}
}

func TestSuggestNoAutoUnwritableStrong(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_pp", "thm_p", "Perso branch work", pigtail.StatusBuilding,
		[2]string{"phase", "implement on feat/p-branch"})
	addSuggestSubject(t, st, "sub_tt", "thm_t", "Work PR work", pigtail.StatusBuilding,
		[2]string{"delivery", "monorepo#9225 merged"})

	_, res := suggestAs(t, r, perso, pigtail.SuggestRequest{Text: "fix #9225", Branch: "feat/p-branch"})
	if len(strongOf(res)) != 2 || res.Auto != "" {
		t.Fatalf("both: got %+v, want two strong, no auto", res)
	}
	if c, _ := candidateOf(res, "sub_tt"); c.Writable {
		t.Fatalf("work subject writable from perso: %+v", c)
	}
	_, res = suggestAs(t, r, perso, pigtail.SuggestRequest{Text: "fix #9225"})
	if len(strongOf(res)) != 1 || res.Auto != "" {
		t.Fatalf("T alone: got %+v, want no auto", res)
	}
	_, res = suggestAs(t, r, perso, pigtail.SuggestRequest{Branch: "feat/p-branch"})
	if res.Auto != "sub_pp" {
		t.Fatalf("P alone: got %+v, want auto sub_pp", res)
	}
}

func TestSuggestScopePartition(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_secret", "thm_p", "Secret perso title", pigtail.StatusBuilding,
		[2]string{"delivery", "aloysbr/pigtail#9225 merged"})
	rec, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225 secret perso"})
	if len(res.Candidates) != 0 || res.Elsewhere.Strong != 1 || res.Auto != "" {
		t.Fatalf("work: got %+v, want no candidate, elsewhere.strong 1", res)
	}
	if b := rec.Body.String(); strings.Contains(b, "sub_secret") || strings.Contains(b, "Secret perso") {
		t.Fatalf("work body leaks the perso subject: %s", b)
	}

	r, st = suggestFixture(t)
	addSuggestSubject(t, st, "sub_work", "thm_t", "Work owner", pigtail.StatusBuilding,
		[2]string{"delivery", "monorepo#9225 merged"})
	_, res = suggestAs(t, r, perso, pigtail.SuggestRequest{Text: "fix #9225"})
	c, ok := candidateOf(res, "sub_work")
	if !ok || c.Writable || res.Auto != "" || c.Strength != "strong" {
		t.Fatalf("perso: got %+v, want sub_work strong, writable false, no auto", res)
	}
}

func TestSuggestOffTopicReason(t *testing.T) {
	r, st := suggestFixture(t)
	seedTiers9225(t, st)
	declareRow(t, st, cmuxWS, "globex", "polish follow-up of merged #9225")
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "continue", WorkspaceID: cmuxWS})
	if s := strongOf(res); len(s) != 1 || s[0] != "sub_a" {
		t.Fatalf("got %+v, want sub_a strong from the off-topic reason", res)
	}
}

func TestSuggestDeclinedNotAuto(t *testing.T) {
	r, st := suggestFixture(t)
	seedTiers9225(t, st)
	addSuggestSubject(t, st, "sub_bb", "thm_t", "Branch subject", pigtail.StatusBuilding,
		[2]string{"phase", "implement on feat/bb-branch"})
	declareRow(t, st, cmuxWS, "globex", "délie : sub_a refusé")
	declareRow(t, st, cmuxWS2, "globex", "Ada a dit délie sur sub_a")

	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225", WorkspaceID: cmuxWS})
	if c, ok := candidateOf(res, "sub_a"); !ok || c.Strength != "strong" || res.Auto != "" {
		t.Fatalf("refused: got %+v, want sub_a strong, no auto", res)
	}
	_, res = suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225", WorkspaceID: cmuxWS, Branch: "feat/bb-branch"})
	if res.Auto != "sub_bb" {
		t.Fatalf("refused + another strong: got %+v, want auto sub_bb", res)
	}
	_, res = suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225"})
	if res.Auto != "sub_a" {
		t.Fatalf("no workspace: got %+v, want auto sub_a", res)
	}
	_, res = suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225", WorkspaceID: cmuxWS2})
	if res.Auto != "sub_a" {
		t.Fatalf("cited without refusé: got %+v, want auto sub_a", res)
	}
}

func TestSuggestWorkspaceLowercase(t *testing.T) {
	r, st := suggestFixture(t)
	seedTiers9225(t, st)
	declareRow(t, st, cmuxWS, "globex", "polish follow-up of merged #9225")
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "continue", WorkspaceID: strings.ToLower(cmuxWS)})
	if s := strongOf(res); len(s) != 1 || s[0] != "sub_a" {
		t.Fatalf("lowercase uuid: got %+v, want sub_a strong", res)
	}
	for _, ws := range []string{"workspace:3", "pas-un-uuid"} {
		if rec, _ := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "continue", WorkspaceID: ws}); rec.Code != http.StatusBadRequest {
			t.Fatalf("workspace %q = %d %s, want 400", ws, rec.Code, rec.Body)
		}
	}
}

func TestSuggestDeclinedScope(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_hidden", "thm_p", "Hidden perso owner", pigtail.StatusBuilding,
		[2]string{"delivery", "aloysbr/pigtail#9225 merged"})
	declareRow(t, st, cmuxWS, "globex", "délie : sub_hidden refusé")
	_, base := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225"})
	rec, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225", WorkspaceID: cmuxWS})
	if b := rec.Body.String(); strings.Contains(b, "sub_hidden") || strings.Contains(b, "Hidden perso") {
		t.Fatalf("body leaks the refused perso subject: %s", b)
	}
	if res.Elsewhere != base.Elsewhere || len(res.Candidates) != 0 {
		t.Fatalf("got %+v, want elsewhere %+v unchanged, no candidate", res, base.Elsewhere)
	}
}

func TestSuggestOffTopicRowOtherScope(t *testing.T) {
	r, st := suggestFixture(t)
	seedTiers9225(t, st)
	declareRow(t, st, cmuxWS, "perso", "polish follow-up of merged #9225 — délie : sub_a refusé")
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "continue", WorkspaceID: cmuxWS})
	if len(res.Candidates) != 0 {
		t.Fatalf("unreadable row's reason was read: %+v", res)
	}
	_, res = suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #9225", WorkspaceID: cmuxWS})
	if res.Auto != "sub_a" {
		t.Fatalf("unreadable row's refusal was read: %+v", res)
	}
}

func TestSuggestBadRequest(t *testing.T) {
	t.Parallel()
	r, _ := suggestFixture(t)
	req := httptest.NewRequest("POST", "/subjects/suggest", strings.NewReader(""))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body = %d, want 400", rec.Code)
	}
	req = httptest.NewRequest("POST", "/subjects/suggest", strings.NewReader("{not json"))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d, want 400", rec.Code)
	}
	if rec, _ := suggestAs(t, r, work, pigtail.SuggestRequest{WorkspaceID: cmuxWS}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no text, cwd nor branch = %d, want 400", rec.Code)
	}
}

func TestSuggestTextTruncatedAndLimit(t *testing.T) {
	r, st := suggestFixture(t)
	seedTiers9225(t, st)
	// The reference past 4096 bytes is not read.
	long := strings.Repeat("é", 2100) + " fix #9225"
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: long})
	if len(res.Candidates) != 0 {
		t.Fatalf("text not cut at 4096 bytes: %+v", res)
	}
	// limit out of 1..10 falls back to 3; a valid limit truncates.
	r, st = suggestFixture(t)
	for _, id := range []string{"sub_1", "sub_2", "sub_3", "sub_4", "sub_5"} {
		addSuggestSubject(t, st, id, "thm_t", "Owner "+id, pigtail.StatusBuilding, [2]string{"phase", "PR #9225"})
	}
	for _, c := range []struct{ limit, want int }{{0, 3}, {50, 3}, {2, 2}, {10, 5}} {
		_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "#9225", Limit: c.limit})
		if len(res.Candidates) != c.want {
			t.Errorf("limit %d: %d candidates, want %d", c.limit, len(res.Candidates), c.want)
		}
	}
}

type replayFixture struct {
	Themes []struct {
		ID, Name, Kind string
		VaultLink      string `json:"vault_link"`
	} `json:"themes"`
	Subjects []struct {
		ID, Theme, Title, Status string
		Logs                     [][2]string `json:"logs"`
	} `json:"subjects"`
	Cases []struct {
		Reason, Caller, Expect string
	} `json:"cases"`
}

// TestSuggestReplay (criterion 1) replays synthetic reasons shaped like the
// real off-topic declarations of 2026-10-15..19 against a fixture world.
// Expect: strong:<id>[,<id>] (exactly these strong), weak-pr:<id>, weak-any,
// none (no strong; weak=<n> logged), elsewhere-weak.
func TestSuggestReplay(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "suggest_replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx replayFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	r, st, _ := newTestRouterSvc(t)
	now := time.UnixMilli(1).UTC()
	for _, th := range fx.Themes {
		if err := st.CreateTheme(&pigtail.Theme{ID: th.ID, Name: th.Name, Kind: th.Kind, VaultLink: th.VaultLink, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range fx.Subjects {
		addSuggestSubject(t, st, s.ID, s.Theme, s.Title, s.Status, s.Logs...)
	}
	for _, c := range fx.Cases {
		t.Run(c.Caller+"/"+c.Reason, func(t *testing.T) {
			rec, res := suggestAs(t, r, caller{scope: c.Caller}, pigtail.SuggestRequest{Text: c.Reason, Limit: 10})
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			strong := strongOf(res)
			sort.Strings(strong)
			weak := len(res.Candidates) - len(strong)
			kind, arg, _ := strings.Cut(c.Expect, ":")
			switch kind {
			case "strong":
				want := strings.Split(arg, ",")
				sort.Strings(want)
				if strings.Join(strong, ",") != strings.Join(want, ",") {
					t.Fatalf("strong %v, want %v (%+v)", strong, want, res)
				}
			case "weak-pr":
				cand, ok := candidateOf(res, arg)
				if !ok || cand.Signal != "pr" || cand.Strength != "weak" || len(strong) != 0 {
					t.Fatalf("want %s pr/weak, no strong: %+v", arg, res)
				}
			case "weak-any":
				if weak == 0 || len(strong) != 0 {
					t.Fatalf("want weak candidates only: %+v", res)
				}
			case "none":
				if len(strong) != 0 || res.Elsewhere.Strong != 0 {
					t.Fatalf("want no strong: %+v", res)
				}
				t.Logf("weak=%d", weak)
			case "elsewhere-weak":
				if len(res.Candidates) != 0 || res.Elsewhere.Weak == 0 {
					t.Fatalf("want elsewhere.weak only: %+v", res)
				}
			default:
				t.Fatalf("unknown expectation %q", c.Expect)
			}
		})
	}
}

const offTopicSummaryMark = "déclaré hors sujet : "

// TestSuggestReplayLive replays the real off-topic reasons (a JSON export of
// `pigtail journal list --kind workspace-off-topic --json`) against a copy of
// pigtail.db. Skipped unless PIGTAIL_SUGGEST_REPLAY_DB and
// PIGTAIL_SUGGEST_REPLAY_JOURNAL are set; PIGTAIL_SUGGEST_REPLAY_SCOPE is the
// caller (default perso). A reason is never printed (it may name a
// colleague): only its sha8 and its PR references.
func TestSuggestReplayLive(t *testing.T) {
	t.Parallel()
	dbPath, journal := os.Getenv("PIGTAIL_SUGGEST_REPLAY_DB"), os.Getenv("PIGTAIL_SUGGEST_REPLAY_JOURNAL")
	if dbPath == "" || journal == "" {
		t.Skip("PIGTAIL_SUGGEST_REPLAY_DB and PIGTAIL_SUGGEST_REPLAY_JOURNAL not set")
	}
	who := os.Getenv("PIGTAIL_SUGGEST_REPLAY_SCOPE")
	if who == "" {
		who = "perso"
	}
	b, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	var entries []pigtail.JournalEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	r, st, _ := newTestRouterSvcAt(t, dbPath)
	callerSc, err := scope.FromHeader(who)
	if err != nil {
		t.Fatal(err)
	}
	all, err := st.ListSubjects(store.SubjectFilter{Scope: scope.Any})
	if err != nil {
		t.Fatal(err)
	}
	replayed, noSubject, weakNoSubject := 0, 0, 0
	for _, e := range entries {
		_, reason, ok := strings.Cut(e.Summary, offTopicSummaryMark)
		if !ok || strings.TrimSpace(reason) == "" {
			continue
		}
		replayed++
		sum := sha256.Sum256([]byte(reason))
		refs := suggest.ExtractPRRefs(reason)
		var refTxt []string
		shortBareOnly := len(refs) > 0
		for _, ref := range refs {
			if ref.Repo != "" {
				refTxt = append(refTxt, ref.Repo+"#"+strconv.Itoa(ref.Number))
			} else {
				refTxt = append(refTxt, "#"+strconv.Itoa(ref.Number))
			}
			if ref.Repo != "" || ref.Number >= 1000 {
				shortBareOnly = false
			}
		}
		start := time.Now()
		rec, res := suggestAs(t, r, caller{scope: who}, pigtail.SuggestRequest{Text: reason, Limit: 10})
		took := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Errorf("r:%x status %d", sum[:4], rec.Code)
			continue
		}
		strong := 0
		for _, c := range res.Candidates {
			if c.Strength == "strong" {
				strong++
			}
		}
		line := e.Timestamp.Format("2006-01-02T15:04") + " " + replayLine(sum, refTxt, res, took)
		if len(refs) == 0 && strong == 0 && res.Elsewhere.Strong == 0 {
			noSubject++
			weakNoSubject += len(res.Candidates)
			line += fmt.Sprintf(" weak=%d", len(res.Candidates))
		}
		t.Log(line)
		if took > 300*time.Millisecond {
			t.Errorf("r:%x took %s > 300ms", sum[:4], took)
		}
		// A PR reference finds its owner — strong here, or strong in a scope
		// the caller does not read. A bare reference under 4 digits without a
		// repo is a question (pr/weak) by rule: exempt.
		if len(refs) > 0 && !shortBareOnly && strong == 0 && res.Elsewhere.Strong == 0 {
			t.Errorf("r:%x refs:%s: no strong candidate", sum[:4], strings.Join(refTxt, ","))
		}
		for _, id := range hiddenLeaks(rec.Body.String(), all, callerSc) {
			t.Errorf("r:%x: a subject of a scope %s may not read leaks (%s)", sum[:4], who, id)
		}
	}
	t.Logf("replayed %d reasons as %s", replayed, who)
	t.Logf("weak sur raisons sans sujet : %d/%d", weakNoSubject, noSubject)
	if replayed == 0 {
		t.Fatal("no reason replayed: the journal export holds no off-topic reason")
	}
}

// replayLine is one reason of the live replay, pseudonymised: the reason's
// sha8, its PR references, then per candidate its id and signal/strength
// only — never a title nor the reason's text (GDPR, plan contract) — the
// strong count and ids, the weak count, elsewhere, auto and the timing.
func replayLine(sum [32]byte, refs []string, res pigtail.SuggestResult, took time.Duration) string {
	var cands, strong []string
	weak := 0
	for _, c := range res.Candidates {
		cands = append(cands, c.SubjectID+" "+c.Signal+"/"+c.Strength)
		if c.Strength == "strong" {
			strong = append(strong, c.SubjectID)
		} else {
			weak++
		}
	}
	return fmt.Sprintf("r:%x refs:%s strong:%d[%s] weak:%d → [%s] elsewhere:%d/%d auto:%s %s",
		sum[:4], strings.Join(refs, ","), len(strong), strings.Join(strong, ","), weak, strings.Join(cands, "; "),
		res.Elsewhere.Strong, res.Elsewhere.Weak, res.Auto, took.Round(time.Millisecond))
}

// hiddenLeaks returns the ids of the subjects the caller may not read that a
// suggest response body names: by id, or by a title of 8 runes or more that
// no subject the caller reads also holds (a visible twin's title in the body
// is the twin's, not a leak). The id check stays strict.
func hiddenLeaks(body string, all []pigtail.Subject, caller scope.Scope) []string {
	visibleTitle := map[string]bool{}
	for _, s := range all {
		if scope.CanRead(caller, scope.Scope(s.Scope)) {
			visibleTitle[s.Title] = true
		}
	}
	var out []string
	for _, s := range all {
		if scope.CanRead(caller, scope.Scope(s.Scope)) {
			continue
		}
		titleLeak := utf8.RuneCountInString(s.Title) >= 8 && !visibleTitle[s.Title] && strings.Contains(body, s.Title)
		if strings.Contains(body, s.ID) || titleLeak {
			out = append(out, s.ID)
		}
	}
	return out
}

// TestReplayHiddenLeaksTwinTitle: a hidden subject whose title is also held
// by a subject the caller reads is no leak when only the visible twin is in
// the body; its id still is, and so is a title no visible subject holds.
func TestReplayHiddenLeaksTwinTitle(t *testing.T) {
	t.Parallel()
	all := []pigtail.Subject{
		{ID: "sub_visibletwin", Title: "Same long title for both", Scope: "globex"},
		{ID: "sub_hiddentwin", Title: "Same long title for both", Scope: "perso"},
		{ID: "sub_hiddenonly", Title: "A title only perso holds", Scope: "perso"},
	}
	caller, err := scope.FromHeader("globex")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"candidates":[{"subject_id":"sub_visibletwin","title":"Same long title for both"}]}`
	if got := hiddenLeaks(body, all, caller); len(got) != 0 {
		t.Fatalf("twin title read as a leak: %v", got)
	}
	if got := hiddenLeaks(body+" sub_hiddentwin", all, caller); len(got) != 1 || got[0] != "sub_hiddentwin" {
		t.Fatalf("hidden id: %v", got)
	}
	if got := hiddenLeaks(body+" A title only perso holds", all, caller); len(got) != 1 || got[0] != "sub_hiddenonly" {
		t.Fatalf("hidden-only title: %v", got)
	}
}

// TestReplayLineNoTitle: the live replay line names candidates by id and
// signal only, carries the strong ids and the weak count, and never a title.
func TestReplayLineNoTitle(t *testing.T) {
	t.Parallel()
	res := pigtail.SuggestResult{Candidates: []pigtail.SuggestCandidate{
		{SubjectID: "sub_a", ShortTitle: "Short title A", Title: "Long title A", Signal: "pr", Strength: "strong"},
		{SubjectID: "sub_b", ShortTitle: "Short title B", Title: "Long title B", Signal: "keyword", Strength: "weak"},
	}}
	line := replayLine(sha256.Sum256([]byte("x")), []string{"aloysbr/pigtail#218"}, res, 12*time.Millisecond)
	for _, bad := range []string{"title", "Title"} {
		if strings.Contains(line, bad) {
			t.Fatalf("line carries a title: %q", line)
		}
	}
	for _, want := range []string{"sub_a pr/strong", "sub_b keyword/weak", "strong:1[sub_a]", "weak:1", "refs:aloysbr/pigtail#218"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q lacks %q", line, want)
		}
	}
}

// subject suggest reads kinds by exact equality: a handoff-auto line that
// names a PR owns nothing (plan task 7a).
func TestSuggestIgnoresHandoffAuto(t *testing.T) {
	r, st := suggestFixture(t)
	addSuggestSubject(t, st, "sub_auto", "thm_t", "Alpha", pigtail.StatusBuilding,
		[2]string{pigtail.LogHandoffAuto, "[automatique]\nPR #4242 flip la PR"})
	addSuggestSubject(t, st, "sub_man", "thm_t", "Beta", pigtail.StatusBuilding,
		[2]string{pigtail.LogHandoff, "PR #4343 flip la PR"})
	_, res := suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #4242"})
	if _, ok := candidateOf(res, "sub_auto"); ok {
		t.Fatalf("a handoff-auto line made a candidate: %+v", res)
	}
	_, res = suggestAs(t, r, work, pigtail.SuggestRequest{Text: "fix #4343"})
	if _, ok := candidateOf(res, "sub_man"); !ok {
		t.Fatalf("control: a manual handoff must make a candidate: %+v", res)
	}
}
