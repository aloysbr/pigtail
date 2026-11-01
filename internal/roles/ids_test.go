package roles

import (
	"testing"
	"time"
)

func TestAlertIDRoundTrip(t *testing.T) {
	k := time.Date(2026, 10, 30, 11, 31, 0, 0, time.UTC)
	id := AlertID("pilote", k)
	if id != "role-silent:pilote:"+"1793359860000" {
		t.Fatalf("id = %s", id)
	}
	n, got, ok := ParseAlertID(id)
	if !ok || n != "pilote" || !got.Equal(k) {
		t.Fatalf("parse = %q %v %v", n, got, ok)
	}
	if n, got, ok := ParseRecoveredID(RecoveredID("a-b", k)); !ok || n != "a-b" || !got.Equal(k) {
		t.Fatalf("recovered parse = %q %v %v", n, got, ok)
	}
	for _, bad := range []string{"", "role-silent:", "role-silent:x", "role-silent:x:y", "respawn:a:1", "role-silent:BAD:1"} {
		if _, _, ok := ParseAlertID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestValidName(t *testing.T) {
	for n, want := range map[string]bool{"pilote": true, "a": true, "a-b-9": true, "-a": false, "A": false, "a_b": false, "": false,
		"abcdefghijklmnopqrstuvwxyz0123456": false, "abcdefghijklmnopqrstuvwxyz012345": true} {
		if ValidName(n) != want {
			t.Errorf("ValidName(%q) != %v", n, want)
		}
	}
}
