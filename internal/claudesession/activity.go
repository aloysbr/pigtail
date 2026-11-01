package claudesession

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ConversationSince reports whether the transcript at path holds a user or an
// assistant entry after byte offset (the size it had at a turn end): the
// conversation went on since. System entries and anything else are ignored —
// Claude Code may write system entries after its Stop hooks, so a raw size
// change is no proof. An offset past the end (a transcript rewritten) and an
// unreadable file are errors, never "nothing new".
func ConversationSince(path string, offset int64) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if offset < 0 || offset > st.Size() {
		return false, fmt.Errorf("offset %d past the transcript's %d bytes (rewritten?)", offset, st.Size())
	}
	r := bufio.NewReader(io.NewSectionReader(f, offset, st.Size()-offset))
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(line, &e) == nil && (e.Type == "user" || e.Type == "assistant") {
				return true, nil
			}
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}
