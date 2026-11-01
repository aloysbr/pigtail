package handlers

import (
	"strings"
	"testing"
)

func TestGenID(t *testing.T) {
	t.Parallel()
	id := genID("sub_")
	if !strings.HasPrefix(id, "sub_") {
		t.Fatalf("missing prefix: %q", id)
	}
	if len(id) != len("sub_")+16 { // 8 random bytes → 16 hex chars
		t.Fatalf("unexpected length: %q (%d)", id, len(id))
	}
	first, second := genID("sub_"), genID("sub_")
	if first == second {
		t.Fatal("genID should not collide on consecutive calls")
	}
}
