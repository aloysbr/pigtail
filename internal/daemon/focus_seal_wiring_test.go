package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Focus D26: New wires the store's seal gate on the same Watch as FreezeMW.
// Under a freeze an expired focus reads ended and nothing is written; at the
// first read after the lift it is sealed, one line per subject, once.
func TestServerSealHeldDuringFreeze(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", dir)
	st, err := store.Open(filepath.Join(t.TempDir(), "pigtail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "ongoing", VaultLink: "Perso/Areas/p", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_p", ThemeID: "thm_p", Title: "p", Slug: "p", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ends := now.Add(-time.Hour)
	if err := st.CreateFocus(&pigtail.Focus{ID: "fcs_old", Name: "Old", Scope: "perso", EndsAt: &ends,
		Members: []pigtail.FocusMember{{Kind: pigtail.FocusMemberSubject, ID: "sub_p"}}}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Save(dir, maintenance.State{ID: "mnt_focus", On: true, Freeze: true, Reason: "focus", Since: now}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.buildRouter()
	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = ""
		req.Header.Set(pigtail.HeaderScope, "perso")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	focusLines := func() int {
		lines, err := st.ListLog("sub_p", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l.Text, `focus "Old"`) {
				n++
			}
		}
		return n
	}
	if body := get("/focus"); strings.Contains(body, "fcs_old") {
		t.Fatalf("expired focus listed open: %s", body)
	}
	var env struct {
		Data struct {
			Focus []pigtail.Focus `json:"focus"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(get("/focus?all=1")), &env); err != nil || len(env.Data.Focus) != 1 || env.Data.Focus[0].EndReason != pigtail.FocusEndDeadline {
		t.Fatalf("all = %+v, %v", env, err)
	}
	if n := focusLines(); n != 0 {
		t.Fatalf("%d end lines written during the freeze", n)
	}
	// The seal writes ended_at and the end lines in one transaction: no
	// line means not sealed in base; the lift below writes it, once.
	if err := os.Remove(filepath.Join(dir, maintenance.FileName)); err != nil {
		t.Fatal(err)
	}
	get("/focus")
	if n := focusLines(); n != 1 {
		t.Fatalf("%d end lines after the lift, want 1", n)
	}
	get("/focus")
	if n := focusLines(); n != 1 {
		t.Fatalf("%d end lines after a second read, want 1", n)
	}
	lines, _ := st.ListLog("sub_p", time.Time{})
	if len(lines) != 1 || !lines[0].Timestamp.Equal(time.UnixMilli(ends.UnixMilli())) {
		t.Fatalf("line = %+v, want dated the deadline", lines)
	}
}
