package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/focuspri"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// roomySampler reports a machine with plenty of memory and no pressure, so
// only the job ceiling ever holds a request back in these tests.
type roomySampler struct{}

func (roomySampler) ReadMemory() (sysmem.Memory, error) {
	return sysmem.Memory{Available: 60 << 30, Pressure: 1, Total: 64 << 30}, nil
}
func (roomySampler) Procs() (map[int]sysmem.Proc, error) { return map[int]sysmem.Proc{}, nil }

func newAdmissionRouter(t *testing.T, maxJobs int) (*chi.Mux, *admission.Controller, chan struct{}) {
	t.Helper()
	return newAdmissionRouterFP(t, maxJobs, nil)
}

// newAdmissionRouterFP is newAdmissionRouter with a focus resolver (nil = none).
func newAdmissionRouterFP(t *testing.T, maxJobs int, fp FocusResolver) (*chi.Mux, *admission.Controller, chan struct{}) {
	t.Helper()
	notify := make(chan struct{}, 1)
	c := admission.New(events.New(),
		admission.WithConfig(admission.Config{MaxJobs: maxJobs, FloorBytes: 6 << 30, MaxHold: time.Hour}),
		admission.WithSampler(roomySampler{}),
		admission.WithLogger(func(string, ...any) {}),
		admission.WithNotify(func() {
			select {
			case notify <- struct{}{}:
			default:
			}
		}),
	)
	r := chi.NewRouter()
	RegisterAdmissionRoutes(r, &Services{Admission: c, FocusPri: fp}, testResponder{})
	return r, c, notify
}

