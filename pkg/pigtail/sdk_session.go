package pigtail

import (
	"encoding/json"
	"time"
)

// SDK session refusals (docs/wire-protocol.md § SDK sessions).
const (
	// ErrSDKOff: pigtaild runs without -sdk-host and -sdk-node (HTTP 503).
	ErrSDKOff ErrorCode = "SDK_OFF"
	// ErrSDKLaunchOff: SDK sessions can be read but pigtaild runs without
	// -sdk-launch, so none can be launched (HTTP 503).
	ErrSDKLaunchOff ErrorCode = "SDK_LAUNCH_OFF"
	// ErrNotIdle: a reply to a session whose turn is not over (HTTP 409).
	ErrNotIdle ErrorCode = "NOT_IDLE"
)

// SDK session states.
const (
	SDKStarting = "starting"
	SDKRunning  = "running"
	SDKWaiting  = "waiting"
	SDKIdle     = "idle"
	SDKEnded    = "ended"
	SDKFailed   = "failed"
	SDKLost     = "lost"
)

// SDKRequest is a question or a permission the session waits an answer for.
type SDKRequest struct {
	Rid      string          `json:"rid"`
	Kind     string          `json:"kind"` // question | permission
	Tool     string          `json:"tool"`
	Input    json.RawMessage `json:"input"`
	OpenedAt time.Time       `json:"opened_at"`
}

// SDKSession is a Claude session held by the Agent SDK (sdksession.Session on
// the wire). Live: a writer exists, so no resume command is offered.
type SDKSession struct {
	ID              string       `json:"id"`
	SubjectID       string       `json:"subject_id"`
	Cwd             string       `json:"cwd"`
	ConfigDir       string       `json:"config_dir"`
	ClaudeSessionID string       `json:"claude_session_id"`
	State           string       `json:"state"`
	Reason          string       `json:"reason,omitempty"`
	ResumeCommand   string       `json:"resume_command,omitempty"`
	HostPID         int          `json:"host_pid"`
	ClaudePIDs      []int        `json:"claude_pids"`
	Turn            int          `json:"turn"`
	Pending         []SDKRequest `json:"pending"`
	Live            bool         `json:"live"`
	StartedAt       time.Time    `json:"started_at"`
}

// SDKSessionList is GET /sdk-sessions.
type SDKSessionList struct {
	Sessions      []SDKSession `json:"sessions"`
	LaunchEnabled bool         `json:"launch_enabled"`
}

// SDKEvents is GET /sdk-sessions/{id}/events: the journal lines above `after`.
type SDKEvents struct {
	Events  []json.RawMessage `json:"events"`
	LastSeq int               `json:"last_seq"`
}

// SDKLaunched is the 202 of POST /subjects/{id}/sdk-sessions.
type SDKLaunched struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// SDKAnswerRequest answers a request: allow or deny; Answers maps a question's
// text to the chosen label(s) (a list for a multiSelect question); Message is
// the reason of a deny.
type SDKAnswerRequest struct {
	Behavior string                     `json:"behavior"`
	Answers  map[string]json.RawMessage `json:"answers,omitempty"`
	Message  string                     `json:"message,omitempty"`
}

// SDKMessageRequest is the reply of an end of turn.
type SDKMessageRequest struct {
	Text string `json:"text"`
}
