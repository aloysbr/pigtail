package pigtail

import (
	"encoding/json"
	"time"
)

// Session-message relay: short coordination messages between two Claude Code
// sessions, relayed by the daemon. See docs/wire-protocol.md §"Session
// messages" for the contract.

// ID prefixes of the relay.
const (
	PrefixMessage = "msg_"
	PrefixThread  = "thr_"
)

// Relay events, published on the SSE bus.
const (
	EventMessageCreated   EventType = "message.created"
	EventMessageDelivered EventType = "message.delivered"
	EventMessageAcked     EventType = "message.acked"
	EventMessageExpired   EventType = "message.expired"
)

// Relay error codes (additive to the stable list in types.go).
const (
	// ErrAddressUnresolved: no live session matches the address.
	ErrAddressUnresolved ErrorCode = "ADDRESS_UNRESOLVED"
	// ErrAddressAmbiguous: more than one live session matches; details list
	// the candidates.
	ErrAddressAmbiguous ErrorCode = "ADDRESS_AMBIGUOUS"
	// ErrScopeMismatch: sender and recipient run on different scopes and the
	// message is not a notice from the master cell with the cross-scope
	// opt-in, or a scope is unknown.
	ErrScopeMismatch ErrorCode = "SCOPE_MISMATCH"
	// ErrRelayRefused: a relay limit refused the message (size, secret,
	// pending cap, rate, thread cap, self-reply).
	ErrRelayRefused ErrorCode = "RELAY_REFUSED"
)

// Message kinds.
const (
	MessageKindInfo    = "info"
	MessageKindRequest = "request"
	MessageKindReply   = "reply"
	MessageKindAck     = "ack"
	// MessageKindNotice is a one-way service notice: no reply, no subject
	// attached. The only kind that may cross scopes — from the master cell,
	// with the cross-scope opt-in (D1, D3).
	MessageKindNotice = "notice"
)

// Message states.
const (
	MessageStatePending   = "pending"
	MessageStateDelivered = "delivered"
	MessageStateAcked     = "acked"
	MessageStateExpired   = "expired"
)

// SystemSender is the from_session of messages the daemon itself sends (an
// expiry notice to the sender of a message nobody read).
const SystemSender = "pigtail"

