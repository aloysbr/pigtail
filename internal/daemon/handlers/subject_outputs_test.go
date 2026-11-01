package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func outBody(kind, path, url string) map[string]any {
	b := map[string]any{"kind": kind}
	if path != "" {
		b["path"] = path
	}
	if url != "" {
		b["url"] = url
	}
	return b
}

func decodeAdd(t *testing.T, body []byte) pigtail.SubjectOutputAddResult {
	t.Helper()
	var res pigtail.SubjectOutputAddResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return res
}

func storedOutputs(t *testing.T, st *store.Store, subjectID string) []pigtail.SubjectOutput {
	t.Helper()
	out, err := st.ListSubjectOutputs(context.Background(), subjectID, store.OutputFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSubjectOutputAddCreatedThenExisting(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	body := outBody("capture", "/a/c.png", "")
	rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	first := decodeAdd(t, rec.Body.Bytes())
	if !first.Created || first.Output.Seq <= 0 || first.Output.SubjectID != "sub_p" || first.Output.Title != "c.png" {
		t.Fatalf("first = %+v", first)
	}
	rec = doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", body)
	second := decodeAdd(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || second.Created || second.Output.Seq != first.Output.Seq {
		t.Fatalf("second: %d %+v", rec.Code, second)
	}
}

func TestSubjectOutputAddValidation(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	bad := map[string]map[string]any{
		"unknown kind":    outBody("video", "/a/c.png", ""),
		"relative path":   outBody("capture", "c.png", ""),
		"61 runes title":  {"kind": "capture", "path": "/a/c.png", "title": strings.Repeat("é", 61)},
		"meta array":      {"kind": "capture", "path": "/a/c.png", "meta": []int{1}},
		"body subject_id": {"kind": "capture", "path": "/a/c.png", "subject_id": "sub_t"},
	}
	for name, body := range bad {
		rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), string(pigtail.ErrInvalidRequest)) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if out := storedOutputs(t, st, "sub_p"); len(out) != 0 {
		t.Fatalf("refused rows stored: %+v", out)
	}
}

func TestSubjectOutputsScope(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/subjects/sub_p/outputs", outBody("capture", "/a/c.png", "")},
		{"GET", "/subjects/sub_p/outputs", nil},
		{"POST", "/subjects/sub_p/outputs/purge", pigtail.SubjectOutputPurgeRequest{Key: "/a/c.png"}},
	} {
		rec := doAs(t, r, work, c.method, c.path, c.body)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such subject") {
			t.Errorf("work %s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if out := storedOutputs(t, st, "sub_p"); len(out) != 0 {
		t.Fatalf("work caller wrote: %+v", out)
	}
}

func TestSubjectOutputListQuery(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	dir := t.TempDir()
	present := filepath.Join(dir, "present.png")
	gone := filepath.Join(dir, "gone.png")
	for _, p := range []string{present, gone} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var seqs []int64
	for _, b := range []map[string]any{
		{"kind": "capture", "path": present, "session_id": "s1"},
		{"kind": "mockup", "path": gone, "session_id": "s2"},
		{"kind": "pr", "url": "https://github.com/a/b/pull/3", "session_id": "s2"},
	} {
		rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", b)
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body)
		}
		seqs = append(seqs, decodeAdd(t, rec.Body.Bytes()).Output.Seq)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	list := func(q string) []pigtail.SubjectOutput {
		t.Helper()
		rec := doAs(t, r, perso, "GET", "/subjects/sub_p/outputs"+q, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", q, rec.Code, rec.Body)
		}
		var out []pigtail.SubjectOutput
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := list("")
	if len(all) != 3 {
		t.Fatalf("all = %d", len(all))
	}
	for i, want := range []bool{true, false, true} {
		if all[i].Exists == nil || *all[i].Exists != want {
			t.Errorf("row %d (%s) exists = %v, want %v", i, all[i].Key, all[i].Exists, want)
		}
	}
	for q, want := range map[string]int{
		"?since=" + strconv.FormatInt(seqs[0], 10): 2,
		"?kind=mockup": 1,
		"?session=s2":  2,
		"?limit=1":     1,
	} {
		if got := list(q); len(got) != want {
			t.Errorf("%s: %d rows, want %d", q, len(got), want)
		}
	}
	for _, q := range []string{"?limit=0", "?limit=1001", "?kind=video", "?since=-1"} {
		rec := doAs(t, r, perso, "GET", "/subjects/sub_p/outputs"+q, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := doAs(t, r, perso, "GET", "/subjects/sub_p/outputs?limit=0", nil); !strings.Contains(rec.Body.String(), "limit must be 1..1000") {
		t.Errorf("limit message: %s", rec.Body)
	}
	if rec := doAs(t, r, work, "GET", "/subjects/sub_t/outputs", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty list = %d %q, want []", rec.Code, rec.Body)
	}
}

func TestSubjectOutputPurgeRoute(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", outBody("capture", "/a/c.png", "")); rec.Code != 201 {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs/purge", pigtail.SubjectOutputPurgeRequest{Key: "/a/b/../c.png"})
	if rec.Code != http.StatusOK {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if _, ok := raw["purged_at"].(float64); !ok {
		t.Fatalf("purged_at not an integer: %s", rec.Body)
	}
	if _, ok := raw["exists"]; ok {
		t.Fatalf("purge answer carries exists (GET only): %s", rec.Body)
	}
	rec = doAs(t, r, perso, "POST", "/subjects/sub_p/outputs/purge", pigtail.SubjectOutputPurgeRequest{Key: "/nope/x.png"})
	var e struct{ Code, Message string }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != http.StatusNotFound || e.Code != string(pigtail.ErrNotFound) || e.Message != "no such output" {
		t.Fatalf("unknown key: %d %s", rec.Code, rec.Body)
	}
	rec = doAs(t, r, perso, "GET", "/subjects/sub_p/outputs", nil)
	if !strings.Contains(rec.Body.String(), `"purged_at"`) {
		t.Fatalf("purged row not listed: %s", rec.Body)
	}
}

func TestSubjectOutputEventOnlyWhenCreated(t *testing.T) {
	t.Parallel()
	r, _, svc := scopedFixtureSvc(t)
	bus := events.New()
	svc.Bus = bus
	body := outBody("capture", "/a/c.png", "")
	var first pigtail.SubjectOutputAddResult
	for i := 0; i < 2; i++ {
		rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", body)
		if rec.Code >= 300 {
			t.Fatalf("post %d: %d %s", i, rec.Code, rec.Body)
		}
		if i == 0 {
			first = decodeAdd(t, rec.Body.Bytes())
		}
	}
	evs := bus.Replay("", []string{string(pigtail.EventSubjectOutputAdded)})
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(evs), evs)
	}
	e := evs[0]
	data := e.Data
	if e.Scope != "perso" || data["subject_id"] != "sub_p" || data["seq"] != first.Output.Seq ||
		data["id"] != first.Output.ID || data["kind"] != "capture" || data["key"] != "/a/c.png" || len(data) != 5 {
		t.Fatalf("event = %+v", e)
	}
}

func TestGetSubjectOutputStats(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	for _, p := range []string{"/a/1.png", "/a/2.png"} {
		if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", map[string]any{"kind": "capture", "path": p, "ts": 1000}); rec.Code != 201 {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs/purge", pigtail.SubjectOutputPurgeRequest{Key: "/a/2.png"}); rec.Code != 200 {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body)
	}
	var sub map[string]any
	_ = json.Unmarshal(doAs(t, r, perso, "GET", "/subjects/sub_p", nil).Body.Bytes(), &sub)
	if sub["outputs_count"] != float64(1) || sub["last_output_ts"] != float64(1000) {
		t.Fatalf("sub_p = %v", sub)
	}
	sub = nil
	_ = json.Unmarshal(doAs(t, r, work, "GET", "/subjects/sub_t", nil).Body.Bytes(), &sub)
	if v, ok := sub["outputs_count"]; !ok || v != float64(0) {
		t.Fatalf("sub_t outputs_count = %v (present %v), want 0", v, ok)
	}
	if body := doAs(t, r, perso, "GET", "/subjects", nil).Body.String(); strings.Contains(body, "outputs_count") {
		t.Fatalf("GET /subjects carries outputs_count: %s", body)
	}
}

// openAllOutputs fakes an outputs server whose index opens every path: the
// refusals of the real index are proven in internal/daemon
// (TestServedURLOnlyForRowsTheServerOpens).
func openAllOutputs(base string) func() func(string) string {
	return func() func(string) string {
		return func(p string) string { return base + staticsrv.ServedPath(p) }
	}
}

func TestSubjectOutputListServedURL(t *testing.T) {
	r, _, svc := scopedFixtureSvc(t)
	dir := t.TempDir()
	mock := filepath.Join(dir, "proj", "mockup", "t3", "index.html")
	capture := filepath.Join(dir, "shots", "cap 1.png")
	gone := filepath.Join(dir, "gone", "index.html")
	for _, p := range []string{mock, capture, gone} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []map[string]any{
		outBody("mockup", mock, ""),
		outBody("capture", capture, ""),
		outBody("pr", "", "https://github.com/o/r/pull/1"),
		outBody("report", gone, ""),
	} {
		if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", b); rec.Code != http.StatusCreated {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body)
		}
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	list := func() []pigtail.SubjectOutput {
		t.Helper()
		rec := doAs(t, r, perso, "GET", "/subjects/sub_p/outputs", nil)
		var out []pigtail.SubjectOutput
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}
	base := "http://127.0.0.1:7899"
	svc.OutputsURLs = openAllOutputs(base)
	out := list()
	if got, want := out[0].ServedURL, base+filepath.ToSlash(filepath.Dir(mock))+"/"; got != want || !strings.HasSuffix(got, "/t3/") {
		t.Errorf("mockup served_url = %q; want %q", got, want)
	}
	if got := out[1].ServedURL; !strings.HasPrefix(got, base+"/") || !strings.HasSuffix(got, "/shots/cap%201.png") {
		t.Errorf("capture served_url = %q; want the escaped file URL", got)
	}
	if out[1].Path != capture {
		t.Errorf("path rewritten: %q", out[1].Path)
	}
	if out[2].ServedURL != "" || out[2].URL != "https://github.com/o/r/pull/1" {
		t.Errorf("pr = served %q url %q; want no served_url, url untouched", out[2].ServedURL, out[2].URL)
	}
	if out[3].ServedURL != "" {
		t.Errorf("missing file served_url = %q; want none", out[3].ServedURL)
	}
	svc.OutputsURLs = func() func(string) string { return nil }
	for i, o := range list() {
		if o.ServedURL != "" {
			t.Errorf("row %d served_url = %q with the listener off; want none", i, o.ServedURL)
		}
	}
	rec := doAs(t, r, perso, "GET", "/subjects/sub_p/outputs", nil)
	if strings.Contains(rec.Body.String(), "served_url") {
		t.Errorf("served_url present with the listener off: %s", rec.Body)
	}
}

func TestSubjectOutputAddIgnoresServedURL(t *testing.T) {
	r, st, svc := scopedFixtureSvc(t)
	svc.OutputsURLs = openAllOutputs("http://127.0.0.1:7899")
	body := map[string]any{"kind": "capture", "path": "/a/c.png", "served_url": "http://evil.example/x"}
	rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if res := decodeAdd(t, rec.Body.Bytes()); res.Output.ServedURL != "" {
		t.Fatalf("add answer served_url = %q; want none (server field)", res.Output.ServedURL)
	}
	if strings.Contains(rec.Body.String(), "evil.example") {
		t.Fatalf("add answer echoes the body's served_url: %s", rec.Body)
	}
	for _, o := range storedOutputs(t, st, "sub_p") {
		if o.ServedURL != "" {
			t.Fatalf("stored served_url = %q", o.ServedURL)
		}
	}
}

func getGroups(t *testing.T, r http.Handler, c caller, id string) (int, []pigtail.OutputGroup, string) {
	t.Helper()
	rec := doAs(t, r, c, "GET", "/subjects/"+id+"/outputs/groups", nil)
	var g []pigtail.OutputGroup
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
	return rec.Code, g, rec.Body.String()
}

func TestSubjectOutputGroupsRouteScope(t *testing.T) {
	r, _ := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", outBody("mockup", "/zqx/slug/mockup/t1/index.html", "")); rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	code, groups, body := getGroups(t, r, perso, "sub_p")
	if code != http.StatusOK || len(groups) != 1 || groups[0].Name != "slug" {
		t.Fatalf("perso groups = %d %s", code, body)
	}
	hidden := doAs(t, r, work, "GET", "/subjects/sub_p/outputs/groups", nil)
	unknown := doAs(t, r, work, "GET", "/subjects/sub_nope/outputs/groups", nil)
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("work reads perso groups = %d %s; unknown = %s", hidden.Code, hidden.Body, unknown.Body)
	}
	if strings.Contains(hidden.Body.String(), "slug") {
		t.Fatalf("work answer names the perso group: %s", hidden.Body)
	}
	if code, groups, _ := getGroups(t, r, work, "sub_t"); code != http.StatusOK || groups == nil || len(groups) != 0 {
		t.Fatalf("empty groups = %d %v; want 200 []", code, groups)
	}
}

