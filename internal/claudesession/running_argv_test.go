package claudesession

import "testing"

// A session spawned by another one carries its parent's id in its prompt (the
// parent's scratchpad path): the parent, once quit, must not read as running.
func TestRunningMatchesOnlyTheIDASessionFlagNames(t *testing.T) {
	const id = "779bf8ca-271c-45d3-836c-70538927e298"
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"--resume", "  42 claude --resume " + id + " --permission-mode auto\n", true},
		{"--resume=", "  42 claude --resume=" + id + "\n", true},
		{"-r", "  42 /opt/homebrew/bin/claude -r " + id + "\n", true},
		{"--session-id", "  42 claude --session-id " + id + " --settings /tmp/x\n", true},
		{"--session-id=", "  42 node /x/.bin/claude --session-id=" + id + "\n", true},
		{"id in another session's prompt", "  42 claude --session-id 1f5c3174-d9e3-419a-bfc6-3a7e37c9460e --permission-mode auto /example-skill Brief : /private/tmp/claude-501/-Users-x/" + id + "/scratchpad/brief.md .\n", false},
		{"id as a bare word in a prompt", "  42 claude --session-id 1f5c3174-d9e3-419a-bfc6-3a7e37c9460e resume " + id + "\n", false},
		{"longer value starting with the id", "  42 claude --resume " + id + "-other\n", false},
		{"another flag names the id", "  42 claude --settings " + id + "\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			saved := psRun
			t.Cleanup(func() { psRun = saved })
			psRun = func() ([]byte, error) { return []byte(c.line), nil }
			if got := Running(id); got != c.want {
				t.Fatalf("Running(%q) on %q = %v, want %v", id, c.line, got, c.want)
			}
		})
	}
}
