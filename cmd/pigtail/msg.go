package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/presence"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// msgTimeout bounds every daemon call of `pigtail msg` (not the snapshot's
// cmux calls, which carry their own bound). A variable only so a test that
// drives the whole command under a loaded machine (make check, -race: the
// snapshot's process listing alone can eat the budget) can widen it.
var msgTimeout = 5 * time.Second

func msgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "msg",
		Short: "Relay short coordination messages between Claude Code sessions",
		Long: `Relay short coordination messages between Claude Code sessions.

A message is for coordination only: a shared file or checkout, a worktree or
port held or released, a server restarted, "about to touch X". It is never an
approval: the recipient is told it comes from a peer, not from the user.

Addresses, tried in order (the first level with a match decides; several
matches fail and list the candidates): subject slug or sub_ id, pigtail
worktree name, cmux workspace title (substring), Claude's native session name,
session-id prefix. See the live sessions with ` + "`pigtail session ls`" + `.

Limits (enforced by the daemon): 2 KB per message, no secrets, 5 unread per
recipient, 5 messages per 10 min to the same session, 6 messages per thread
(then escalate to the user), no cross-scope messages, 24 h TTL by default.`,
	}
	cmd.AddCommand(msgSendCmd(), msgReplyCmd(), msgInboxCmd(), msgShowCmd(), msgThreadCmd(), msgAckCmd(), msgLogCmd())
	return cmd
}

// --- identity & registry -----------------------------------------------------

// selfEnv is what the calling session knows about itself from its environment.
// Environment, not process ancestry: the Bash tool of Claude Code detaches
// every command (setsid), so no walk of the process tree reaches claude.
type selfEnv struct {
	SessionID   string
	ConfigDir   string
	Cwd         string
	WorkspaceID string
	SurfaceID   string
	PID         int
}

func readSelfEnv(getenv func(string) string) selfEnv {
	s := selfEnv{
		SessionID:   getenv("CLAUDE_CODE_SESSION_ID"),
		ConfigDir:   claudeConfigDir(getenv),
		WorkspaceID: getenv(cmux.EnvWorkspaceID),
		SurfaceID:   getenv(cmux.EnvSurfaceID),
	}
	s.Cwd, _ = os.Getwd()
	if pid, err := strconv.Atoi(getenv("CLAUDE_PID")); err == nil {
		s.PID = pid
	}
	return s
}

// selfPeer turns the environment into a peer row: the one registry entry that
// never depends on cmux answering.
func selfPeer(s selfEnv, cells *claudeprofile.Table) pigtail.PeerSession {
	p := pigtail.PeerSession{
		SessionID: s.SessionID, ConfigDir: s.ConfigDir, Cwd: s.Cwd,
		CmuxWorkspaceID: s.WorkspaceID, CmuxSurfaceID: s.SurfaceID, Source: "self",
	}
	var n claudesession.Native
	ok := false
	if s.PID > 0 {
		n, ok = claudesession.ReadNative(s.ConfigDir, s.PID)
		ok = ok && n.SessionID == s.SessionID
	}
	if !ok {
		n, ok = claudesession.FindNative(s.ConfigDir, s.SessionID)
	}
	if ok {
		p.PID, p.Name, p.Status = n.PID, n.Name, n.Status
	}
	if c, ok := cells.ByConfigDir(s.ConfigDir); ok {
		p.CellID, p.Scope = c.ID, c.Scope
	}
	return p
}

// snapshot derives the registry from cmux + the config dirs, CLI-side (the
// daemon execs cmux only from its sidebar loop, when switched on), with the
// calling session merged in and each
// session's last worktree attached from the presence markers. Every source is
// best-effort: a missing cmux leaves the processes and the caller itself. It
// also returns the presence report, which relayPeers overlays on the daemon's
// echo.
func snapshot(self selfEnv, warn io.Writer) ([]pigtail.PeerSession, presence.Report) {
	cells, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil && warn != nil {
		fmt.Fprintf(warn, "warning: %v — scopes unknown, messages will be refused\n", err)
	}
	in := peers.Inputs{Cells: cells}
	if b, err := cmux.SessionsJSON(); err == nil {
		in.Cmux, _ = peers.ParseCmuxSessions(b)
	}
	if out, err := cmux.WorkspaceList(); err == nil {
		in.Titles = peers.ParseWorkspaceTitles(out)
	}
	if live, err := claudesession.LiveSessions(cells.ConfigDirs()); err == nil {
		in.Live = live
	}
	ps := peers.Build(in)
	if self.SessionID != "" {
		me := selfPeer(self, cells)
		found := false
		for i := range ps {
			if ps[i].SessionID == me.SessionID {
				found = true
				mergePeer(&ps[i], me)
			}
		}
		if !found {
			ps = append(ps, me)
		}
	}
	dir := presence.Dir(os.Getenv)
	rep, perr := presence.Scan(dir)
	if warn != nil {
		warnPresence(warn, dir, rep, perr)
	}
	peers.AttachMarkers(ps, rep.Markers)
	return ps, rep
}

