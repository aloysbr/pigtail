package pigtail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits and event of the subject outputs registry.
const (
	OutputTitleMax                    = 60
	OutputMetaMax                     = 4096
	OutputURLMax                      = 2048
	OutputListDefault                 = 200
	OutputListMax                     = 1000
	EventSubjectOutputAdded EventType = "subject.output_added"
	outputSubjectIDMax                = 200
	outputSessionIDMax                = 200
)

// OutputKinds is the closed list of output kinds. Kind "capture" is a
// screenshot; it has nothing to do with `pigtail capture` (backlog ideas).
var OutputKinds = []string{"capture", "html", "pdf", "mockup", "report", "server", "pr", "clip", "link", "file"}

var (
	outputURLRE  = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:\S+$`)
	outputStepRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// SubjectOutput is one artifact produced for a subject. Times are epoch ms.
type SubjectOutput struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	SubjectID string          `json:"subject_id"`
	SessionID string          `json:"session_id,omitempty"`
	Kind      string          `json:"kind"`
	Path      string          `json:"path,omitempty"`
	URL       string          `json:"url,omitempty"`
	Key       string          `json:"key"`
	Title     string          `json:"title"`
	TS        int64           `json:"ts"` // epoch ms
	Step      string          `json:"step,omitempty"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	PurgedAt  *int64          `json:"purged_at,omitempty"` // epoch ms
	CreatedAt int64           `json:"created_at"`          // epoch ms
	Exists    *bool           `json:"exists,omitempty"`    // filled by GET only
	// ServedURL is the outputs server's URL of Path (a round folder's ends
	// with "/"), filled by GET only, for a row with a path that exists, while
	// the outputs listener is bound. URL is never rewritten.
	ServedURL string `json:"served_url,omitempty"`
}

// SubjectOutputAddResult is the answer of POST /subjects/{id}/outputs.
type SubjectOutputAddResult struct {
	Output  SubjectOutput `json:"output"`
	Created bool          `json:"created"`
}

// SubjectOutputQuery filters a listing.
type SubjectOutputQuery struct {
	SinceSeq  int64
	Kind      string
	SessionID string
	Limit     int
}

// SubjectOutputPurgeRequest is the body of POST /subjects/{id}/outputs/purge.
type SubjectOutputPurgeRequest struct {
	Key string `json:"key"`
}

// OutputID is the deterministic id of the output of a subject under a key.
func OutputID(subjectID, key string) string {
	sum := sha256.Sum256([]byte(subjectID + "\n" + key))
	return "out_" + hex.EncodeToString(sum[:])[:12]
}

// TruncateTitle cuts s to OutputTitleMax runes, the final "…" included.
func TruncateTitle(s string) string {
	if utf8.RuneCountInString(s) <= OutputTitleMax {
		return s
	}
	r := []rune(s)
	return string(r[:OutputTitleMax-1]) + "…"
}

// DefaultOutputTitle derives a title: basename of path, else the url without
// its "<scheme>://" (or "<scheme>:"); truncated.
func DefaultOutputTitle(p, url string) string {
	if p != "" {
		return TruncateTitle(path.Base(filepath.ToSlash(p)))
	}
	if i := strings.Index(url, "://"); i >= 0 {
		url = url[i+3:]
	} else if i := strings.Index(url, ":"); i >= 0 {
		url = url[i+1:]
	}
	return TruncateTitle(url)
}

func outputBlank(r rune) bool { return r <= ' ' || r == 0x7f }

// Normalize trims, cleans and validates the output, and fills Key, ID, the
// default title and TS. Every error names its field. A title over
// OutputTitleMax is refused, never truncated here.
func (o *SubjectOutput) Normalize() error {
	o.SubjectID = strings.TrimSpace(o.SubjectID)
	o.SessionID = strings.TrimSpace(o.SessionID)
	o.Kind = strings.TrimSpace(o.Kind)
	o.Path = strings.TrimSpace(o.Path)
	o.URL = strings.TrimSpace(o.URL)
	o.Title = strings.TrimSpace(o.Title)
	o.Step = strings.TrimSpace(o.Step)
	switch {
	case o.SubjectID == "":
		return errors.New("subject_id is required")
	case len(o.SubjectID) > outputSubjectIDMax || strings.IndexFunc(o.SubjectID, outputBlank) >= 0:
		return fmt.Errorf("subject_id must be at most %d bytes, without whitespace", outputSubjectIDMax)
	case len(o.SessionID) > outputSessionIDMax || strings.IndexFunc(o.SessionID, outputBlank) >= 0:
		return fmt.Errorf("session_id must be at most %d bytes, without whitespace", outputSessionIDMax)
	}
	known := false
	for _, k := range OutputKinds {
		if o.Kind == k {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("kind %q must be one of %s", o.Kind, strings.Join(OutputKinds, " "))
	}
	if o.Path == "" && o.URL == "" {
		return errors.New("path or url is required")
	}
	if o.Path != "" {
		if !filepath.IsAbs(o.Path) {
			return fmt.Errorf("path %q must be absolute", o.Path)
		}
		o.Path = filepath.Clean(o.Path)
	}
	if o.URL != "" {
		if len(o.URL) > OutputURLMax {
			return fmt.Errorf("url is %d bytes, at most %d", len(o.URL), OutputURLMax)
		}
		if !outputURLRE.MatchString(o.URL) {
			return fmt.Errorf("url %q must match %s", o.URL, outputURLRE)
		}
	}
	o.Key = o.Path
	if o.Key == "" {
		o.Key = o.URL
	}
	o.ID = OutputID(o.SubjectID, o.Key)
	if o.Title == "" {
		o.Title = DefaultOutputTitle(o.Path, o.URL)
	}
	switch {
	case !utf8.ValidString(o.Title):
		return errors.New("title is not valid UTF-8")
	case strings.ContainsAny(o.Title, "\r\n"):
		return errors.New("title must be one line")
	case utf8.RuneCountInString(o.Title) > OutputTitleMax:
		return fmt.Errorf("title is %d characters, at most %d", utf8.RuneCountInString(o.Title), OutputTitleMax)
	case o.Title == "":
		return errors.New("title is required")
	}
	if o.Step != "" && !outputStepRE.MatchString(o.Step) {
		return fmt.Errorf("step %q must match %s", o.Step, outputStepRE)
	}
	if len(o.Meta) > 0 {
		if len(o.Meta) > OutputMetaMax {
			return fmt.Errorf("meta is %d bytes, at most %d", len(o.Meta), OutputMetaMax)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(o.Meta, &m); err != nil || m == nil {
			return errors.New("meta must be a JSON object")
		}
	}
	switch {
	case o.TS < 0:
		return errors.New("ts must not be negative")
	case o.TS == 0:
		o.TS = time.Now().UnixMilli()
	}
	return nil
}

// StatExists sets Exists: whether Path is on disk; true for a url-only row.
func StatExists(o *SubjectOutput) {
	ok := true
	if o.Path != "" {
		_, err := os.Stat(o.Path)
		ok = err == nil
	}
	o.Exists = &ok
}