// PeerSession is one Claude Code session as the relay knows it: the join of
// cmux's session list, Claude's own sessions/<pid>.json, the cell table and
// pigtail's workspaces/subjects. Rows are derived (snapshots POSTed by the
// CLI, SessionStart/SessionEnd hooks), never declared by hand.
type PeerSession struct {
	SessionID       string `json:"session_id"`
	ConfigDir       string `json:"config_dir,omitempty"`
	CellID          string `json:"cell_id,omitempty"`
	Scope           string `json:"scope,omitempty"` // a scope name (from the cell table)
	Cwd             string `json:"cwd,omitempty"`
	Name            string `json:"name,omitempty"`   // Claude's native session name
	Status          string `json:"status,omitempty"` // Claude's native status (busy|idle|…)
	PID             int    `json:"pid,omitempty"`    // the claude process, never a Bash-tool pid
	CmuxWorkspaceID string `json:"cmux_workspace_id,omitempty"`
	CmuxSurfaceID   string `json:"cmux_surface_id,omitempty"`
	CmuxTitle       string `json:"cmux_title,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`   // pigtail worktree containing cwd
	WorkspaceName   string `json:"workspace_name,omitempty"` // its directory name
	SubjectID       string `json:"subject_id,omitempty"`
	// SubjectSet makes UpsertPeer write SubjectID as given, '' included,
	// instead of merging (an empty value never erases a stored one). Set by
	// the relay for a session proven alive; never on the wire.
	SubjectSet  bool       `json:"-"`
	SubjectSlug string     `json:"subject_slug,omitempty"`
	Lifecycle   string     `json:"lifecycle,omitempty"` // cmux agent_lifecycle
	Source      string     `json:"source,omitempty"`    // snapshot|hook|self
	UpdatedAt   time.Time  `json:"updated_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`

	// SubjectShortTitle is ShortTitle of the session's subject (SubjectID);
	// CmuxBound says its cmux workspace is bound to that subject. Read from
	// the subject and the link, never stored on the row. TitleRedacted is set
	// by the relay's scope redaction only, never serialised.
	SubjectShortTitle string `json:"subject_short_title,omitempty"`
	CmuxBound         bool   `json:"cmux_bound,omitempty"`
	TitleRedacted     bool   `json:"-"`

	// SubjectStamp is the raw peer_sessions.subject_id (the stamp), whatever
	// SubjectID reads; CmuxBindingSubjectID is the subject the session's cmux
	// workspace is linked to now, '' when unlinked. Both are read from the
	// store, never serialised: the relay's redaction and upsert use them.
	SubjectStamp         string `json:"-"`
	CmuxBindingSubjectID string `json:"-"`

	// LastWorktree is the worktree the session last worked in, from its
	// newest presence marker (written by the user's presence hook), read CLI-side
	// with no TTL — possibly a directory deleted since (consumers check). It
	// never feeds WorkspaceID/SubjectID: the cwd-derived worktree stays what
	// the label is built from.
	LastWorktree   string     `json:"last_worktree,omitempty"`
	LastWorktreeAt *time.Time `json:"last_worktree_at,omitempty"`

	// PausedAt is when pigtaild paused the session (sub_x000000000000036):
	// read from its session_pause row in state paused, never stored on the
	// peer row; nil = not paused.
	PausedAt *time.Time `json:"paused_at,omitempty"`

	// Waits are what the session told it waits for (spec §4.8): at most one
	// automatic (hooks) and one declared (`pigtail session wait`) entry.
	// Read from session_waits on every response; whether one shows in the
	// cmux sidebar also depends on the native status (see Source*).
	Waits []SessionWait `json:"waits,omitempty"`

	// Computed per response, never persisted.
	Label string `json:"label,omitempty"`
	Alive *bool  `json:"alive,omitempty"`
	Inbox int    `json:"inbox"`
}

// Sources of a session wait.
const (
	// WaitSourceAuto: a Claude Code hook typed a blocking dialog
	// (PermissionRequest, Notification, PreToolUse AskUserQuestion /
	// ExitPlanMode). Shown only while the native status is "waiting".
	WaitSourceAuto = "auto"
	// WaitSourceDeclared: the agent said it before handing back
	// (`pigtail session wait`). Shown while the session is idle or waiting.
	WaitSourceDeclared = "declared"
	// WaitSourceHeld: held by a process (type mac only), finished by that
	// process, its death, the end of the session or its until; never by a
	// prompt or a turn.
	WaitSourceHeld = "held"
)

// EventSessionWaitChanged is published when a session's wait is recorded or
// cleared. Data: {session_id, cmux_workspace_id, type, source} — never the
// reason; type is "" when cleared. Scope is the session's.
const EventSessionWaitChanged EventType = "session.wait_changed"

// SessionWait is one recorded wait of a session.
type SessionWait struct {
	// ID: the wait record of a declared wait, or of the auto wait of a dialog
	// (permission, question); empty otherwise.
	ID     string    `json:"id,omitempty"`
	Type   string    `json:"type"` // permission|question|arbitrage|review|blocked|done|working|wrapped; limit|login|api_error (auto only)
	Reason string    `json:"reason,omitempty"`
	Source string    `json:"source"` // auto|declared|held (type mac held only)
	Since  time.Time `json:"since"`
	// Until: auto limit and held mac waits: when it ends on its own (the
	// usage limit's reset; the hold's deadline).
	Until *time.Time `json:"until,omitempty"`
	// HolderPID: held waits only: the process that holds it.
	HolderPID int `json:"holder_pid,omitempty"`
}

