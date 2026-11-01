package main

import (
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The inbox preamble counts Ada' answers apart from peer messages
// (sub_x000000000000026, Task 7).
func TestInboxPreambleCountsAnswers(t *testing.T) {
	t.Parallel()
	human := pigtail.Message{FromSession: pigtail.HumanSender, FromLabel: "Ada"}
	peer := pigtail.Message{FromSession: "s-peer"}
	sys := pigtail.Message{FromSession: pigtail.SystemSender}
	for _, c := range []struct {
		ms   []pigtail.Message
		want string
	}{
		{[]pigtail.Message{peer}, "pigtail relay: 1 message from another Claude session.\n"},
		{[]pigtail.Message{peer, sys}, "pigtail relay: 2 messages from other Claude sessions.\n"},
		{[]pigtail.Message{human}, "pigtail relay: 1 answer from Ada.\n"},
		{[]pigtail.Message{human, human}, "pigtail relay: 2 answers from Ada.\n"},
		{[]pigtail.Message{human, peer, peer}, "pigtail relay: 1 answer from Ada, 2 messages from other sessions.\n"},
		{[]pigtail.Message{peer, human}, "pigtail relay: 1 answer from Ada, 1 message from another session.\n"},
	} {
		if got := inboxPreamble(c.ms); got != c.want {
			t.Errorf("inboxPreamble(%d msgs) = %q, want %q", len(c.ms), got, c.want)
		}
	}
}
