package claudesession

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// Prompt is one prompt typed by the user, read from a transcript.
type Prompt struct {
	At    time.Time
	Text  string // string content, or the text blocks joined by "\n"
	Image bool   // a block is an image (an image-only prompt has Text == "")
}

// promptReadMax bounds one PromptsAfter read (a var for tests).
var promptReadMax int64 = 8 << 20

type promptEntry struct {
	Type      string                 `json:"type"`
	IsMeta    bool                   `json:"isMeta"`
	Timestamp string                 `json:"timestamp"`
	Origin    *struct{ Kind string } `json:"origin"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment struct {
		Type   string                 `json:"type"`
		Prompt json.RawMessage        `json:"prompt"`
		Origin *struct{ Kind string } `json:"origin"`
	} `json:"attachment"`
}

// PromptsAfter returns the prompts the user typed, stamped strictly after
// since, oldest first, read from byte offset from of the transcript at path,
// and the offset just past the last complete line read.
//
// A typed prompt is, as measured on local transcripts (2026-10-23), either a
// "user" entry whose origin.kind is "human", or, when typed during a turn, an
// "attachment" entry of type "queued_command" whose attachment.origin.kind is
// "human" (its text in attachment.prompt, no "user" entry follows). Background
// task notifications ("task-notification"), peer messages ("peer", isMeta),
// auto continuations, tool results, local commands and compact summaries are
// never returned. A transcript written by a Claude Code that carries no
// origin yields nothing.
//
// from < 0 or beyond the file reads its tail; a read is capped at
// promptReadMax. A partial last line is left for the next read.
func PromptsAfter(path string, from int64, since time.Time) ([]Prompt, int64, error) {
	w, err := readWindow(path, from)
	if err != nil {
		return nil, from, err
	}
	var out []Prompt
	for _, line := range w.lines {
		if p, ok := parsePrompt(line, since); ok {
			out = append(out, p)
		}
	}
	return out, w.next, nil
}

// window is one incremental read of a transcript: its complete lines and the
// offset just past the last one.
type window struct {
	lines [][]byte
	next  int64
	// whole: the window starts at byte 0 (the whole file prefix was read).
	whole bool
	// resumed: the window starts at the caller's own offset, so it continues
	// a previous read with no gap (not a tail read, not a capped one).
	resumed bool
}

// readWindow reads the complete lines of the transcript at path from byte
// offset from (from < 0 or beyond the file reads its tail; a read is capped
// at promptReadMax; a partial last line is left for the next read).
func readWindow(path string, from int64) (window, error) {
	if path == "" {
		return window{}, ErrNoTranscript
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return window{}, ErrNoTranscript
		}
		return window{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return window{}, err
	}
	size := st.Size()
	start := from
	resumed := true
	if start < 0 || start > size {
		start = max(0, size-apiErrorTail)
		resumed = false
	}
	capped := false
	if size-start > promptReadMax {
		start = size - promptReadMax
		capped, resumed = true, false
	}
	readFrom := start
	if start > 0 {
		readFrom = start - 1
	}
	buf := make([]byte, size-readFrom)
	n, err := f.ReadAt(buf, readFrom)
	if err != nil && !errors.Is(err, io.EOF) {
		return window{}, err
	}
	buf = buf[:n]
	if start > 0 {
		// Skip the first line unless the window starts on a line start.
		if buf[0] != '\n' {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				buf = nil
			} else {
				buf = buf[i:]
				readFrom += int64(i)
			}
		}
		if len(buf) > 0 {
			buf = buf[1:]
			readFrom++
		}
	}
	w := window{whole: start == 0, resumed: resumed}
	last := bytes.LastIndexByte(buf, '\n')
	if last < 0 {
		w.next = readFrom
		if capped {
			w.next = size
		}
		return w, nil
	}
	w.next = readFrom + int64(last) + 1
	w.lines = bytes.Split(buf[:last], []byte{'\n'})
	return w, nil
}

func parsePrompt(line []byte, since time.Time) (Prompt, bool) {
	if !bytes.Contains(line, []byte(`"human"`)) {
		return Prompt{}, false
	}
	var e promptEntry
	if json.Unmarshal(line, &e) != nil || e.IsMeta {
		return Prompt{}, false
	}
	var raw json.RawMessage
	switch {
	case e.Type == "user" && e.Origin != nil && e.Origin.Kind == "human":
		raw = e.Message.Content
	case e.Type == "attachment" && e.Attachment.Type == "queued_command" &&
		e.Attachment.Origin != nil && e.Attachment.Origin.Kind == "human":
		raw = e.Attachment.Prompt
	default:
		return Prompt{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil || !at.After(since) {
		return Prompt{}, false
	}
	text, image := promptContent(raw)
	if strings.TrimSpace(text) == "" && !image {
		return Prompt{}, false
	}
	return Prompt{At: at, Text: text, Image: image}, true
}

func promptContent(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var parts []string
	image := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			image = true
		}
	}
	return strings.Join(parts, "\n"), image
}
