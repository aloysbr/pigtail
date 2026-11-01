package pigtail

import (
	"strings"
	"time"
)

// Subject find (sub_x000000000000031): a few words → ranked subjects
// (POST /subjects/find). Every search is a subject_finds row with a pointer
// line in the journal; the subject finally picked is written by
// POST /subject-finds/{fid}/pick or by an attach carrying find_id.
const (
	PrefixSubjectFind          = "sf_"
	SubjectFindQueryMax        = 500 // bytes, after NormalizeFindQuery
	SubjectFindLimitDefault    = 5
	SubjectFindLimitMax        = 20
	JournalKindSubjectFind     = "subject-find"
	JournalKindSubjectFindPick = "subject-find-pick"
)

// SubjectFindSource is the journal source of a search's lines.
func SubjectFindSource(fid string) string { return "pigtail:subject-find/" + fid }

// NormalizeFindQuery folds every run of whitespace (newlines included) into
// one space and trims the ends: the stored query is one line.
func NormalizeFindQuery(q string) string { return strings.Join(strings.Fields(q), " ") }

// SubjectFindRequest is the body of POST /subjects/find.
type SubjectFindRequest struct {
	Query       string `json:"query"`
	WorkspaceID string `json:"workspace_id,omitempty"` // the pane's cmux workspace: groups searches until a pick
	Limit       int    `json:"limit,omitempty"`        // 1..20, 0 = 5
}

// SubjectFindCandidate is one ranked subject the caller may read.
type SubjectFindCandidate struct {
	SubjectID    string     `json:"subject_id"`
	ShortTitle   string     `json:"short_title"`
	Title        string     `json:"title"`
	ThemeID      string     `json:"theme_id"`
	Theme        string     `json:"theme"`
	Scope        string     `json:"scope"`
	Status       string     `json:"status"`
	Closed       bool       `json:"closed"`
	Writable     bool       `json:"writable"`
	LastActivity *time.Time `json:"last_activity,omitempty"` // last phase/progress/handoff/delivery/decision log line
	Score        float64    `json:"score"`                   // 2 decimals
	Matched      []string   `json:"matched"`                 // never null
}

// SubjectFindResult answers POST /subjects/find.
type SubjectFindResult struct {
	FindID     string                 `json:"find_id"`
	Query      string                 `json:"query"`      // normalised
	Candidates []SubjectFindCandidate `json:"candidates"` // never null
	Total      int                    `json:"total"`      // readable candidates before the limit
	Elsewhere  int                    `json:"elsewhere"`  // candidates of a scope the caller may not read
}

// SubjectFindHit is one stored candidate of a search, in rank order.
type SubjectFindHit struct {
	SubjectID string  `json:"subject_id"`
	Score     float64 `json:"score"`
}

// SubjectFind is one subject_finds row.
type SubjectFind struct {
	ID              string
	Scope           string
	Timestamp       time.Time
	Query           string
	Candidates      []SubjectFindHit // the readable candidates returned, never another scope's
	CmuxWorkspaceID string           // "" = none
	PickedSubjectID string           // "" = not picked
	PickedAt        time.Time        // zero = not picked
}

// SubjectFindPickRequest is the body of POST /subject-finds/{fid}/pick.
type SubjectFindPickRequest struct {
	SubjectID string `json:"subject_id"`
}

// SubjectFindPick answers a pick (and is what its journal line says).
type SubjectFindPick struct {
	FindID     string    `json:"find_id"`
	SubjectID  string    `json:"subject_id"`
	Rank       int       `json:"rank"`       // 1-based place among the search's candidates, 0 = not among them
	Candidates int       `json:"candidates"` // how many the search returned
	Searches   int       `json:"searches"`   // searches of the workspace since its previous pick, this one included; 1 without a workspace
	PickedAt   time.Time `json:"picked_at"`
	Created    bool      `json:"created"` // false: the same pick was already recorded
}
