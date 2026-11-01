package claudesession

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// apiErrorTail bounds how much of a transcript LastAPIError reads: the
// error entry of a turn that just failed is among the last lines.
const apiErrorTail = 256 << 10

// APIError is the last API error entry of a transcript (isApiErrorMessage):
// what Claude Code wrote when a turn stopped on an API failure.
type APIError struct {
	Error    string    // rate_limit, authentication_failed, server_error, …
	Status   int       // apiErrorStatus (429, 401, 500…)
	At       time.Time // the entry's timestamp
	ResetsAt time.Time // quotaLimits.resetsAt (epoch s); zero when absent
	Text     string    // message.content: the string, or the text blocks joined by "\n"
}

// Classes of an API error for the auto-retry: a network failure waits for the
// API to be reachable again, anything else follows the timed back-off.
const (
	ClassNetwork = "network"
	ClassOther   = "other"
)

// networkMarkers are the (case-insensitive) texts of a connectivity failure.
var networkMarkers = []string{"ENOTFOUND", "ECONNRESET", "Connection lost", "went to sleep"}

// Network reports whether the error text names a connectivity failure.
func (e APIError) Network() bool {
	t := strings.ToLower(e.Text)
	for _, m := range networkMarkers {
		if strings.Contains(t, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// Class is ClassNetwork for a connectivity failure, ClassOther otherwise.
func (e APIError) Class() string {
	if e.Network() {
		return ClassNetwork
	}
	return ClassOther
}

// contentText reads message.content: a JSON string, or an array of blocks
// whose "text" blocks are joined by a newline. Anything else: "".
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// LastAPIError reads the tail of the transcript at path and returns its last
// API error entry. Missing file, unreadable tail or no such entry: false.
func LastAPIError(path string) (APIError, bool) {
	lines, ok := transcriptTail(path)
	if !ok {
		return APIError{}, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !bytes.Contains(l, []byte(`"isApiErrorMessage":true`)) {
			continue
		}
		var e struct {
			Error       string    `json:"error"`
			Status      int       `json:"apiErrorStatus"`
			Timestamp   time.Time `json:"timestamp"`
			QuotaLimits struct {
				ResetsAt int64 `json:"resetsAt"`
			} `json:"quotaLimits"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(l, &e) != nil || e.Error == "" {
			continue // a line cut by the tail, or not an error entry
		}
		out := APIError{Error: e.Error, Status: e.Status, At: e.Timestamp, Text: contentText(e.Message.Content)}
		if e.QuotaLimits.ResetsAt > 0 {
			out.ResetsAt = time.Unix(e.QuotaLimits.ResetsAt, 0)
		}
		return out, true
	}
	return APIError{}, false
}

// transcriptTail returns the lines of the last apiErrorTail bytes of the
// transcript at path (the first may be cut). Missing path or file, or an
// unreadable tail: false.
func transcriptTail(path string) ([][]byte, bool) {
	if path == "" {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	off := st.Size() - apiErrorTail
	if off < 0 {
		off = 0
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return nil, false
	}
	return bytes.Split(buf, []byte("\n")), true
}