func waitForAdmission(t *testing.T, c *admission.Controller, notify chan struct{}, what string, cond func(admission.Status) bool) {
	t.Helper()
	if cond(c.Status()) {
		return
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-notify:
			if cond(c.Status()) {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// openLease POSTs a lease request on a real listener and returns a reader on
// the NDJSON stream plus the function that drops the connection.
func openLease(t *testing.T, srv *httptest.Server, body string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/admission/leases", strings.NewReader(body))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req) //nolint:bodyclose // closed by the returned drop func, or below on failure
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("status = %d", resp.StatusCode)
	}
	return bufio.NewReader(resp.Body), func() { cancel(); resp.Body.Close() }
}

func readUpdate(t *testing.T, br *bufio.Reader) pigtail.AdmissionUpdate {
	t.Helper()
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	var env struct {
		Data pigtail.AdmissionUpdate `json:"data"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return env.Data
}

func TestGetAdmissionWithoutController(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	RegisterAdmissionRoutes(r, &Services{}, testResponder{})
	rec := doJSON(t, r, "GET", "/admission", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["max_jobs"]) != "0" || string(raw["leases"]) != "[]" || string(raw["queue"]) != "[]" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAcquireAdmissionDisabled(t *testing.T) {
	t.Parallel()
	r, _, _ := newAdmissionRouter(t, 0)
	rec := doJSON(t, r, "POST", "/admission/leases", pigtail.AdmissionRequest{Group: "wt"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), string(pigtail.ErrAdmissionDisabled)) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "POST", "/admission/leases", pigtail.AdmissionRequest{Class: "tsc"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing group: status = %d", rec.Code)
	}
}

// The connection is the lease: granted at once, booked while open, released
// by the disconnect alone.
func TestAcquireAdmissionGrantedAndReleasedOnDisconnect(t *testing.T) {
	t.Parallel()
	r, c, notify := newAdmissionRouter(t, 2)
	srv := httptest.NewServer(r)
	defer srv.Close()

	br, drop := openLease(t, srv, `{"group":"wt-a","class":"tsc","label":"tsc --noEmit"}`)
	u := readUpdate(t, br)
	if u.State != pigtail.AdmissionGranted || u.LeaseID == "" || u.CostBytes == 0 {
		drop()
		t.Fatalf("first line = %+v, want an immediate grant", u)
	}
	waitForAdmission(t, c, notify, "the lease to be booked", func(s admission.Status) bool { return len(s.Leases) == 1 })

	rec := doJSON(t, r, "GET", "/admission", nil)
	var st pigtail.AdmissionStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Leases) != 1 || st.Leases[0].Group != "wt-a" || st.Leases[0].Class != "tsc" || st.MaxJobs != 2 {
		t.Fatalf("status = %+v", st)
	}

	drop()
	waitForAdmission(t, c, notify, "the lease to be released", func(s admission.Status) bool { return len(s.Leases) == 0 })
}

func TestAcquireAdmissionQueuesThenGrants(t *testing.T) {
	t.Parallel()
	r, c, notify := newAdmissionRouter(t, 1)
	srv := httptest.NewServer(r)
	defer srv.Close()

	first, dropFirst := openLease(t, srv, `{"group":"wt-a","class":"tsc"}`)
	if u := readUpdate(t, first); u.State != pigtail.AdmissionGranted {
		dropFirst()
		t.Fatalf("first request: %+v", u)
	}

	second, dropSecond := openLease(t, srv, `{"group":"wt-b","class":"eslint"}`)
	defer dropSecond()
	u := readUpdate(t, second)
	if u.State != pigtail.AdmissionQueued || u.Position != 1 || u.Reason != string(admission.ReasonMaxJobs) || u.Running != 1 {
		dropFirst()
		t.Fatalf("second request's first line = %+v, want queued behind the job ceiling", u)
	}

	dropFirst()
	if u := readUpdate(t, second); u.State != pigtail.AdmissionGranted {
		t.Fatalf("after the first lease dropped: %+v, want granted", u)
	}
	waitForAdmission(t, c, notify, "the second lease", func(s admission.Status) bool {
		return len(s.Leases) == 1 && s.Leases[0].Group == "wt-b"
	})
}

func TestAcquireAdmissionWithdrawnWhenClientLeavesTheQueue(t *testing.T) {
	t.Parallel()
	r, c, notify := newAdmissionRouter(t, 1)
	srv := httptest.NewServer(r)
	defer srv.Close()

	first, dropFirst := openLease(t, srv, `{"group":"wt-a","class":"tsc"}`)
	defer dropFirst()
	readUpdate(t, first)

	second, dropSecond := openLease(t, srv, `{"group":"wt-b","class":"tsc"}`)
	readUpdate(t, second) // queued
	waitForAdmission(t, c, notify, "the waiter to be queued", func(s admission.Status) bool { return len(s.Queue) == 1 })
	dropSecond()
	waitForAdmission(t, c, notify, "the waiter to leave the queue", func(s admission.Status) bool { return len(s.Queue) == 0 })
}

func TestGetAdmissionHistory(t *testing.T) {
	t.Parallel()
	hist := admission.NewHistory(filepath.Join(t.TempDir(), "admission-history.jsonl"))
	c := admission.New(events.New(),
		admission.WithConfig(admission.Config{MaxJobs: 2, FloorBytes: 6 << 30, MaxHold: time.Hour}),
		admission.WithSampler(roomySampler{}),
		admission.WithLogger(func(string, ...any) {}),
		admission.WithHistory(hist),
	)
	tk, err := c.Enqueue(admission.Request{Group: "wt", Class: "tsc", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	c.Release(c.Grant(tk))
	r := chi.NewRouter()
	RegisterAdmissionRoutes(r, &Services{Admission: c}, testResponder{})

	rec := doJSON(t, r, "GET", "/admission/history?since=2000-01-01T00:00:00Z", nil)
	var out pigtail.AdmissionHistory
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if out.FloorBytes != 6<<30 || len(out.Records) != 1 || out.Records[0].Group != "wt" {
		t.Fatalf("history = %+v", out)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec = doJSON(t, r, "GET", "/admission/history?since="+future, nil)
	if !strings.Contains(rec.Body.String(), `"records":[]`) {
		t.Fatalf("since in the future must filter everything out, got %s", rec.Body.String())
	}
	if rec := doJSON(t, r, "GET", "/admission/history?since=7d", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a malformed since must be refused, got %d", rec.Code)
	}
}

func TestFillAdmissionStatusCarriesTheCompressor(t *testing.T) {
	t.Parallel()
	var out pigtail.AdmissionStatus
	fillAdmissionStatus(&out, admission.Status{
		Config:  admission.Config{MaxJobs: 3, CompressBytesPerSec: 16 << 20},
		Reading: admission.Reading{CompressorBytes: 19 << 30, UnusedBytes: 1 << 30, CompressBytesPerSec: 40 << 20},
	})
	if out.CompressorBytes != 19<<30 || out.UnusedBytes != 1<<30 ||
		out.CompressRateBytesPerSec != 40<<20 || out.CompressGateBytesPerSec != 16<<20 {
		t.Fatalf("status = %+v", out)
	}
}

func TestGetAdmissionQueueCarriesFocus(t *testing.T) {
	t.Parallel()
	r, c, _ := newAdmissionRouter(t, 1)
	if _, err := c.Enqueue(admission.Request{Group: "x", Class: "tsc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enqueue(admission.Request{Group: "a", Class: "tsc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enqueue(admission.Request{Group: "b", Class: "tsc", Focus: true}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, r, "GET", "/admission", nil)
	var raw struct {
		Queue []map[string]json.RawMessage `json:"queue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Queue) != 2 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if _, ok := raw.Queue[0]["focus"]; ok {
		t.Fatalf("non-focus row carries focus: %s", rec.Body.String())
	}
	if string(raw.Queue[1]["focus"]) != "true" {
		t.Fatalf("focus row = %v", raw.Queue[1])
	}
}

// An old client sends none of the identity fields.
func TestOldClientBodyDecodes(t *testing.T) {
	t.Parallel()
	r, _, _ := newAdmissionRouter(t, 2)
	srv := httptest.NewServer(r)
	defer srv.Close()
	br, drop := openLease(t, srv, `{"group":"wt-a","class":"tsc"}`)
	defer drop()
	if u := readUpdate(t, br); u.State != pigtail.AdmissionGranted {
		t.Fatalf("update = %+v", u)
	}
	var req pigtail.AdmissionRequest
	if err := json.Unmarshal([]byte(`{"group":"g"}`), &req); err != nil || req.SessionID != "" || req.CmuxWorkspaceID != "" || req.Cwd != "" {
		t.Fatalf("req = %+v err = %v", req, err)
	}
}

// fakeFocus answers true for the sessions in its set and counts its calls.
type fakeFocus struct {
	mu    sync.Mutex
	focus map[string]bool
	calls int
}

func (f *fakeFocus) IsFocus(c focuspri.Caller) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.focus[c.SessionID]
}

func (f *fakeFocus) set(session string, v bool) {
	f.mu.Lock()
	f.focus[session] = v
	f.mu.Unlock()
}

// grantOrder queues "plain" (older) then "focus" behind a held lease, drops the
// held one and returns which of the two was granted. flip runs once both are
// queued.
func grantOrder(t *testing.T, fp FocusResolver, flip func()) string {
	t.Helper()
	r, _, _ := newAdmissionRouterFP(t, 1, fp)
	srv := httptest.NewServer(r)
	defer srv.Close()
	first, dropFirst := openLease(t, srv, `{"group":"held","class":"tsc"}`)
	if u := readUpdate(t, first); u.State != pigtail.AdmissionGranted {
		dropFirst()
		t.Fatalf("first: %+v", u)
	}
	plain, dropPlain := openLease(t, srv, `{"group":"plain","class":"tsc","session_id":"plain-s"}`)
	defer dropPlain()
	if u := readUpdate(t, plain); u.State != pigtail.AdmissionQueued {
		dropFirst()
		t.Fatalf("plain: %+v", u)
	}
	foc, dropFoc := openLease(t, srv, `{"group":"focus","class":"tsc","session_id":"focus-s"}`)
	defer dropFoc()
	if u := readUpdate(t, foc); u.State != pigtail.AdmissionQueued {
		dropFirst()
		t.Fatalf("focus: %+v", u)
	}
	if flip != nil {
		flip()
	}
	dropFirst()
	got := make(chan string, 2)
	// No t.Fatal off the test goroutine: the connections close with the test.
	waitGranted := func(br *bufio.Reader, who string) {
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			if strings.Contains(string(line), `"state":"granted"`) {
				got <- who
				return
			}
		}
	}
	go waitGranted(plain, "plain")
	go waitGranted(foc, "focus")
	return <-got
}

