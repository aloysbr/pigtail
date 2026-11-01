package handlers

import (
	"encoding/json"
	"testing"
)

// The header alias is read as globex; every answer says globex.
func TestScopeHeaderAlias(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	for _, h := range []string{"gx", "GX", "globex"} {
		rec := doAs(t, r, caller{scope: h}, "GET", "/subjects/sub_t", nil)
		if rec.Code != 200 {
			t.Fatalf("GET aliased subject as %q = %d", h, rec.Code)
		}
		var body struct {
			Scope string `json:"scope"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Scope != "globex" {
			t.Fatalf("as %q: subject scope = %q, want globex", h, body.Scope)
		}
		if rec := doAs(t, r, caller{scope: h}, "GET", "/subjects/sub_p", nil); rec.Code != 404 {
			t.Fatalf("perso subject as %q = %d, want 404", h, rec.Code)
		}
	}
}