// warnPresence reports what makes the WORKTREE fallback blind. A deleted
// worktree is normal life and stays quiet, and so does old junk (a malformed
// file older than the newest valid marker — e.g. a hook test fixture left in
// the real TMPDIR). A malformed file NEWER than every valid marker, or
// malformed files and no valid one, is the signature of the presence hook script
// changing its format: that one speaks.
func warnPresence(warn io.Writer, dir string, r presence.Report, err error) {
	if err != nil {
		fmt.Fprintf(warn, "warning: presence markers unreadable in %s: %v — WORKTREE falls back to the launch cwd only\n", dir, err)
		return
	}
	if r.Malformed > 0 && (len(r.Markers) == 0 || r.NewestMalformed.After(r.NewestValid)) {
		fmt.Fprintf(warn, "warning: %d presence marker(s) in %s do not follow the presence hook's format (claude-wt-<key8>-<session>, line 1 = absolute path), the newest from %s — has the hook changed?\n",
			r.Malformed, dir, r.NewestMalformed.Format(time.RFC3339))
	}
}

// mergePeer fills dst's empty fields from src.
func mergePeer(dst *pigtail.PeerSession, src pigtail.PeerSession) {
	fill := func(d *string, s string) {
		if *d == "" {
			*d = s
		}
	}
	fill(&dst.ConfigDir, src.ConfigDir)
	fill(&dst.Cwd, src.Cwd)
	fill(&dst.CmuxWorkspaceID, src.CmuxWorkspaceID)
	fill(&dst.CmuxSurfaceID, src.CmuxSurfaceID)
	fill(&dst.Name, src.Name)
	fill(&dst.Status, src.Status)
	fill(&dst.CellID, src.CellID)
	fill(&dst.Scope, src.Scope)
	if dst.PID == 0 {
		dst.PID = src.PID
	}
}

// resolveSelf names the calling session: CLAUDE_CODE_SESSION_ID when set,
// else the registry row on the caller's cmux surface.
func resolveSelf(self selfEnv, ps []pigtail.PeerSession) (string, error) {
	if self.SessionID != "" {
		return self.SessionID, nil
	}
	if self.SurfaceID != "" {
		for _, p := range ps {
			if strings.EqualFold(p.CmuxSurfaceID, self.SurfaceID) {
				return p.SessionID, nil
			}
		}
	}
	return "", errors.New("cannot tell which Claude session is calling: run this from inside a Claude Code session (CLAUDE_CODE_SESSION_ID unset, no cmux surface match)")
}

// register snapshots the registry, posts it, and returns the caller's id.
func register(ctx context.Context, c *pigtail.Client, warn io.Writer) (string, error) {
	self := readSelfEnv(os.Getenv)
	ps, _ := snapshot(self, warn)
	me, err := resolveSelf(self, ps)
	if err != nil {
		return "", err
	}
	// The daemon budget starts after the snapshot: a slow cmux must not eat it.
	pctx, cancel := context.WithTimeout(ctx, msgTimeout)
	defer cancel()
	if _, err := c.PostPeers(pctx, pigtail.PeersRequest{Peers: peers.LiveOnly(ps, peers.Alive, self.SessionID)}); err != nil {
		return "", fmt.Errorf("register sessions with the daemon: %w", err)
	}
	return me, nil
}

// asRegisteredSelf runs call as the calling session (sessionClient). The
// daemon answers NOT_FOUND for a session it does not know — one whose
// SessionStart hook never reached it — so on that answer the session registers
// itself (its own row, no cmux snapshot) and call runs once more. A session of
// another scope stays NOT_FOUND: its registered scope is kept.
func asRegisteredSelf(ctx context.Context, me string, call func(*pigtail.Client) error) error {
	c := sessionClient(os.Getenv)
	err := call(c)
	if !isNotFound(err) {
		return err
	}
	self := readSelfEnv(os.Getenv)
	self.SessionID = me
	cells, _ := claudeprofile.Load(setup.ConfigDir())
	if _, perr := c.PostPeers(ctx, pigtail.PeersRequest{Peers: []pigtail.PeerSession{selfPeer(self, cells)}}); perr != nil {
		return err
	}
	return call(c)
}

