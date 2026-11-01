package pigtail

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SubjectCmuxBinding is the explicit link between a subject and a cmux
// workspace (table subject_cmux_bindings, migration 0012). A workspace carries
// at most one subject; a subject may have several workspaces. The first link
// wins: binding a workspace already bound to another subject is refused
// (409) unless the caller asks to replace it.
//
// The title/description fields are the sidebar loop's memory (cmuxsync, a
// later PR): what the workspace was called before pigtail renamed it, what
// pigtail last posed, and who owns the description now (the title is always
// pigtail's since D5). The link routes only read them, and reset the owners
// on retitle.
type SubjectCmuxBinding struct {
	CmuxWorkspaceID    string    `json:"cmux_workspace_id"` // UUID, upper case
	SubjectID          string    `json:"subject_id"`
	Source             string    `json:"source"` // attach | subject-work | ptnew
	BoundAt            time.Time `json:"bound_at"`
	PrevTitle          string    `json:"prev_title,omitempty"`
	PrevTitleCustom    bool      `json:"prev_title_custom,omitempty"`
	PrevDescription    string    `json:"prev_description,omitempty"`
	AppliedTitle       string    `json:"applied_title,omitempty"`
	AppliedDescription string    `json:"applied_description,omitempty"`
	TitleOwner         string    `json:"title_owner"`       // pigtail | user
	DescriptionOwner   string    `json:"description_owner"` // pigtail | user
	MissingTicks       int       `json:"missing_ticks,omitempty"`
	// FindID echoes the attach's find_id once its pick is recorded (attach
	// response only): a CLI that sent one and reads none knows the daemon
	// ignored it.
	FindID string `json:"find_id,omitempty"`
}

// States of a cmux workspace, as GET /cmux-workspaces/:wsid/state and `subject which --json` name them.
const (
	WorkspaceStateBound          = "bound"
	WorkspaceStateOffTopic       = "off-topic"
	WorkspaceStateBoundElsewhere = "bound-elsewhere" // linked to a subject the caller may not read (D25)
	WorkspaceStateUnbound        = "unbound"
	WorkspaceStateUnknown        = "unknown" // CLI only: the state route could not be read
)

// WorkspaceOffTopic is a cmux workspace declared off-topic (table
// workspace_off_topic, migration 0020): the session there works on no
// subject, by choice. Dropped when the workspace closes or a subject is bound.
type WorkspaceOffTopic struct {
	CmuxWorkspaceID string    `json:"cmux_workspace_id"`
	Scope           string    `json:"scope,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	DeclaredAt      time.Time `json:"declared_at"`
	MissingTicks    int       `json:"missing_ticks,omitempty"`
	// Space is the explicit space given at declaration (migration 0024, D1 of
	// sub_x000000000000019); "" = none, inferred at read time.
	Space string `json:"space,omitempty"`
	// ResolvedSpace is computed at read time, never stored: Space, else the
	// space of the first readable subject cited in Reason; set only when it is
	// a declared space.
	ResolvedSpace string `json:"resolved_space,omitempty"`
}

// Writers of the link (SubjectCmuxBinding.Source).
const (
	CmuxSourceAttach      = "attach"
	CmuxSourceSubjectWork = "subject-work"
	CmuxSourcePtnew       = "ptnew"
)

// Owners of a bound workspace's title and description.
const (
	CmuxOwnerPigtail = "pigtail"
	CmuxOwnerUser    = "user"
)

// ValidCmuxBindingSource reports whether s names a known writer of the link.
func ValidCmuxBindingSource(s string) bool {
	switch s {
	case CmuxSourceAttach, CmuxSourceSubjectWork, CmuxSourcePtnew:
		return true
	}
	return false
}

// AttachCmuxWorkspaceRequest is the body of POST /subjects/:id/cmux-workspaces.
type AttachCmuxWorkspaceRequest struct {
	CmuxWorkspaceID string `json:"cmux_workspace_id"`
	Source          string `json:"source,omitempty"` // default attach
	// Replace moves a workspace bound to another subject onto this one. The
	// caller must be able to write that other subject too.
	Replace bool `json:"replace,omitempty"`
	// Retitle hands the description back to pigtail (owners reset to
	// "pigtail"), as if it had never been edited by hand. The title is
	// always pigtail's since D5.
	Retitle bool `json:"retitle,omitempty"`
	// FindID is the subject find (POST /subjects/find) that led to this
	// subject: the attach records it as the search's pick.
	FindID string `json:"find_id,omitempty"`
}

// CmuxWorkspaceSubject is the subject summary GET /cmux-workspaces/:wsid
// returns next to the binding.
type CmuxWorkspaceSubject struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	ShortTitle string `json:"short_title"` // effective label (stored or derived)
	Scope      string `json:"scope"`
}

// CmuxWorkspaceLookup is the answer of GET /cmux-workspaces/:wsid.
type CmuxWorkspaceLookup struct {
	Binding SubjectCmuxBinding   `json:"binding"`
	Subject CmuxWorkspaceSubject `json:"subject"`
}

var cmuxWorkspaceIDRe = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)

// NormalizeCmuxWorkspaceID trims and upper-cases a cmux workspace UUID — the
// form `cmux workspace list` prints and the binding table keys on. Anything
// that is not a UUID (a `workspace:N` ref included: the CLI resolves those)
// is an error.
func NormalizeCmuxWorkspaceID(s string) (string, error) {
	id := strings.ToUpper(strings.TrimSpace(s))
	if !cmuxWorkspaceIDRe.MatchString(id) {
		return "", fmt.Errorf("cmux workspace id must be a UUID, got %q", s)
	}
	return id, nil
}

// DetailBoundElsewhere marks the 404 of GET /cmux-workspaces/:wsid for a
// workspace bound to a subject the caller may not read (D7).
const DetailBoundElsewhere = "bound_elsewhere"

// IsCmuxBoundElsewhere reports that 404: the workspace carries a link the
// caller cannot see. It names nothing more.
func IsCmuxBoundElsewhere(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != ErrNotFound {
		return false
	}
	v, _ := ae.Details[DetailBoundElsewhere].(bool)
	return v
}

// CmuxWorkspaceState answers GET /cmux-workspaces/:wsid/state.
type CmuxWorkspaceState struct {
	WorkspaceID string                `json:"workspace_id"`
	State       string                `json:"state"` // bound | off-topic | unbound
	Subject     *CmuxWorkspaceSubject `json:"subject,omitempty"`
	OffTopic    *WorkspaceOffTopic    `json:"off_topic,omitempty"` // only when the caller may read the declaration
}

// DeclareOffTopicRequest is the body of POST /cmux-workspaces/:wsid/off-topic.
type DeclareOffTopicRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Space     string `json:"space,omitempty"` // explicit space (D1); "" = inferred
}