// SessionWaitRequest is the body of POST /peers/{session}/wait.
type SessionWaitRequest struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
	Source string `json:"source,omitempty"` // default declared
	// Until: auto waits only: when it ends on its own (the usage limit's
	// reset). Kept for type limit only.
	Until *time.Time `json:"until,omitempty"`
	// HolderPID: held waits only (required then).
	HolderPID int `json:"holder_pid,omitempty"`
	// Details: the typed content of the wait (internal/waitdetail.Validate).
	Details json.RawMessage `json:"details,omitempty"`
}

// SessionSkillRequest is the body of POST /peers/{session}/skill: the skill
// the session just invoked (D11), kept as its last one.
type SessionSkillRequest struct {
	Skill string `json:"skill"`
}

// Turn-end events of POST /peers/{session}/turn.
const (
	TurnEventStop    = "stop"    // the Stop hook: the turn handed back
	TurnEventFailure = "failure" // the StopFailure hook: the turn failed
)

// SessionTurnRequest is the body of POST /peers/{session}/turn: a turn end
// (D12), keyed by the transcript's promptId.
type SessionTurnRequest struct {
	TurnID    string    `json:"turn_id"`
	TurnStart time.Time `json:"turn_start"`
	Event     string    `json:"event"` // stop | failure
	// Working (stop only): the turn's final message closes on a 🔄 line (work
	// going on in the background). When nothing was declared in the turn,
	// the daemon declares the working wait itself, with Reason; the turn
	// still counts as handed back undeclared. Without it, an undeclared Stop
	// ends a working wait left by an earlier turn.
	Working bool   `json:"working,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// SkillHandbacks counts the turns handed back without a declared wait, by the
// session's last skill then ("" = none invoked). Contract read by the weekly
// review job (M4).
type SkillHandbacks struct {
	Skill  string    `json:"skill"`
	Count  int       `json:"count"`
	LastAt time.Time `json:"last_at"`
}

// SessionHandbacksResponse is the answer of GET /session-handbacks.
type SessionHandbacksResponse struct {
	Since  time.Time        `json:"since"`
	Skills []SkillHandbacks `json:"skills"`
}

// PeersRequest is the body of POST /peers.
type PeersRequest struct {
	Peers []PeerSession `json:"peers"`
	// End marks the listed sessions ended (SessionEnd hook) instead of
	// upserting them.
	End bool `json:"end,omitempty"`
}

// Message is one relayed message.
type Message struct {
	ID           string     `json:"id"`
	ThreadID     string     `json:"thread_id"`
	InReplyTo    string     `json:"in_reply_to,omitempty"`
	FromSession  string     `json:"from_session"`
	ToSession    string     `json:"to_session"`
	FromLabel    string     `json:"from_label"`
	ToLabel      string     `json:"to_label"`
	SubjectID    string     `json:"subject_id,omitempty"`
	Kind         string     `json:"kind"`
	Body         string     `json:"body"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	DeliveredVia string     `json:"delivered_via,omitempty"`
	AckedAt      *time.Time `json:"acked_at,omitempty"`
}

// SendMessageRequest is the body of POST /messages.
type SendMessageRequest struct {
	From string `json:"from"` // sender session id
	// To is an address (subject slug or sub_ id, worktree name, cmux title
	// substring, native name, session-id prefix). Ignored for a reply, which
	// always goes back to the original sender.
	To         string `json:"to,omitempty"`
	Kind       string `json:"kind,omitempty"` // info|request|notice for a new message, reply|ack for a reply (default info)
	Body       string `json:"body"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"` // default 24h
	InReplyTo  string `json:"in_reply_to,omitempty"`
}

