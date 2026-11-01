package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func rankBody(changes ...[3]any) map[string]any {
	out := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		out = append(out, map[string]any{"id": c[0], "from": c[1], "to": c[2]})
	}
	return map[string]any{"changes": out}
}

// sub_p (rank 1) is the master's, sub_t (rank 2) the other scope's.
func TestSetRanksScope(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	t2, err := st.GetSubject("sub_t")
	if err != nil || scope.Canonical(t2.Scope) == scope.Master() {
		t.Fatalf("sub_t must be outside the master: %+v %v", t2, err)
	}
	other := caller{scope: t2.Scope}
	hidden := doAs(t, r, other, "POST", "/subjects/ranks", rankBody([3]any{"sub_p", 1, 3}))
	unknown := doAs(t, r, other, "POST", "/subjects/ranks", rankBody([3]any{"sub_nope", 1, 3}))
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("non-master on the master's subject = %d %s, unknown %d %s", hidden.Code, hidden.Body, unknown.Code, unknown.Body)
	}
	master := caller{scope: string(scope.Master())}
	if rec := doAs(t, r, master, "POST", "/subjects/ranks", rankBody([3]any{"sub_p", 1, 3}, [3]any{"sub_t", 2, 4})); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), string(pigtail.ErrScopeMismatch)) {
		t.Fatalf("master without opt-in on another scope = %d %s, want 403", rec.Code, rec.Body)
	}
	if p, _ := st.GetSubject("sub_p"); p.Rank != 1 {
		t.Fatalf("a refused change wrote sub_p rank %d", p.Rank)
	}
	rec := doWeb(t, r, "POST", "/subjects/ranks", rankBody([3]any{"sub_p", 1, 3}, [3]any{"sub_t", 2, 4}))
	if rec.Code != http.StatusOK {
		t.Fatalf("web front = %d %s, want 200", rec.Code, rec.Body)
	}
	var got []pigtail.Subject
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 2 || got[0].ID != "sub_p" || got[0].Rank != 3 || got[1].Rank != 4 {
		t.Fatalf("web front answer = %s (%v)", rec.Body, err)
	}
	// A stale from: 409 RANK_CHANGED, nothing written.
	rec = doWeb(t, r, "POST", "/subjects/ranks", rankBody([3]any{"sub_p", 3, 1}, [3]any{"sub_t", 9, 2}))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), string(pigtail.ErrRankChanged)) {
		t.Fatalf("stale from = %d %s, want 409 %s", rec.Code, rec.Body, pigtail.ErrRankChanged)
	}
	if p, _ := st.GetSubject("sub_p"); p.Rank != 3 {
		t.Fatalf("a refused change wrote sub_p rank %d", p.Rank)
	}
}

func TestSetRanksValidation(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	many := make([][3]any, 501)
	for i := range many {
		many[i] = [3]any{fmt.Sprintf("sub_%d", i), 0, 1}
	}
	for name, body := range map[string]any{
		"none":      rankBody(),
		"501":       rankBody(many...),
		"duplicate": rankBody([3]any{"sub_p", 1, 2}, [3]any{"sub_p", 2, 3}),
		"negative":  rankBody([3]any{"sub_p", 1, -1}),
		"neg from":  rankBody([3]any{"sub_p", -1, 1}),
		"empty id":  rankBody([3]any{"", 1, 2}),
		"not json":  "nope",
	} {
		if rec := doWeb(t, r, "POST", "/subjects/ranks", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
}

func TestSetRanksFrozen(t *testing.T) {
	t.Parallel()
	r, _ := frozenRouter(t, frozen())
	rec := req(r, "POST", "/subjects/ranks", "other")
	if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "MAINTENANCE_FROZEN") {
		t.Fatalf("under a freeze = %d %s, want 423", rec.Code, rec.Body)
	}
	if rec := req(r, "POST", "/subjects/ranks", "lead"); rec.Code == http.StatusLocked {
		t.Fatal("the exempt session must pass the freeze")
	}
}
