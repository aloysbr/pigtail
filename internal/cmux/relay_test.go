package cmux

import (
	"reflect"
	"testing"
)

func TestRelayArgs(t *testing.T) {
	for n, want := range map[int]string{1: "1 message", 2: "2 messages", 12: "12 messages"} {
		got := setStatusArgs("W", n)
		if !reflect.DeepEqual(got, []string{"set-status", InboxStatusKey, want, "--icon", "envelope", "--priority", "70", "--workspace", "W"}) {
			t.Fatalf("set-status(%d) = %q", n, got)
		}
	}
	if got := OneLine("a\n  b\tc", 4); got != "a b…" {
		t.Fatalf("OneLine = %q", got)
	}
}
