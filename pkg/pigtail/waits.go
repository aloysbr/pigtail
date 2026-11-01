package pigtail

import "time"

// Enriched waits (sub_x000000000000026, D1, D2): a declared wait carries a
// typed content, a stable id and a history; the user lists it, answers it or
// snoozes it from his terminal (`pigtail wait …`). A dialog of Claude Code
// (permission, question) opens a record too, readable, never answered outside
// the terminal (D2). See docs/wire-protocol.md § Waits.
//
// D28: an answerable wait bound to a subject belongs to the subject in its
// scope; the session is only its emitter. Its answer is a structured value
// (WaitAnswer), the French text a rendering of it, and both payloads carry a
// versioned envelope (kind, schema).

// PrefixWait is the id prefix of a wait record.
const PrefixWait = "wait_"

// States of a wait record.
const (
	WaitStateOpen     = "open"
	WaitStateReplaced = "replaced" // a newer wait of the same owner (its subject in its scope, else its session) and source, or a newer wait of the same session and source
	WaitStateCleared  = "cleared"  // its emitter's session_waits row went (a prompt, the end of the session for a wait its subject does not own)
	WaitStateAnswered = "answered" // the user answered it; the answer went as a relay message, or is kept for the subject's next session
)

// Envelope versions this pigtaild reads (D28 c).
const (
	WaitDetailsSchema = 1
	WaitAnswerSchema  = 1
)

// Wait events, published on the SSE bus. Data: {wait_id, session_id, state}
// — never the reason nor the content. Scope is the wait's (frozen at its
// declaration). session_id is the emitting session, except on wait.delivered
// where it names the session the answer reached.
const (
	EventWaitOpened    EventType = "wait.opened"
	EventWaitAnswered  EventType = "wait.answered"
	EventWaitDelivered EventType = "wait.delivered"
	EventWaitSnoozed   EventType = "wait.snoozed"
	EventWaitClosed    EventType = "wait.closed"
)

// HumanSender is the from_session of the message carrying the human's answer
// to a wait; its from_label is the configured human name (settings.yaml).
const HumanSender = "human"

// NonPeerSender reports whether id is a sender that is not a session: the
// daemon itself (SystemSender) or the human (HumanSender).
func NonPeerSender(id string) bool {
	return id == SystemSender || id == HumanSender
}

// WaitDetails is the typed content of a wait. Which fields a type takes is
// checked by internal/waitdetail.Validate.
type WaitDetails struct {
	// Envelope (D28 c): kind is the wait's type, schema the version of this
	// shape. Optional on input, normalised on storage (Normalize).
	Kind   string `json:"kind,omitempty"`
	Schema int    `json:"schema,omitempty"`
	// Common to every type.
	Title   string     `json:"title,omitempty"`
	Summary string     `json:"summary,omitempty"`
	Step    string     `json:"step,omitempty"`
	URL     string     `json:"url,omitempty"`
	Shots   []WaitShot `json:"shots,omitempty"`
	// arbitrage, question.
	Options []WaitOption `json:"options,omitempty"`
	// arbitrage: why the reco.
	Why string `json:"why,omitempty"`
	// question: what a free answer stands for.
	Other string `json:"other,omitempty"`
	// review: the items to judge, or a url and its lead.
	Items []WaitItem `json:"items,omitempty"`
	Lead  string     `json:"lead,omitempty"`
	// permission: shown only, never answered outside the terminal (D2).
	Cmd   string `json:"cmd,omitempty"`
	Where string `json:"where,omitempty"`
}

// Normalize fills an absent envelope: kind with the wait's type, schema with
// version 1. A value already set is never overwritten.
func (d *WaitDetails) Normalize(kind string) {
	if d.Kind == "" {
		d.Kind = kind
	}
	if d.Schema == 0 {
		d.Schema = WaitDetailsSchema
	}
}

// WaitShot is a capture: an http(s) or file URL, or an absolute path.
type WaitShot struct {
	Src     string `json:"src"`
	Caption string `json:"caption,omitempty"`
}

// WaitOption is one option of an arbitrage or a question.
type WaitOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Reco   bool   `json:"reco,omitempty"`
	URL    string `json:"url,omitempty"`
}

