package claudesession

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConversationSince: only a user or assistant entry after the offset is
// the conversation going on; system entries (written after the Stop hooks)
// are not, and an offset past the end (a rewritten transcript) is an error.
func TestConversationSince(t *testing.T) {
	base := `{"type":"user","promptId":"p1","message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"ok"}}` + "\n"
	for _, cs := range []struct {
		name, tail string
		want       bool
	}{
		{"nothing", "", false},
		{"system", `{"type":"system","subtype":"stop_hook_summary"}` + "\n", false},
		{"assistant", `{"type":"assistant","message":{"role":"assistant","content":"more"}}` + "\n", true},
		{"user", `{"type":"user","promptId":"p2","message":{"role":"user","content":"next"}}` + "\n", true},
		{"system then user", `{"type":"system"}` + "\n" + `{"type":"user","promptId":"p2"}` + "\n", true},
		{"garbage", "not json\n", false},
	} {
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, []byte(base+cs.tail), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ConversationSince(p, int64(len(base)))
		if err != nil || got != cs.want {
			t.Fatalf("%s: ConversationSince = %v, %v; want %v", cs.name, got, err, cs.want)
		}
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConversationSince(p, int64(len(base))+1); err == nil {
		t.Fatal("offset past the end: want an error")
	}
	if _, err := ConversationSince(filepath.Join(t.TempDir(), "absent.jsonl"), 0); err == nil {
		t.Fatal("missing transcript: want an error")
	}
}
