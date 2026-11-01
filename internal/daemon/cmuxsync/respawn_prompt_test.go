package cmuxsync

import (
	"regexp"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waittype"
)

// realPromptTurnless is the user's turnless commands for these tests
// (settings.yaml session.turnless_prompts): /show [n] and /clip [n].
var realPromptTurnless = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^/show(?:\s+\d+)?$`),
	regexp.MustCompile(`^/clip(?:\s+\d+)?\s*$`),
}

// A prompt clears an abandoned mark only when it is the user's own real turn.
func TestRealPrompt(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Done: "/finish", Turnless: realPromptTurnless})
	for _, c := range []struct {
		name string
		p    claudesession.Prompt
		want bool
	}{
		{"plain", claudesession.Prompt{Text: "go on"}, true},
		{"wake line", claudesession.Prompt{Text: relay.WakeLine}, false},
		{"compact line", claudesession.Prompt{Text: relay.CompactLine}, false},
		{"padded wake line", claudesession.Prompt{Text: " lis ta boîte pigtail\n"}, false},
		{"empty", claudesession.Prompt{}, false},
		{"image only", claudesession.Prompt{Image: true}, true},
		{"/show tags", claudesession.Prompt{Text: "<command-message>show</command-message>\n<command-name>/show</command-name>"}, false},
		{"/show 2 tags", claudesession.Prompt{Text: "<command-message>show</command-message>\n<command-name>/show</command-name>\n<command-args>2</command-args>"}, false},
		{"/clip 2 plain", claudesession.Prompt{Text: "/clip 2"}, false},
		{"/compact tags", claudesession.Prompt{Text: "<command-name>/compact</command-name>"}, false},
		{"pasted command excerpt mid-prompt", claudesession.Prompt{Text: "why does this log <command-name>/show</command-name> twice?"}, true},
		{"/finish next tags", claudesession.Prompt{Text: "<command-message>finish</command-message>\n<command-name>/finish</command-name>\n<command-args>next</command-args>"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := realPrompt(c.p); got != c.want {
				t.Fatalf("realPrompt(%+v) = %v, want %v", c.p, got, c.want)
			}
		})
	}
}

// With no turnless command set, a logged slash command is a real turn like
// any other: only the wake and compact lines stay out.
func TestRealPromptNoTurnlessCommands(t *testing.T) {
	withWaitCommands(t, waittype.Commands{})
	for _, p := range []claudesession.Prompt{
		{Text: "<command-message>show</command-message>\n<command-name>/show</command-name>"},
		{Text: "/clip 2"},
	} {
		if !realPrompt(p) {
			t.Fatalf("realPrompt(%q) = false with no turnless command set", p.Text)
		}
	}
	if realPrompt(claudesession.Prompt{Text: relay.WakeLine}) {
		t.Fatal("the wake line read as a real prompt")
	}
}