func TestAcquireAdmissionFocusCallerJumps(t *testing.T) {
	t.Parallel()
	fp := &fakeFocus{focus: map[string]bool{"focus-s": true}}
	if got := grantOrder(t, fp, nil); got != "focus" {
		t.Fatalf("first granted = %s, want focus", got)
	}
}

func TestAcquireAdmissionNoFocusPriIsNormal(t *testing.T) {
	t.Parallel()
	if got := grantOrder(t, nil, nil); got != "plain" {
		t.Fatalf("first granted = %s, want plain (FIFO)", got)
	}
}

func TestAcquireAdmissionFocusFalseIsNormal(t *testing.T) {
	t.Parallel()
	fp := &fakeFocus{focus: map[string]bool{}}
	if got := grantOrder(t, fp, nil); got != "plain" {
		t.Fatalf("first granted = %s, want plain (FIFO)", got)
	}
}

// The flag is fixed at enqueue: a focus that ends while queued changes nothing.
func TestFocusFlagFixedAtEnqueue(t *testing.T) {
	t.Parallel()
	fp := &fakeFocus{focus: map[string]bool{"focus-s": true}}
	got := grantOrder(t, fp, func() { fp.set("focus-s", false) })
	if got != "focus" {
		t.Fatalf("first granted = %s, want focus (kept its place)", got)
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.calls != 3 {
		t.Fatalf("IsFocus called %d times, want once per request (3)", fp.calls)
	}
}

// The exit a client reports before hanging up lands on the history line,
// which is written at the disconnect, asynchronously.
func TestReportAdmissionExit(t *testing.T) {
	t.Parallel()
	hist := admission.NewHistory(filepath.Join(t.TempDir(), "admission-history.jsonl"))
	c := admission.New(events.New(),
		admission.WithConfig(admission.Config{MaxJobs: 2, FloorBytes: 6 << 30, MaxHold: time.Hour}),
		admission.WithSampler(roomySampler{}),
		admission.WithLogger(func(string, ...any) {}),
		admission.WithHistory(hist),
	)
	r := chi.NewRouter()
	RegisterAdmissionRoutes(r, &Services{Admission: c}, testResponder{})
	srv := httptest.NewServer(r)
	defer srv.Close()

	br, drop := openLease(t, srv, `{"group":"wt-a","class":"tsc"}`)
	u := readUpdate(t, br)
	if u.State != pigtail.AdmissionGranted || u.LeaseID == "" {
		drop()
		t.Fatalf("first line = %+v, want a grant", u)
	}
	path := "/admission/leases/" + u.LeaseID + "/exit"

	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	if rec := post(path, `{}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), string(pigtail.ErrInvalidRequest)) {
		drop()
		t.Fatalf("no exit_code: status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec := post(path, `not json`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), string(pigtail.ErrInvalidRequest)) {
		drop()
		t.Fatalf("not JSON: status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec := post("/admission/leases/nope/exit", `{"exit_code":1}`); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), string(pigtail.ErrNotFound)) {
		drop()
		t.Fatalf("unknown id: status = %d body = %s", rec.Code, rec.Body.String())
	}
	rec := post(path, `{"exit_code":5}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recorded":true`) {
		drop()
		t.Fatalf("report: status = %d body = %s", rec.Code, rec.Body.String())
	}
	drop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		h := doJSON(t, r, "GET", "/admission/history", nil)
		if strings.Contains(h.Body.String(), `"exit_code":5`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("history never showed exit_code 5: %s", h.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec := post(path, `{"exit_code":6}`); rec.Code != http.StatusNotFound {
		t.Fatalf("a released lease must answer 404, got %d", rec.Code)
	}
}

func TestFillAdmissionStatusCarriesTheCalmRule(t *testing.T) {
	var out pigtail.AdmissionStatus
	fillAdmissionStatus(&out, admission.Status{
		Config: admission.Config{MaxJobs: 3, CompressBytesPerSec: 16 << 20, CompressCalmWindow: time.Minute,
			CompressCalmClasses: []string{"build", "vitest-browser"}, CompressCalmMaxStarve: 10 * time.Minute},
		Reading: admission.Reading{CompressCalmBytesPerSec: 40 << 20},
	})
	if out.CompressCalmWindowSeconds != 60 || out.CompressCalmRateBytesPerSec != 40<<20 ||
		strings.Join(out.CompressCalmClasses, ",") != "build,vitest-browser" || out.CompressCalmMaxStarveSeconds != 600 {
		t.Fatalf("status = %+v", out)
	}
	var off pigtail.AdmissionStatus
	fillAdmissionStatus(&off, admission.Status{Config: admission.Config{MaxJobs: 3, CompressCalmClasses: []string{"build"}}})
	if off.CompressCalmWindowSeconds != 0 || off.CompressCalmClasses != nil {
		t.Fatalf("a rule switched off must not be reported, got %+v", off)
	}
}