func TestSubjectOutputGroupsMissingTour(t *testing.T) {
	r, _, svc := scopedFixtureSvc(t)
	svc.OutputsURLs = openAllOutputs("http://127.0.0.1:7899")
	dir := t.TempDir()
	t1 := filepath.Join(dir, "slug", "mockup", "t1", "index.html")
	t2 := filepath.Join(dir, "slug", "mockup", "t2", "index.html")
	for _, p := range []string{t1, t2} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if rec := doAs(t, r, perso, "POST", "/subjects/sub_p/outputs", outBody("mockup", p, "")); rec.Code != http.StatusCreated {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body)
		}
	}
	if err := os.RemoveAll(filepath.Dir(t1)); err != nil {
		t.Fatal(err)
	}
	code, groups, body := getGroups(t, r, perso, "sub_p")
	if code != http.StatusOK || len(groups) != 1 || len(groups[0].Tours) != 2 {
		t.Fatalf("groups = %d %s", code, body)
	}
	gone, here := groups[0].Tours[0], groups[0].Tours[1]
	if gone.N != 1 || gone.Exists || gone.ServedURL != "" {
		t.Errorf("missing tour = %+v; want exists=false, no served_url", gone)
	}
	if here.N != 2 || !here.Exists || !strings.HasSuffix(here.ServedURL, "/t2/") {
		t.Errorf("present tour = %+v; want exists, served_url …/t2/", here)
	}
}
