package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aloysbr/pigtail/internal/buildinfo"
)

// A2: GET /version serves the build identity stamped into pigtaild, so
// `pigtail version --daemon` and post-merge can read what is installed.
func TestVersionRouteServesBuild(t *testing.T) {
	ob, oc, od := buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty
	buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty = "main", "0123456789abcdef0123456789abcdef01234567", "false"
	t.Cleanup(func() { buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty = ob, oc, od })

	srv := healthServer(t, nil)
	rec := httptest.NewRecorder()
	srv.handleVersion(rec, httptest.NewRequest("GET", "/version", nil))
	if rec.Code != 200 {
		t.Fatalf("/version = %d", rec.Code)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"daemon_branch": "main",
		"daemon_commit": "0123456789abcdef0123456789abcdef01234567",
		"daemon_dirty":  "false",
	}
	for k, v := range want {
		if body.Data[k] != v {
			t.Errorf("%s = %v, want %q (%s)", k, body.Data[k], v, rec.Body.String())
		}
	}
	if _, ok := body.Data["daemon_version"]; !ok {
		t.Errorf("daemon_version gone: %s", rec.Body.String())
	}
}

// An unstamped daemon serves unknown, never an empty field (A1).
func TestVersionRouteUnstampedReadsUnknown(t *testing.T) {
	ob, oc, od := buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty
	buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty = "", "", ""
	t.Cleanup(func() { buildinfo.Branch, buildinfo.Commit, buildinfo.Dirty = ob, oc, od })

	srv := healthServer(t, nil)
	rec := httptest.NewRecorder()
	srv.handleVersion(rec, httptest.NewRequest("GET", "/version", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"daemon_branch", "daemon_commit", "daemon_dirty"} {
		if body.Data[k] != "unknown" {
			t.Errorf("%s = %v, want unknown", k, body.Data[k])
		}
	}
}