// SendMessageResponse is the data of a successful POST /messages.
type SendMessageResponse struct {
	Message Message     `json:"message"`
	To      PeerSession `json:"to"`
	// Pending is the recipient's pending count including this message, for
	// the sender's cmux fallback pill.
	Pending int `json:"pending"`
	// MatchedBy is the addressing level that resolved `to` (peers.Resolve
	// levels, e.g. "worktree", "last worktree"). Empty for a reply.
	MatchedBy string `json:"matched_by,omitempty"`
	// Delivery says how the recipient will see the message (D1):
	//   "waiter"  — a Stop watcher is long-polling (or polled < 15 s ago):
	//               it wakes now;
	//   "busy"    — native status busy|waiting: mid-turn, its PostToolUse /
	//               prompt hook reads the inbox;
	//   "none"    — idle with no watcher: it will NOT wake on its own;
	//   "unknown" — no watcher and no usable status: it may not wake;
	//   "wake"    — idle with no watcher, its pane verified: pigtaild types
	//               "lis ta boîte pigtail" at its next sweep, under the
	//               screen guard.
	// Empty from a daemon that predates the field: say nothing.
	Delivery string `json:"delivery,omitempty"`
}

// BroadcastRequest is the body of POST /messages/broadcast: one service notice
// (kind notice, D1) to every live session the sender reaches — its own scope,
// or every scope for the master cell with X-Pigtail-Cross-Scope: write (D2).
// Each recipient gets its own message and thread, under the limits of a
// single send; the response has one result per recipient (T3).
type BroadcastRequest struct {
	From       string `json:"from"` // sender session id
	Body       string `json:"body"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"` // default 24h
}

// BroadcastResult is the outcome of a broadcast for one recipient: sent
// (MessageID set) or refused (Refused and Reason set).
type BroadcastResult struct {
	To        PeerSession `json:"to"`
	MessageID string      `json:"message_id,omitempty"`
	Pending   int         `json:"pending,omitempty"`
	Delivery  string      `json:"delivery,omitempty"` // as SendMessageResponse.Delivery
	Refused   ErrorCode   `json:"refused,omitempty"`  // empty = sent
	Reason    string      `json:"reason,omitempty"`
}

// BroadcastResponse is the data of POST /messages/broadcast.
type BroadcastResponse struct {
	Results []BroadcastResult `json:"results"`
	Sent    int               `json:"sent"`
	Refused int               `json:"refused"`
	// OtherScopesSkipped counts the live sessions of other scopes a master
	// broadcast without the opt-in did not reach (C7). Set for a master
	// sender only: a non-master never learns of other scopes (D19).
	OtherScopesSkipped int `json:"other_scopes_skipped,omitempty"`
}

// Delivery values of SendMessageResponse.Delivery.
const (
	DeliveryWaiter  = "waiter"
	DeliveryBusy    = "busy"
	DeliveryNone    = "none"
	DeliveryUnknown = "unknown"
	// DeliveryWake: idle with no watcher, its pane verified: pigtaild types
	// "lis ta boîte pigtail" at its next sweep, under the screen guard.
	DeliveryWake = "wake"
)

// WillNotWake reports whether the sender must warn that nobody will read the
// message without a human: idle with no watcher, or no watcher and no usable
// status. False for an empty Delivery (older daemon).
func (r *SendMessageResponse) WillNotWake() bool {
	return r.Delivery == DeliveryNone || r.Delivery == DeliveryUnknown
}

// ClaimRequest is the body of POST /messages/claim.
type ClaimRequest struct {
	Session string `json:"session"`
	Via     string `json:"via"` // prompt|tool|rewake|cli
}

// ClaimResponse is the data of POST /messages/claim and GET /messages/wait.
type ClaimResponse struct {
	Messages []Message `json:"messages"`
	// Superseded is set by /messages/wait when a newer waiter for the same
	// session took over: the old waiter must exit.
	Superseded bool `json:"superseded,omitempty"`
}

// AckRequest is the body of POST /messages/{id}/ack.
type AckRequest struct {
	Session string `json:"session"`
}

// MessageFilter selects messages for GET /messages.
type MessageFilter struct {
	To      string
	From    string
	Session string // either side
	State   string
	Thread  string
	Subject string
	Since   time.Time
	Limit   int
}
