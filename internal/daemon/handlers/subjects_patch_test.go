package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedLinkedSubject creates a subject and fills the optional fields a partial
// update must never lose.
func seedLinkedSubject(t *testing.T, r http.Handler) pigtail.Subject {
	t.Helper()
	themeID := seedThemeAPI(t, r)
	rec := doJSON(t, r, "POST", "/subjects", pigtail.Subject{ThemeID: themeID, Title: "Keep my fields"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{
		"board_card": "ABC-42", "vault_path": "Globex/Projects/x/subjects/keep", "impact": 3, "rank": 2,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	return getSubjectAPI(t, r, sub.ID)
}

func getSubjectAPI(t *testing.T, r http.Handler, id string) pigtail.Subject {
	t.Helper()
	rec := doJSON(t, r, "GET", "/subjects/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	return sub
}

// A client that does not know a field (an older binary, a skill's curl) must
// not erase it: an absent key leaves the column as it is.
func TestUpdateSubjectKeepsAbsentFields(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	sub := seedLinkedSubject(t, r)
	if sub.BoardCard != "ABC-42" || sub.VaultPath == "" || sub.Impact != 3 {
		t.Fatalf("seed not applied: %+v", sub)
	}

	// Minimal body: only status.
	rec := doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"status": pigtail.StatusBuilding})
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := getSubjectAPI(t, r, sub.ID)
	if got.Status != pigtail.StatusBuilding {
		t.Fatalf("status not written: %+v", got)
	}
	if got.BoardCard != "ABC-42" || got.VaultPath != sub.VaultPath || got.Impact != 3 || got.Rank != 2 {
		t.Fatalf("absent fields were erased: %+v", got)
	}

	// Full struct from a client that predates board_card: every other field
	// is sent, board_card is not.
	raw, _ := json.Marshal(got)
	var old map[string]any
	_ = json.Unmarshal(raw, &old)
	delete(old, "board_card")
	old["title"] = "Renamed by an old client"
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, old)
	if rec.Code != http.StatusOK {
		t.Fatalf("old-client update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got = getSubjectAPI(t, r, sub.ID)
	if got.Title != "Renamed by an old client" {
		t.Fatalf("title not written: %+v", got)
	}
	if got.BoardCard != "ABC-42" {
		t.Fatalf("board_card erased by a client that does not send it: %+v", got)
	}
}

// An explicit null — or the zero value, the existing "reset" convention —
// clears the field.
func TestUpdateSubjectClearsExplicitFields(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	sub := seedLinkedSubject(t, r)

	rec := doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"board_card": nil, "vault_path": "", "impact": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := getSubjectAPI(t, r, sub.ID)
	if got.BoardCard != "" || got.VaultPath != "" || got.Impact != 0 {
		t.Fatalf("explicit null/empty not cleared: %+v", got)
	}
	if got.Rank != 2 || got.Title != sub.Title {
		t.Fatalf("fields outside the body changed: %+v", got)
	}

	// Required fields never go empty: null/"" keeps the current value.
	rec = doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"status": nil, "slug": "", "theme_id": nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("required-null status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got = getSubjectAPI(t, r, sub.ID)
	if got.Status != sub.Status || got.Slug != sub.Slug || got.ThemeID != sub.ThemeID {
		t.Fatalf("required fields emptied: %+v", got)
	}
}

// Server-owned keys are ignored, unknown keys and bodies with nothing to
// write are refused — nothing is written in that case.
func TestUpdateSubjectBodyValidation(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	sub := seedLinkedSubject(t, r)

	rec := doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{
		"id": "sub_other", "scope": "gx", "created_at": "2001-01-01T00:00:00Z", "rank": 7,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("read-only keys status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := getSubjectAPI(t, r, sub.ID)
	if got.Rank != 7 || !got.CreatedAt.Equal(sub.CreatedAt) || got.Scope != sub.Scope {
		t.Fatalf("read-only keys not ignored: %+v", got)
	}

	for name, body := range map[string]any{
		"unknown key":    map[string]any{"rank": 9, "no_such_field": "x"},
		"empty object":   map[string]any{},
		"read-only only": map[string]any{"id": sub.ID},
		"not an object":  []string{"rank"},
		"wrong type":     map[string]any{"rank": "high"},
	} {
		rec := doJSON(t, r, "POST", "/subjects/"+sub.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
	if got := getSubjectAPI(t, r, sub.ID); got.Rank != 7 {
		t.Fatalf("a refused body wrote something: %+v", got)
	}
}

// A subject always has a title: a body that sends it null or blank is refused
// and writes nothing, not even its other keys.
func TestUpdateSubjectRefusesEmptyTitle(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	sub := seedLinkedSubject(t, r)

	for name, title := range map[string]any{"null": nil, "empty": "", "blank": "  \t "} {
		rec := doJSON(t, r, "POST", "/subjects/"+sub.ID, map[string]any{"title": title, "rank": 9})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s title: status = %d, want 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
	if got := getSubjectAPI(t, r, sub.ID); got.Title != sub.Title || got.Rank != sub.Rank {
		t.Fatalf("a refused title wrote something: %+v", got)
	}
}
