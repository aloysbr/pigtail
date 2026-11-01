// Package activity polls tmux panes of running agent sessions and emits
// workspace.activity events whenever the pane content changes.
//
// This is what makes the TUI triage view feel alive: when Claude writes
// output, the daemon notices on the next poll tick and pushes an event
// over SSE, which causes the TUI to refresh.
package activity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// DefaultInterval is the polling cadence per session.
const DefaultInterval = 5 * time.Second

// Watcher manages one polling goroutine per active AgentSession.
type Watcher struct {
	bus      *events.Bus
	interval time.Duration
	persist  func(sessionID string, state pigtail.AgentState) // optional

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // agent_session_id → stop
}

// New constructs a Watcher with the default interval.
func New(bus *events.Bus) *Watcher {
	return &Watcher{bus: bus, interval: DefaultInterval, cancels: map[string]context.CancelFunc{}}
}

// SetInterval lets tests speed up the cadence.
func (w *Watcher) SetInterval(d time.Duration) { w.interval = d }

// SetPersister wires a callback the watcher calls when it classifies a new
// agent state. Decoupled from the store; the daemon supplies the closure.
func (w *Watcher) SetPersister(f func(sessionID string, state pigtail.AgentState)) {
	w.persist = f
}

// Start begins polling the tmux session of the given AgentSession. It is
// safe to call Start twice with the same session — the second call is a
// no-op.
func (w *Watcher) Start(sess pigtail.AgentSession) {
	if sess.ID == "" || sess.TmuxSessionName == "" {
		return
	}
	w.mu.Lock()
	if _, exists := w.cancels[sess.ID]; exists {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored and invoked by Stop/StopAll
	w.cancels[sess.ID] = cancel
	w.mu.Unlock()

	go w.loop(ctx, sess)
}

// Stop terminates the watcher for one session. No-op if unknown.
func (w *Watcher) Stop(sessionID string) {
	w.mu.Lock()
	cancel, ok := w.cancels[sessionID]
	if ok {
		delete(w.cancels, sessionID)
	}
	w.mu.Unlock()
	if ok {
		cancel()
	}
}

// StopAll cancels every active watcher (call on daemon shutdown).
func (w *Watcher) StopAll() {
	w.mu.Lock()
	for id, cancel := range w.cancels {
		cancel()
		delete(w.cancels, id)
	}
	w.mu.Unlock()
}

// loop runs until the tmux session disappears or ctx is cancelled.
func (w *Watcher) loop(ctx context.Context, sess pigtail.AgentSession) {
	var lastHash string
	var lastState pigtail.AgentState
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			out, err := capturePane(ctx, sess.TmuxSessionName)
			if err != nil {
				// Session likely gone — emit a final hint and stop.
				w.bus.Publish(pigtail.Event{
					Type: pigtail.EventAgentCrashed,
					Data: map[string]interface{}{
						"agent_session_id": sess.ID,
						"workspace_id":     sess.WorkspaceID,
						"reason":           "tmux session unavailable",
					},
				})
				w.Stop(sess.ID)
				return
			}
			hash := sha256.Sum256([]byte(out))
			h := hex.EncodeToString(hash[:8])
			if h == lastHash {
				continue
			}
			lastHash = h
			state := classifyPane(out)
			if state != lastState {
				lastState = state
				if w.persist != nil {
					w.persist(sess.ID, state)
				}
			}
			w.bus.Publish(pigtail.Event{
				Type: pigtail.EventWorkspaceActivity,
				Data: map[string]interface{}{
					"workspace_id":     sess.WorkspaceID,
					"agent_session_id": sess.ID,
					"summary":          lastNonEmpty(out),
					"state":            string(state),
				},
			})
		}
	}
}

// capturePane returns the visible content of the tmux pane attached to
// the given session.
func capturePane(ctx context.Context, sessionName string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-t", sessionName)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// classifyPane inspects captured tmux pane text and maps it to an AgentState.
// It only distinguishes "blocked on a prompt" (paused) from "working"
// (running). Best-effort: depends on Claude Code's TUI rendering, so all
// patterns are centralised here for easy maintenance.
func classifyPane(pane string) pigtail.AgentState {
	lower := strings.ToLower(pane)
	// Plan-approval prompt is a more specific case of a Yes/No prompt.
	if strings.Contains(lower, "auto-accept edits") || strings.Contains(lower, "manually approve") {
		return pigtail.AgentStatePausedPlan
	}
	if strings.Contains(lower, "do you want to proceed") ||
		strings.Contains(lower, "use skill \"") ||
		(strings.Contains(pane, "❯") && (strings.Contains(lower, "1. yes") || strings.Contains(lower, "(y/n)"))) {
		return pigtail.AgentStatePausedInput
	}
	return pigtail.AgentStateRunning
}

// lastNonEmpty returns the last non-empty line of the captured pane.
// Truncated to a reasonable display size.
func lastNonEmpty(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > 120 {
			line = line[:119] + "…"
		}
		return line
	}
	return ""
}
