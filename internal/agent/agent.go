// Package agent defines the Agent interface for spawning and managing
// AI coding agent sessions (Claude Code, Codex, Aider, …).
//
// v1 ships a single implementation: tmux (each AgentSession runs in a
// detached tmux session, surviving terminal close and Mac restart).
// The interface is shaped so a future container/firecracker backend
// can be added without touching daemon code.
package agent

import (
	"context"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// SpawnIntent carries the information needed to spawn an agent inside
// an existing workspace.
type SpawnIntent struct {
	// AgentKind is the agent binary to run ("claude", "codex", "aider").
	AgentKind string

	// Intent is the initial prompt sent to the agent.
	Intent string

	// ProfileID, if set, selects a Profile whose CLAUDE_CONFIG_DIR /
	// env overrides are applied to the spawned process.
	ProfileID string

	// PlanFirst tells compatible agents to enter planning mode (e.g.
	// Claude Code "/plan") before making any edits.
	PlanFirst bool

	// PlanPath, if set, is the workspace-relative path of a reviewed plan
	// the agent must read and execute (set when DispatchRequest.PlanContent
	// was provided). Takes precedence over PlanFirst when building the command.
	PlanPath string

	// ExtraEnv merges into the spawn environment (after Profile env).
	ExtraEnv map[string]string
}

// AttachInfo tells a client how to attach to a running agent session
// (typically: a tmux session name to pass to `tmux attach`).
type AttachInfo struct {
	Kind            string // "tmux" for v1
	TmuxSessionName string
	Command         string // ready-to-run shell command, e.g. "tmux a -t pigtail-…"
}

// Agent owns the runtime lifecycle of an AgentSession.
//
// Implementations: ClaudeAgent via tmux (v1), future container backends.
type Agent interface {
	// Kind returns the agent backend identifier ("tmux", "container", …).
	// Note: this is the *runtime* kind, distinct from AgentKind on the
	// session (which identifies the AI tool: claude/codex/aider).
	Kind() string

	// Spawn starts an agent inside the given workspace. Returns a fully
	// populated *AgentSession with TmuxSessionName / ExternalSessionID
	// set and State = running.
	Spawn(ctx context.Context, ws *pigtail.Workspace, intent SpawnIntent) (*pigtail.AgentSession, error)

	// Attach returns the info a client needs to attach a terminal to
	// the live agent session.
	Attach(ctx context.Context, sess *pigtail.AgentSession) (AttachInfo, error)

	// Pause signals the agent to stop accepting work. For tmux-backed
	// agents this typically means sending a control sequence so the
	// agent finishes its current step and waits.
	Pause(ctx context.Context, sess *pigtail.AgentSession) error

	// Resume undoes a previous Pause.
	Resume(ctx context.Context, sess *pigtail.AgentSession) error

	// Kill terminates the agent process and its tmux session (or
	// equivalent). Idempotent.
	Kill(ctx context.Context, sess *pigtail.AgentSession) error

	// Status returns the current runtime state of the session by
	// inspecting the underlying process / tmux session. Used by the
	// daemon to reconcile in-memory state on startup or after a crash.
	Status(ctx context.Context, sess *pigtail.AgentSession) (pigtail.AgentState, error)

	// Reply forwards user input to the agent. For tmux-backed agents
	// this types the text into the live pane and presses Enter.
	// Multi-line content is delivered line by line.
	Reply(ctx context.Context, sess *pigtail.AgentSession, content string) error
}
