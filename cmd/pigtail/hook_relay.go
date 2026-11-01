package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Claude Code hooks of the session-message relay. Every one fails open: a
// relay problem must never block a prompt, a tool call or a session start.
// Every one speaks as its session (sessionClient: the cell of
// CLAUDE_CONFIG_DIR, inherited from claude): the daemon lets a caller act as a
// session only from that session's own scope.
//
//	claude-session      SessionStart (and SessionEnd with --end): register the
//	                    session in the relay's registry.
//	claude-inbox        UserPromptSubmit + PostToolUse: inject unread messages
//	                    as additionalContext. Fast path: no marker file, exit.
//	claude-inbox-wait   Stop, asyncRewake: wait for a message while the session
//	                    is idle, and wake it (exit 2) when one arrives.
//	claude-wait         PermissionRequest, Notification, PreToolUse
//	                    (AskUserQuestion|ExitPlanMode), UserPromptSubmit: record
//	                    what the session waits for, or clear it at the next
//	                    prompt (session_wait.go).

const (
	relayHookTimeout = 2 * time.Second
	// waitDefaultBudget stays under the Stop hook's configured timeout (3600 s
	// in settings-hooks-relay.json): Claude Code kills a hook at its timeout
	// silently, so the watcher leaves on its own first and the next Stop
	// re-arms a fresh one.
	waitDefaultBudget = 58 * time.Minute
	waitPoll          = 50 * time.Second
	waitRetry         = 10 * time.Second
)

func addRelayHooks(cmd *cobra.Command) {
	var end bool
	session := &cobra.Command{
		Use:   "claude-session",
		Short: "Claude Code SessionStart/SessionEnd hook: register the session with the message relay",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runSessionHook(os.Stdin, os.Getenv, end)
			return nil
		},
	}
	session.Flags().BoolVar(&end, "end", false, "SessionEnd: mark the session ended")
	cmd.AddCommand(session)

	cmd.AddCommand(&cobra.Command{
		Use:   "claude-inbox",
		Short: "Claude Code UserPromptSubmit/PostToolUse hook: inject unread peer messages",
		Long: `Injects this session's unread relay messages as additionalContext, framed as
coming from a peer session (never from the user). Fast path: when the inbox
marker file for the session does not exist, it exits at once without calling
the daemon. A session in Standby is served only on a prompt typed by the user
(never on /compact or a tool call). PIGTAIL_RELAY=off disables it.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runInboxHook(os.Stdin, os.Stdout, os.Getenv)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "claude-inbox-wait",
		Short: "Claude Code Stop hook (asyncRewake): wake an idle session when a peer message arrives",
		Long: `Waits, while the session is idle, for a relay message; on one it prints the
framed message on stderr and exits 2, which with asyncRewake wakes the session
(recorded as a task notification, never as a user turn). Exits 0 on its own
before the hook timeout, when the claude process dies, when a newer watcher
for the same session takes over, or when the session is not interactive.
PIGTAIL_RELAY_REWAKE=off disables it; PIGTAIL_RELAY_WAIT_BUDGET overrides the
58m budget.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if code := runInboxWait(os.Stdin, os.Stderr, os.Getenv, time.Now); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	})

	cmd.AddCommand(waitHookCmd())
}

// relayHookPayload is the part of a Claude Code hook payload the relay reads.
type relayHookPayload struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
	Prompt        string `json:"prompt"` // UserPromptSubmit: what was typed
}

func readRelayPayload(stdin io.Reader) (relayHookPayload, bool) {
	var p relayHookPayload
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&p); err != nil || p.SessionID == "" {
		return p, false
	}
	return p, true
}

// inboxMarkerDir must agree with the daemon's RelayConfig.MarkerDir: the
// directory of the socket, plus "inbox".
func inboxMarkerDir(getenv func(string) string) string {
	if d := getenv("PIGTAIL_INBOX_DIR"); d != "" {
		return d
	}
	if s := socketPath(); s != "" {
		return filepath.Join(filepath.Dir(s), "inbox")
	}
	return filepath.Join(setup.ConfigDir(), "inbox")
}

func relayOff(getenv func(string) string, key string) bool {
	return strings.EqualFold(strings.TrimSpace(getenv(key)), "off")
}

// --- claude-session --------------------------------------------------------------

