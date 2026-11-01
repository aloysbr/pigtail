package pigtail

import "time"

// Focus spaces (sub_x000000000000007): a temporary cmux sidebar folder that
// gathers the sessions of some themes and subjects, an overlay on
// themes.space. A focus counts while it is not ended and its deadline, if
// any, has not passed — judged at read.
const (
	PrefixFocus        = "fcs_"
	FocusMemberTheme   = "theme"
	FocusMemberSubject = "subject"
	FocusEndDeadline   = "deadline"
	FocusEndManual     = "manual"
)

// FocusMember is one theme or subject of a focus.
type FocusMember struct {
	Kind    string    `json:"kind"` // theme | subject
	ID      string    `json:"id"`
	AddedAt time.Time `json:"added_at"`
	Missing bool      `json:"missing,omitempty"` // theme/subject deleted since (no FK on member_id), set at read
}

// Focus is a temporary folder of themes and subjects.
type Focus struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Scope     string        `json:"scope"`
	CreatedAt time.Time     `json:"created_at"`
	EndsAt    *time.Time    `json:"ends_at,omitempty"`
	EndedAt   *time.Time    `json:"ended_at,omitempty"`
	EndReason string        `json:"end_reason,omitempty"` // deadline | manual
	Members   []FocusMember `json:"members"`
	Note      string        `json:"note,omitempty"` // set by POST /focus: why no folder shows (folders idle or stopped)
}