// WaitItem is one item of a review.
type WaitItem struct {
	Label  string     `json:"label"`
	Detail string     `json:"detail,omitempty"`
	URL    string     `json:"url,omitempty"`
	Shots  []WaitShot `json:"shots,omitempty"`
}

// Wait is one wait record as GET /waits and GET /waits/{id} answer it.
type Wait struct {
	ID string `json:"id"`
	// SessionID is the emitting session; SessionEnded says it is gone
	// (ended, or its pid dead) while the wait stays its subject's (D29-A).
	SessionID         string       `json:"session_id"`
	SessionLabel      string       `json:"session_label,omitempty"`
	SessionEnded      bool         `json:"session_ended,omitempty"`
	SubjectID         string       `json:"subject_id,omitempty"`
	SubjectShortTitle string       `json:"subject_short_title,omitempty"`
	Type              string       `json:"type"`
	Reason            string       `json:"reason,omitempty"`
	Source            string       `json:"source"` // declared|auto
	State             string       `json:"state"`
	OpenedAt          time.Time    `json:"opened_at"`
	ClosedAt          *time.Time   `json:"closed_at,omitempty"`
	SnoozedUntil      *time.Time   `json:"snoozed_until,omitempty"`
	Details           *WaitDetails `json:"details,omitempty"`
	Answer            *WaitAnswer  `json:"answer,omitempty"`
	// DeliveredTo is the session the answer reached; empty while it is kept.
	DeliveredTo      string     `json:"delivered_to,omitempty"`
	DeliveredToLabel string     `json:"delivered_to_label,omitempty"`
	DeliveredAt      *time.Time `json:"delivered_at,omitempty"`
}

// WaitAnswerRequest is the body of POST /waits/{id}/answer.
type WaitAnswerRequest struct {
	Choice string   `json:"choice,omitempty"`
	Text   string   `json:"text,omitempty"`
	Items  []string `json:"items,omitempty"` // "n=ok|ko[:note]"
	Seen   bool     `json:"seen,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

// WaitAnswer is the answer kept on a wait record: the source of truth, the
// message body a rendering of it (D28 b).
type WaitAnswer struct {
	Kind   string            `json:"kind,omitempty"`   // the wait's type
	Schema int               `json:"schema,omitempty"` // WaitAnswerSchema
	Choice string            `json:"choice,omitempty"`
	Text   string            `json:"text,omitempty"`
	Items  []WaitItemVerdict `json:"items,omitempty"`
	Seen   bool              `json:"seen,omitempty"`
	Reason string            `json:"reason,omitempty"`
	At     time.Time         `json:"at"`
}

// Normalize fills an absent envelope: kind with the wait's type, schema with
// version 1. A value already set is never overwritten.
func (a *WaitAnswer) Normalize(kind string) {
	if a.Kind == "" {
		a.Kind = kind
	}
	if a.Schema == 0 {
		a.Schema = WaitAnswerSchema
	}
}

// WaitItemVerdict is the verdict on review item N (1-based).
type WaitItemVerdict struct {
	N    int    `json:"n"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// WaitAnswerResponse is the answer of POST /waits/{id}/answer.
type WaitAnswerResponse struct {
	Wait         Wait   `json:"wait"`
	MessageID    string `json:"message_id"`
	Delivery     string `json:"delivery"`
	StandbyEnded bool   `json:"standby_ended,omitempty"`
	// DeliveredTo is the session the answer went to: the emitter, or the
	// subject's live session; empty when kept (DeliveryKept).
	DeliveredTo      string `json:"delivered_to,omitempty"`
	DeliveredToLabel string `json:"delivered_to_label,omitempty"`
}

// DeliveryHeld opens WaitAnswerResponse.Delivery when the session's Standby
// could not be lifted (unreadable): the answer waits for the user's prompt; the
// reason follows ("held: standby unreadable: …").
const DeliveryHeld = "held"

// DeliveryKept is WaitAnswerResponse.Delivery when no session of the wait's
// subject runs in its scope: the answer is kept and goes to the subject's
// next session (D29-C).
const DeliveryKept = "kept: no live session on its subject"

// WaitSnoozeRequest is the body of POST /waits/{id}/snooze.
type WaitSnoozeRequest struct {
	Until time.Time `json:"until"`
}