func runSessionHook(stdin io.Reader, getenv func(string) string, end bool) {
	if relayOff(getenv, "PIGTAIL_RELAY") {
		return
	}
	p, ok := readRelayPayload(stdin)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), relayHookTimeout)
	defer cancel()
	if end {
		removeBgRecord(bgWorkDir(getenv), p.SessionID) // its Stop record ends with it (D42)
		_, _ = sessionClient(getenv).PostPeers(ctx, pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: p.SessionID}}})
		return
	}
	registerSession(ctx, p, getenv)
}

// registerSession posts the session of a hook payload to the registry, as
// that session (its cell's scope, which the row records too).
func registerSession(ctx context.Context, p relayHookPayload, getenv func(string) string) {
	self := readSelfEnv(getenv)
	self.SessionID = p.SessionID
	if p.Cwd != "" {
		self.Cwd = p.Cwd
	}
	cells, _ := claudeprofile.Load(setup.ConfigDir())
	peer := selfPeer(self, cells)
	peer.Source = "hook"
	// Nothing on stdout: a SessionStart hook's stdout is added to the context.
	_, _ = sessionClient(getenv).PostPeers(ctx, pigtail.PeersRequest{Peers: []pigtail.PeerSession{peer}})
}

// --- claude-inbox ------------------------------------------------------------------

func runInboxHook(stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	if relayOff(getenv, "PIGTAIL_RELAY") {
		return
	}
	p, ok := readRelayPayload(stdin)
	if !ok || !validMarkerKey(p.SessionID) {
		return
	}
	if _, err := os.Stat(filepath.Join(inboxMarkerDir(getenv), p.SessionID)); err != nil {
		return // fast path: nothing pending for this session
	}
	if inStandby(getenv, p.SessionID) && (p.HookEventName != "UserPromptSubmit" || !standbyEnds(p.Prompt)) {
		// D16: a session in Standby gets its inbox at the user's prompt only —
		// never on the /compact pigtail types or on a tool call. Unread,
		// its marker and pill stay.
		return
	}
	via := "tool"
	if p.HookEventName == "UserPromptSubmit" {
		via = "prompt"
	}
	ctx, cancel := context.WithTimeout(context.Background(), relayHookTimeout)
	defer cancel()
	ms, err := sessionClient(getenv).ClaimMessages(ctx, p.SessionID, via)
	if err != nil || len(ms) == 0 {
		return
	}
	_ = cmux.ClearInboxStatus(getenv(cmux.EnvWorkspaceID))
	event := p.HookEventName
	if event == "" {
		event = "PostToolUse"
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     event,
		"additionalContext": inboxPreamble(ms) + relay.FrameAll(ms),
	}})
}

// inboxPreamble counts the claimed messages; the human's answers to a wait
// (HumanSender) are counted apart, under the name pigtaild stamped on them.
func inboxPreamble(ms []pigtail.Message) string {
	answers := 0
	name := ""
	for _, m := range ms {
		if m.FromSession == pigtail.HumanSender {
			answers++
			if name == "" {
				name = strings.TrimSpace(m.FromLabel)
			}
		}
	}
	if name == "" {
		name = relay.DefaultHumanName
	}
	others := len(ms) - answers
	if answers == 0 {
		if others == 1 {
			return "pigtail relay: 1 message from another Claude session.\n"
		}
		return fmt.Sprintf("pigtail relay: %d messages from other Claude sessions.\n", others)
	}
	s := "pigtail relay: 1 answer from " + name
	if answers > 1 {
		s = fmt.Sprintf("pigtail relay: %d answers from %s", answers, name)
	}
	switch {
	case others == 1:
		s += ", 1 message from another session"
	case others > 1:
		s += fmt.Sprintf(", %d messages from other sessions", others)
	}
	return s + ".\n"
}

func validMarkerKey(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..")
}

// --- claude-inbox-wait -------------------------------------------------------------

// waitDeps are the seams of runInboxWait.
type waitDeps struct {
	client  *pigtail.Client
	alive   func() bool
	sleep   func(time.Duration)
	ownerOK func() bool
	// register (re)registers the session when the daemon does not know it —
	// its SessionStart ran while the daemon was down, or the database was
	// restored. nil: just retry.
	register func()
	// waiting, when set, is called right before each long poll leaves for
	// the daemon. A test seam only (production leaves it nil): a test waits
	// for it instead of sleeping a guessed delay before acting on the wait.
	waiting func()
}

