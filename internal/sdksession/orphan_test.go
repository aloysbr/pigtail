package sdksession

import "testing"

func TestShellQuoteAndResume(t *testing.T) {
	cases := map[string]string{"/tmp/a-b": "/tmp/a-b", "/tmp/a b": "'/tmp/a b'", "it's": `'it'\''s'`, "": "''"}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
	m := Meta{Cwd: "/tmp/my work", ConfigDir: "/c", SessionUUID: "u1"}
	if got := resumeCommand(m, ""); got != "cd '/tmp/my work' && CLAUDE_CONFIG_DIR=/c claude --resume u1" {
		t.Errorf("resume = %q", got)
	}
	if resumeCommand(Meta{}, "") != "" {
		t.Error("no id, no command")
	}
}