// whoami returns the caller's session id without a full snapshot (read-only
// commands).
func whoami() (string, error) {
	self := readSelfEnv(os.Getenv)
	if self.SessionID != "" {
		return self.SessionID, nil
	}
	ps, _ := snapshot(self, nil)
	return resolveSelf(self, ps)
}

// --- send / reply ---------------------------------------------------------------

func readBody(args []string, stdin io.Reader) (string, error) {
	if len(args) > 1 || (len(args) == 1 && args[0] != "-") {
		return strings.Join(args, " "), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, relay.MaxBodyBytes*2))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func msgSendCmd() *cobra.Command {
	var (
		kind       string
		ttl        time.Duration
		all        bool
		crossScope bool
	)
	cmd := &cobra.Command{
		Use:   "send <to>|--all [text…]",
		Short: "Send a message to another Claude session, or a service notice to every live session (--all); text from args or stdin",
		Long: "--all sends a one-way service notice (kind notice: no reply) to every live session of your scope.\n" +
			"From the master cell, --all --cross-scope reaches every scope; only a notice crosses scopes.\n" +
			"--all fails (exit 1) only when no notice was sent; each refused recipient is named on its line.",
		Annotations: sessionWrite(),
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				return nil
			}
			return cobra.MinimumNArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			text := args
			if !all {
				text = args[1:]
			}
			body, err := readBody(text, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if all && cmd.Flags().Changed("kind") && kind != pigtail.MessageKindNotice {
				return fmt.Errorf("--all sends a service notice only: drop --kind, or use --kind notice")
			}
			c := sessionClient(os.Getenv)
			me, err := register(cmd.Context(), c, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout+5*time.Second)
			defer cancel()
			sender := c
			if crossScope {
				sender = c.WithCrossScopeWrite()
			}
			if all {
				res, err := sender.BroadcastMessage(ctx, pigtail.BroadcastRequest{From: me, Body: body, TTLSeconds: int(ttl / time.Second)})
				if err != nil {
					return err
				}
				return reportBroadcast(cmd.OutOrStdout(), cmd.ErrOrStderr(), res)
			}
			res, err := sender.SendMessage(ctx, pigtail.SendMessageRequest{
				From: me, To: args[0], Kind: kind, Body: body, TTLSeconds: int(ttl / time.Second),
			})
			if err != nil {
				return err
			}
			reportSent(cmd.OutOrStdout(), cmd.ErrOrStderr(), res)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "info", "info | request | notice (one-way, no reply)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "time to live (default 24h, max 7 days)")
	cmd.Flags().BoolVar(&all, "all", false, "send a service notice to every live session of your scope (no <to>)")
	cmd.Flags().BoolVar(&crossScope, "cross-scope", false, "master cell: let a notice reach another scope (with --all: every scope)")
	return cmd
}

func msgReplyCmd() *cobra.Command {
	var ack bool
	cmd := &cobra.Command{
		Use:   "reply <msg_id> [text…]",
		Short: "Reply to a message you received (same thread, back to its sender)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readBody(args[1:], cmd.InOrStdin())
			if err != nil {
				return err
			}
			c := sessionClient(os.Getenv)
			me, err := register(cmd.Context(), c, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout+5*time.Second)
			defer cancel()
			kind := pigtail.MessageKindReply
			if ack {
				kind = pigtail.MessageKindAck
			}
			res, err := c.SendMessage(ctx, pigtail.SendMessageRequest{From: me, InReplyTo: args[0], Kind: kind, Body: body})
			if err != nil {
				return err
			}
			reportSent(cmd.OutOrStdout(), cmd.ErrOrStderr(), res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ack, "ack", false, "send as kind=ack (a closing acknowledgement)")
	return cmd
}

// reportSent prints the outcome — with a warning when the recipient will not
// wake on its own (res.Delivery) — and sets the inbox pill on the
// recipient's workspace, so a human sees the message even when the recipient
// session is idle and no hook runs; pigtail posts no cmux notification (D2).
func reportSent(out, warn io.Writer, res *pigtail.SendMessageResponse) {
	m := res.Message
	fmt.Fprintf(out, "sent %s → %s (thread %s, %d unread there)\n", m.ID, m.ToLabel, m.ThreadID, res.Pending)
	if res.MatchedBy == peers.LevelLastWorktree && res.To.LastWorktreeAt != nil {
		fmt.Fprintf(out, "  matched by its last worktree marker (%s, %s old) — not by its cwd\n",
			res.To.LastWorktree, peers.Age(time.Since(*res.To.LastWorktreeAt)))
	}
	if res.WillNotWake() {
		// stdout, next to the sent line: it qualifies the outcome, and the
		// sending agent must see it even when it drops stderr.
		if res.Delivery == pigtail.DeliveryNone {
			fmt.Fprintf(out, "⚠ %s is idle with no watcher: it will NOT wake up on its own.\n", m.ToLabel)
		} else {
			fmt.Fprintf(out, "⚠ %s has no watcher and its state is unknown: it may NOT wake up on its own.\n", m.ToLabel)
		}
		fmt.Fprintf(out, "  Ask %s to type \"lis ta boîte pigtail\" in its window.\n", humanName())
	}
	if res.Delivery == pigtail.DeliveryWake {
		// D28: pigtaild types the trigger itself, under its screen guard —
		// nothing to hand to the user.
		fmt.Fprintf(out, "⏰ %s is idle: pigtail will wake it (screen guard).\n", m.ToLabel)
	}
	setInboxPill(warn, res.To.CmuxWorkspaceID, res.Pending)
}

// setInboxPill sets the unread pill on a recipient's cmux workspace, so a
// human sees the message even when no hook runs there (D2 of the relay).
func setInboxPill(warn io.Writer, workspaceID string, pending int) {
	if strings.EqualFold(os.Getenv("PIGTAIL_RELAY_NOTIFY"), "off") || workspaceID == "" {
		return
	}
	if err := cmux.SetInboxStatus(workspaceID, pending); err != nil {
		fmt.Fprintf(warn, "warning: cmux set-status: %v\n", err)
	}
}

// reportBroadcast prints one line per recipient — sent with its id (and a
// warning when it will not wake on its own), or refused with its code and
// reason — then a total. It returns an error, hence exit 1, only when no
// notice was sent: a broadcast that reached nobody is never read as done (T3).
func reportBroadcast(out, warn io.Writer, res *pigtail.BroadcastResponse) error {
	asleep := 0
	for _, r := range res.Results {
		if r.Refused != "" {
			fmt.Fprintf(out, "✗ %s — refused (%s): %s\n", r.To.Label, r.Refused, r.Reason)
			continue
		}
		fmt.Fprintf(out, "✓ %s — %s\n", r.To.Label, r.MessageID)
		switch r.Delivery {
		case pigtail.DeliveryNone:
			asleep++
			fmt.Fprintf(out, "  ⚠ idle with no watcher: it will NOT wake up on its own.\n")
		case pigtail.DeliveryUnknown:
			asleep++
			fmt.Fprintf(out, "  ⚠ no watcher and its state is unknown: it may NOT wake up on its own.\n")
		case pigtail.DeliveryWake:
			fmt.Fprintf(out, "  ⏰ %s is idle: pigtail will wake it (screen guard).\n", r.To.Label)
		}
		setInboxPill(warn, r.To.CmuxWorkspaceID, r.Pending)
	}
	fmt.Fprintf(out, "notice: %d sent, %d refused, %d will not wake on its own\n", res.Sent, res.Refused, asleep)
	if asleep > 0 {
		fmt.Fprintln(out, `  Those wait until a human types "lis ta boîte pigtail" in their window.`)
	}
	if res.OtherScopesSkipped > 0 { // C7: the master forgot the opt-in
		fmt.Fprintf(out, "%d live sessions of other scopes not reached — add --cross-scope\n", res.OtherScopesSkipped)
	}
	switch {
	case len(res.Results) == 0:
		return errors.New("no notice sent: no live session to notify")
	case res.Sent == 0:
		return errors.New("no notice sent: every recipient refused it (see above)")
	}
	return nil
}

// --- reading ----------------------------------------------------------------

func msgInboxCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:         "inbox",
		Short:       "Deliver and print this session's unread messages (--all: history, nothing claimed)",
		Args:        cobra.NoArgs,
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			me, err := whoami()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout)
			defer cancel()
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if all {
				ms, err := c.Messages(ctx, pigtail.MessageFilter{Session: me, Limit: 50})
				if err != nil {
					return err
				}
				printMessages(cmd.OutOrStdout(), ms)
				return nil
			}
			var ms []pigtail.Message
			err = asRegisteredSelf(ctx, me, func(sc *pigtail.Client) error {
				ms, err = sc.ClaimMessages(ctx, me, "cli")
				return err
			})
			if err != nil {
				return err
			}
			if len(ms) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no unread message")
				return nil
			}
			_ = cmux.ClearInboxStatus(os.Getenv(cmux.EnvWorkspaceID))
			fmt.Fprintln(cmd.OutOrStdout(), relay.FrameAll(ms))
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list the last 50 messages sent or received, whatever their state")
	return cmd
}

func msgShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "show <msg_id>",
		Short:       "Show one message",
		Args:        cobra.ExactArgs(1),
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout)
			defer cancel()
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			m, err := c.Message(ctx, args[0])
			if err != nil {
				return err
			}
			printMessage(cmd.OutOrStdout(), *m)
			return nil
		},
	}
}

func msgThreadCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "thread <msg_id|thr_id>",
		Short:       "Show a whole thread, oldest first",
		Args:        cobra.ExactArgs(1),
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout)
			defer cancel()
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			thread := args[0]
			if !strings.HasPrefix(thread, pigtail.PrefixThread) {
				m, err := c.Message(ctx, thread)
				if err != nil {
					return err
				}
				thread = m.ThreadID
			}
			ms, err := c.Messages(ctx, pigtail.MessageFilter{Thread: thread})
			if err != nil {
				return err
			}
			for i, m := range ms {
				if i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				printMessage(cmd.OutOrStdout(), m)
			}
			return nil
		},
	}
}

func msgAckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ack <msg_id>",
		Short: "Acknowledge a message you received (closes the loop without a reply)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			me, err := whoami()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout)
			defer cancel()
			var m *pigtail.Message
			err = asRegisteredSelf(ctx, me, func(sc *pigtail.Client) error {
				m, err = sc.AckMessage(ctx, args[0], me)
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "acked %s from %s\n", m.ID, m.FromLabel)
			return nil
		},
	}
}