func runInboxWait(stdin io.Reader, stderr io.Writer, getenv func(string) string, now func() time.Time) int {
	if relayOff(getenv, "PIGTAIL_RELAY_REWAKE") || relayOff(getenv, "PIGTAIL_RELAY") {
		return 0
	}
	p, ok := readRelayPayload(stdin)
	if !ok || !validMarkerKey(p.SessionID) {
		return 0
	}
	self := readSelfEnv(getenv)
	n, ok := findClaude(self, p.SessionID)
	if !ok {
		return 0 // no claude process to watch: nothing to wake
	}
	if n.Kind != "" && n.Kind != "interactive" {
		return 0 // -p / SDK run: asyncRewake runs synchronously there, never block it
	}
	budget := waitDefaultBudget
	if d, err := time.ParseDuration(getenv("PIGTAIL_RELAY_WAIT_BUDGET")); err == nil && d > 0 {
		budget = d
	}
	owner := filepath.Join(inboxMarkerDir(getenv), ".wait-"+p.SessionID)
	if err := claimWatcher(owner); err != nil {
		return 0
	}
	defer releaseWatcher(owner)
	deps := waitDeps{
		client:  sessionClient(getenv),
		alive:   func() bool { return claudesession.SessionAlive(n.ConfigDir, n.PID, p.SessionID) },
		sleep:   time.Sleep,
		ownerOK: func() bool { return ownsWatcher(owner) },
		register: func() {
			ctx, cancel := context.WithTimeout(context.Background(), relayHookTimeout)
			defer cancel()
			registerSession(ctx, p, getenv)
		},
	}
	return waitLoop(p.SessionID, stderr, deps, now, now().Add(budget))
}

// findClaude finds the claude process serving the session: CLAUDE_PID when it
// names this session, else a scan of the config dir's sessions/*.json.
func findClaude(self selfEnv, sessionID string) (claudesession.Native, bool) {
	if self.PID > 0 {
		if n, ok := claudesession.ReadNative(self.ConfigDir, self.PID); ok && n.SessionID == sessionID {
			return n, true
		}
	}
	n, ok := claudesession.FindNative(self.ConfigDir, sessionID)
	if !ok || !claudesession.PidAlive(n.PID) {
		return claudesession.Native{}, false
	}
	return n, true
}

func waitLoop(session string, stderr io.Writer, d waitDeps, now func() time.Time, deadline time.Time) int {
	for {
		remaining := deadline.Sub(now())
		if remaining <= time.Second || !d.alive() || !d.ownerOK() {
			return 0
		}
		poll := waitPoll
		if remaining < poll {
			poll = remaining
		}
		ctx, cancel := context.WithTimeout(context.Background(), poll+15*time.Second)
		stop := watchWhileWaiting(ctx, cancel, d)
		if d.waiting != nil {
			d.waiting()
		}
		res, err := d.client.WaitMessages(ctx, session, poll)
		stop()
		cancel()
		if err != nil {
			if !d.alive() || !d.ownerOK() {
				return 0
			}
			if isNotFound(err) && d.register != nil {
				d.register()
			}
			d.sleep(minDuration(waitRetry, deadline.Sub(now())))
			continue
		}
		if res.Superseded {
			return 0
		}
		if len(res.Messages) > 0 {
			fmt.Fprint(stderr, "pigtail relay: while you were idle, "+strings.TrimPrefix(inboxPreamble(res.Messages), "pigtail relay: "))
			fmt.Fprintln(stderr, relay.FrameAll(res.Messages))
			return 2
		}
	}
}

// livenessCheck is how often a waiting watcher re-checks that its claude
// process is alive and that it still owns the session: a dead session or a
// replaced watcher must not hold a long poll for up to waitPoll.
var livenessCheck = 3 * time.Second

// watchWhileWaiting cancels the in-flight wait as soon as the claude process
// dies or a newer watcher takes over. The returned func stops the check.
func watchWhileWaiting(ctx context.Context, cancel context.CancelFunc, d waitDeps) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(livenessCheck)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if !d.alive() || !d.ownerOK() {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func minDuration(a, b time.Duration) time.Duration {
	if b < a {
		if b < 0 {
			return 0
		}
		return b
	}
	return a
}

// The watcher ownership file holds the pid of the session's current Stop
// watcher. Every Stop spawns a watcher (the spike saw them pile up); the new
// one writes its pid and the old one, seeing it no longer owns the file,
// leaves at its next turn — the daemon's one-waiter-per-session rule makes
// that turn immediate.

func claimWatcher(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func ownsWatcher(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid())
}

func releaseWatcher(path string) {
	if ownsWatcher(path) {
		_ = os.Remove(path)
	}
}
