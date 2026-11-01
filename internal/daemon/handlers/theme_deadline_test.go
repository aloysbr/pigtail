package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func decodeTheme(t *testing.T, body []byte) pigtail.Theme {
	t.Helper()
	var th pigtail.Theme
	if err := json.Unmarshal(body, &th); err != nil {
		t.Fatalf("decode theme: %v (%s)", err, body)
	}
	return th
}

func TestPatchThemeDeadlineCarried(t *testing.T) {
	r, st := newTestRouter(t)
	id := seedThemeAPI(t, r)

	rec := doJSON(t, r, "PATCH", "/themes/"+id, map[string]any{"deadline": "2026-10-31", "carried": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("set both: %d %s", rec.Code, rec.Body)
	}
	if th := decodeTheme(t, rec.Body.Bytes()); th.Deadline != "2026-10-31" || !th.Carried || th.Name != "X" {
		t.Fatalf("set both = %+v", th)
	}

	rec = doJSON(t, r, "PATCH", "/themes/"+id, map[string]any{"carried": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("carried=false alone: %d %s", rec.Code, rec.Body)
	}
	if th := decodeTheme(t, rec.Body.Bytes()); th.Carried || th.Deadline != "2026-10-31" {
		t.Fatalf("carried=false = %+v", th)
	}

	rec = doJSON(t, r, "PATCH", "/themes/"+id, map[string]any{"deadline": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if th := decodeTheme(t, rec.Body.Bytes()); th.Deadline != "" {
		t.Fatalf("clear = %+v", th)
	}

	doJSON(t, r, "PATCH", "/themes/"+id, map[string]any{"deadline": "2026-10-31"})
	rec = doJSON(t, r, "PATCH", "/themes/"+id, map[string]any{"deadline": "31/10/2026"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "YYYY-MM-DD") {
		t.Fatalf("bad date: %d %s", rec.Code, rec.Body)
	}
	if th, _ := st.GetTheme(id); th.Deadline != "2026-10-31" {
		t.Fatalf("theme changed by a refused PATCH: %+v", th)
	}

	rec = doJSON(t, r, "PATCH", "/themes/"+id, map[string]any{"deadline": "2026-02-30", "name": "Y"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("2026-02-30: %d %s", rec.Code, rec.Body)
	}
	if th, _ := st.GetTheme(id); th.Name != "X" {
		t.Fatalf("name written despite the 400: %+v", th)
	}
}

func TestCreateThemeRefusesABadDeadline(t *testing.T) {
	r, _ := newTestRouter(t)
	rec := doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "Z", Kind: "project", VaultLink: "Globex/Projects/z", Deadline: "tomorrow"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad deadline: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, r, "POST", "/themes", pigtail.Theme{Name: "Z", Kind: "project", VaultLink: "Globex/Projects/z", Deadline: "2026-11-15", Carried: true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("good deadline: %d %s", rec.Code, rec.Body)
	}
	if th := decodeTheme(t, rec.Body.Bytes()); th.Deadline != "2026-11-15" || !th.Carried {
		t.Fatalf("created = %+v", th)
	}
}
