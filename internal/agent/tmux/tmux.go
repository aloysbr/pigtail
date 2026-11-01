// Package tmux implements the Agent interface using detached tmux sessions,
// following the pattern established by claude-squad.
package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/agent"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Agent is the tmux-backed implementation of agent.Agent. Each
// AgentSession runs as a detached `tmux new-session -d`, which means it
// survives the controlling terminal closing and Mac restarts (as long
// as tmux server is running).
type Agent struct{}

// New constructs the tmux agent.
func New() *Agent { return &Agent{} }

// Kind returns "tmux".
func (a *Agent) Kind() string { return "tmux" }

// Spawn starts an agent in a detached tmux session inside the workspace
// directory. For the slice, only AgentKind == "claude" is supported.
func (a *Agent) Spawn(ctx context.Context, ws *pigtail.Workspace, intent agent.SpawnIntent) (*pigtail.AgentSession, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, &pigtail.APIError{Code: pigtail.ErrTmuxNotAvailable, Message: "tmux binary not found in PATH"}
	}
	if intent.AgentKind == "" {
		intent.AgentKind = "claude"
	}
	if intent.AgentKind != "claude" {
		return nil, fmt.Errorf("tmux: unsupported agent_kind %q (slice supports claude only)", intent.AgentKind)
	}
	if ws == nil || ws.Path == "" {
		return nil, errors.New("tmux: workspace path required")
	}

	sessID := "agt_" + shortID()
	sessName := fmt.Sprintf("pigtail-%s-%s", ws.ID, sessID)

	if err := tmuxNewSession(ctx, sessName, ws.Path, intent.ExtraEnv); err != nil {
		return nil, &pigtail.APIError{Code: pigtail.ErrAgentSpawnFailed, Message: err.Error()}
	}

	// Build the claude invocation. The agent receives the intent as its
	// first prompt. We quote-escape to survive any shell metachars.
	cmd := buildClaudeCommand(intent)
	if err := tmuxSendKeys(ctx, sessName, cmd); err != nil {
		_ = tmuxKillSession(context.Background(), sessName)
		return nil, &pigtail.APIError{Code: pigtail.ErrAgentSpawnFailed, Message: err.Error()}
	}

	now := time.Now().UTC()
	return &pigtail.AgentSession{
		ID:              sessID,
		WorkspaceID:     ws.ID,
		AgentKind:       intent.AgentKind,
		Intent:          intent.Intent,
		ProfileID:       intent.ProfileID,
		State:           pigtail.AgentStateRunning,
		TmuxSessionName: sessName,
		StartedAt:       now,
		LastEventAt:     now,
	}, nil
}

// Attach returns the tmux command the user should run to attach.
func (a *Agent) Attach(_ context.Context, sess *pigtail.AgentSession) (agent.AttachInfo, error) {
	if sess.TmuxSessionName == "" {
		return agent.AttachInfo{}, errors.New("tmux: session has no tmux_session_name")
	}
	return agent.AttachInfo{
		Kind:            "tmux",
		TmuxSessionName: sess.TmuxSessionName,
		Command:         fmt.Sprintf("tmux attach -t %s", sess.TmuxSessionName),
	}, nil
}

// Pause is a no-op stub for the slice. Real implementation would
// send a control sequence the agent listens for.
func (a *Agent) Pause(_ context.Context, _ *pigtail.AgentSession) error {
	return errors.New("tmux: Pause not implemented (post-slice)")
}

// Resume mirrors Pause.
func (a *Agent) Resume(_ context.Context, _ *pigtail.AgentSession) error {
	return errors.New("tmux: Resume not implemented (post-slice)")
}

// Kill terminates the tmux session (and therefore the agent process).
func (a *Agent) Kill(ctx context.Context, sess *pigtail.AgentSession) error {
	if sess.TmuxSessionName == "" {
		return nil
	}
	return tmuxKillSession(ctx, sess.TmuxSessionName)
}

// Reply types `content` into the agent's tmux pane and presses Enter.
// Multi-line content is delivered line by line so the agent sees each
// line plus a newline at the end (mirrors what a user typing would do).
func (a *Agent) Reply(ctx context.Context, sess *pigtail.AgentSession, content string) error {
	if sess == nil || sess.TmuxSessionName == "" {
		return errors.New("tmux: session has no tmux_session_name")
	}
	if content == "" {
		return errors.New("tmux: reply content is empty")
	}
	// Send each line separately so newlines render as actual Enters
	// rather than literal "\n" characters in the prompt.
	for _, line := range strings.Split(content, "\n") {
		if err := tmuxSendKeys(ctx, sess.TmuxSessionName, line); err != nil {
			return err
		}
	}
	return nil
}

// Status reports whether the tmux session is still alive.
func (a *Agent) Status(ctx context.Context, sess *pigtail.AgentSession) (pigtail.AgentState, error) {
	if sess.TmuxSessionName == "" {
		return pigtail.AgentStateKilled, nil
	}
	alive, err := tmuxHasSession(ctx, sess.TmuxSessionName)
	if err != nil {
		return "", err
	}
	if !alive {
		return pigtail.AgentStateKilled, nil
	}
	return pigtail.AgentStateRunning, nil
}

// --- tmux helpers ---------------------------------------------------------

func tmuxNewSession(ctx context.Context, name, cwd string, env map[string]string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", cwd}
	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	return run(ctx, "tmux", args...)
}

func tmuxSendKeys(ctx context.Context, name, line string) error {
	return run(ctx, "tmux", "send-keys", "-t", name, line, "Enter")
}

func tmuxKillSession(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "tmux", "kill-session", "-t", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if sessionAlreadyGone(string(out)) {
			return nil // idempotent kill
		}
		return fmt.Errorf("tmux kill-session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sessionAlreadyGone reports whether kill-session's output means the session
// no longer exists: "can't find session" (the server has others), "no server
// running" (no server), or "no current target" (the server is still exiting
// after its last session was killed — a second kill races its shutdown).
func sessionAlreadyGone(out string) bool {
	for _, gone := range []string{"can't find", "no server", "no current target"} {
		if strings.Contains(out, gone) {
			return true
		}
	}
	return false
}

func tmuxHasSession(ctx context.Context, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", name)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// buildClaudeCommand renders the shell command that starts claude inside the
// tmux session. Three modes, by precedence:
//   - PlanPath set  → tell the agent to read & execute the reviewed plan
//   - PlanFirst     → start claude in plan mode (proposes before editing)
//   - otherwise     → run the intent directly (autonomous)
func buildClaudeCommand(intent agent.SpawnIntent) string {
	esc := func(s string) string { return strings.ReplaceAll(s, `'`, `'\''`) }

	if intent.PlanPath != "" {
		prompt := fmt.Sprintf(
			"Read %s in full first, then execute it exactly. Do not rescope or reinterpret. Intent: %s",
			intent.PlanPath, intent.Intent,
		)
		return fmt.Sprintf("claude '%s'", esc(prompt))
	}
	if intent.PlanFirst {
		return fmt.Sprintf("claude --permission-mode plan '%s'", esc(intent.Intent))
	}
	return fmt.Sprintf("claude '%s'", esc(intent.Intent))
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shortID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
