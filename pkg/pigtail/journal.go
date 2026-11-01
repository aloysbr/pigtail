package pigtail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The system journal is an index of pointers: one row per event, a one-line
// summary and a link to where the content lives — never the content itself
// (subject "Journal système", D1). The bounds below are what makes that hold.
const (
	JournalSummaryMax  = 280  // characters (runes)
	JournalSourceMax   = 1024 // bytes
	JournalIDMax       = 200  // bytes
	JournalListDefault = 200
	JournalListMax     = 1000
)

var (
	journalKindRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	journalSourceRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:\S`)
	// scopeNameRE is the shape of a scope name (internal/scope); which names
	// exist is the daemon's scope table, not the client's.
	scopeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// Journal kinds written by the daemon itself for space changes (D7, D8 of
// sub_x000000000000033).
const (
	JournalKindSubjectSpaceDrag    = "subject-space-drag"
	JournalKindOffTopicSpaceDrag   = "workspace-off-topic-space-drag"
	JournalKindSubjectSpaceCleared = "subject-space-cleared"
)

// JournalEntry is one row of the system journal. ID is supplied by the
// producer and deterministic: re-posting it in the same scope is a no-op.
// Scope and SubjectID are optional on write (see docs/wire-protocol.md
// § Journal); the stored row always has a scope.
type JournalEntry struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"ts"`
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope,omitempty"`
	SubjectID string    `json:"subject_id,omitempty"`
	Summary   string    `json:"summary"`
	Source    string    `json:"source"`
}

// JournalAddResult answers POST /journal: Created is false when the id was
// already present in that scope — Entry is then the stored row, unchanged.
type JournalAddResult struct {
	Entry   JournalEntry `json:"entry"`
	Created bool         `json:"created"`
}

// JournalQuery filters GET /journal. Zero values mean "no filter"; Scope is a
// view (the daemon narrows it to what the caller may read).
type JournalQuery struct {
	Since, Until time.Time
	Scope        string
	Kind         string
	SubjectID    string
	Limit        int
}

func blankOrControl(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }

// Validate trims the text fields, then checks every bound. The error names
// the field so a producer's stderr line says what to fix.
func (e *JournalEntry) Validate() error {
	e.ID = strings.TrimSpace(e.ID)
	e.Kind = strings.TrimSpace(e.Kind)
	e.Scope = strings.TrimSpace(e.Scope)
	e.SubjectID = strings.TrimSpace(e.SubjectID)
	e.Summary = strings.TrimSpace(e.Summary)
	e.Source = strings.TrimSpace(e.Source)
	switch {
	case e.ID == "":
		return errors.New("id is required (the producer's deterministic id)")
	case len(e.ID) > JournalIDMax || strings.IndexFunc(e.ID, blankOrControl) >= 0:
		return fmt.Errorf("id must be at most %d bytes, without whitespace", JournalIDMax)
	case !journalKindRE.MatchString(e.Kind):
		return fmt.Errorf("kind %q must match %s", e.Kind, journalKindRE)
	case e.Summary == "":
		return errors.New("summary is required")
	case !utf8.ValidString(e.Summary):
		return errors.New("summary is not valid UTF-8")
	case strings.ContainsAny(e.Summary, "\r\n"):
		return errors.New("summary must be one line — the journal points to the content, it never holds it")
	case utf8.RuneCountInString(e.Summary) > JournalSummaryMax:
		return fmt.Errorf("summary is %d characters, at most %d — the journal points to the content, it never holds it",
			utf8.RuneCountInString(e.Summary), JournalSummaryMax)
	case e.Source == "":
		return errors.New("source is required (a link: https:, vault:, file:, pigtail:)")
	case len(e.Source) > JournalSourceMax || strings.ContainsAny(e.Source, "\r\n") || !journalSourceRE.MatchString(e.Source):
		return fmt.Errorf("source must be one link of at most %d bytes starting with a scheme (https:, vault:, file:, pigtail:)", JournalSourceMax)
	case e.Scope != "" && !scopeNameRE.MatchString(strings.ToLower(strings.TrimSpace(e.Scope))):
		return fmt.Errorf("scope %q must be a scope name (the daemon checks it against its scope table)", e.Scope)
	case len(e.SubjectID) > JournalIDMax || strings.IndexFunc(e.SubjectID, blankOrControl) >= 0:
		return errors.New("subject_id must be a pigtail subject id")
	}
	return nil
}

// AddJournal posts one journal row (POST /journal).
func (c *Client) AddJournal(ctx context.Context, e JournalEntry) (*JournalAddResult, error) {
	var out JournalAddResult
	if _, err := c.do(ctx, "POST", "/journal", e, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Journal reads the journal across subjects (GET /journal), newest first.
func (c *Client) Journal(ctx context.Context, q JournalQuery) ([]JournalEntry, error) {
	v := url.Values{}
	if !q.Since.IsZero() {
		v.Set("since", strconv.FormatInt(q.Since.UnixMilli(), 10))
	}
	if !q.Until.IsZero() {
		v.Set("until", strconv.FormatInt(q.Until.UnixMilli(), 10))
	}
	for k, s := range map[string]string{"scope": q.Scope, "kind": q.Kind, "subject": q.SubjectID} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/journal"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out []JournalEntry
	if _, err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