func msgLogCmd() *cobra.Command {
	var (
		subject string
		since   time.Duration
		limit   int
	)
	cmd := &cobra.Command{
		Use:         "log",
		Short:       "Audit trail of relayed messages (every session of the scopes this cell may read)",
		Args:        cobra.NoArgs,
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), msgTimeout)
			defer cancel()
			f := pigtail.MessageFilter{Subject: subject, Limit: limit}
			if since > 0 {
				f.Since = time.Now().Add(-since)
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			ms, err := c.Messages(ctx, f)
			if err != nil {
				return err
			}
			printMessages(cmd.OutOrStdout(), ms)
			return nil
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "", "only messages attached to this subject id")
	cmd.Flags().DurationVar(&since, "since", 0, "only messages newer than this (e.g. 24h)")
	cmd.Flags().IntVar(&limit, "limit", 100, "at most this many (most recent)")
	return cmd
}

func printMessages(w io.Writer, ms []pigtail.Message) {
	if len(ms) == 0 {
		fmt.Fprintln(w, "no message")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWHEN\tKIND\tSTATE\tFROM\tTO\tTEXT")
	for _, m := range ms {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.CreatedAt.Local().Format("01-02 15:04"), m.Kind, m.State,
			m.FromLabel, m.ToLabel, cmux.OneLine(relay.FirstLine(m.Body, 0), 60))
	}
	tw.Flush()
}

func printMessage(w io.Writer, m pigtail.Message) {
	fmt.Fprintf(w, "%s  %s  %s  thread %s\n", m.ID, m.Kind, m.State, m.ThreadID)
	fmt.Fprintf(w, "from %s → %s  at %s\n", m.FromLabel, m.ToLabel, m.CreatedAt.Local().Format(time.RFC3339))
	if m.InReplyTo != "" {
		fmt.Fprintf(w, "in reply to %s\n", m.InReplyTo)
	}
	if m.DeliveredAt != nil {
		fmt.Fprintf(w, "delivered %s via %s\n", m.DeliveredAt.Local().Format(time.RFC3339), m.DeliveredVia)
	}
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w, m.Body)
}

// humanName is the human's name from settings.yaml, else the neutral default
// (a bad file is the daemon's log line, not the CLI's).
func humanName() string {
	s, _ := usersettings.Load(setup.ConfigDir())
	return s.Human.Name
}
