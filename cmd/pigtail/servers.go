package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func serversCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "List running dev servers with RAM usage",
		RunE: func(_ *cobra.Command, _ []string) error {
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := c.Servers(cx)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(resp)
			}
			// Best-effort: the offsite verdict is a second, smaller signal
			// riding along with the dev-server listing. A daemon that cannot
			// answer GET /backup (old version, transient error) must not
			// take down the command people run constantly to see their
			// servers — it just means this run says nothing about backups,
			// same as an unconfigured destination would.
			var offsite *pigtail.OffsiteView
			if backup, err := c.Backup(cx); err == nil {
				offsite = backup.Offsite
			}
			printServers(os.Stdout, resp, offsite)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func printServers(w io.Writer, resp pigtail.ServersResponse, offsite *pigtail.OffsiteView) {
	fmt.Fprintf(w, "available=%dMiB · floor=%dMiB\n", resp.AvailableBytes>>20, resp.FloorBytes>>20)
	if len(resp.Servers) == 0 {
		fmt.Fprintln(w, "no running dev servers")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVER\tPORT\tMEM(MiB)\tIDLE\tPIN\tOWNER")
		myHost, _ := os.Hostname()
		now := serversNow()
		// A listed server whose (workspace, port) is in conflict carries a
		// marker; the fix is printed in the "port conflicts:" block below.
		conflicted := map[[2]string]bool{}
		for _, c := range resp.PortConflicts {
			conflicted[[2]string{c.WorkspaceID, strconv.Itoa(c.Port)}] = true
		}
		for _, s := range resp.Servers {
			idle := (time.Duration(s.IdleSeconds) * time.Second).Round(time.Second)
			owner := shortOwner(s.Owner, myHost)
			if s.Owner != "" && !s.OwnerAlive {
				owner += " †"
			}
			name := s.WorkspaceID + ":" + s.Server
			if conflicted[[2]string{s.WorkspaceID, strconv.Itoa(s.Port)}] {
				name += " ⚠port"
			}
			fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\n",
				name, s.Port, s.RSSBytes>>20, idle, pinColumn(s, now), owner)
		}
		tw.Flush()
	}

	// Only printed when there is at least one recent death — no title, no
	// "none" line otherwise. A permanent section that usually says "nothing"
	// trains the eye to stop reading it, which is exactly the failure mode
	// this project is closing.
	for _, d := range resp.RecentDeaths {
		fmt.Fprintf(w, "✝ %s:%s (:%d) died %s ago\n", d.WorkspaceID, deadServerLabel(d), d.Port, formatAgo(d.AgoSeconds))
	}

	printPortConflicts(w, resp.PortConflicts)
	printMainWorktrees(w, resp.MainWorktrees)
	printOffsiteAlarm(w, offsite)
}

// printPortConflicts lists each port conflict with the command that repairs
// it — the one place of the CLI that prints the fix (`pigtail list` only
// marks the row). Silent when there is none, like the deaths above. The fix
// sits on its own indented line under its conflict: aligned in a column, it
// was pushed as far right as the longest holder path.
func printPortConflicts(w io.Writer, conflicts []pigtail.PortConflictView) {
	if len(conflicts) == 0 {
		return
	}
	fmt.Fprintln(w, "port conflicts:")
	for _, c := range conflicts {
		var what string
		switch c.Reason {
		case pigtail.PortConflictEnvDiverges:
			what = fmt.Sprintf(".env.local :%d ≠ registry :%d", c.Port, c.RegisteredPort)
		default:
			holder := c.HolderPath
			if holder == "" {
				holder = "an unknown holder"
			}
			what = fmt.Sprintf(":%d held by %s", c.Port, holder)
		}
		fmt.Fprintf(w, "  %s  %s  %s\n    → %s\n", c.WorkspaceID, c.Server, what, c.Fix)
	}
}

// printOffsiteAlarm surfaces the offsite backup verdict in `pigtail servers`,
// but only at alarm severity. This command is about dev servers; the backup
// earns a line here only when it is screaming, never as routine status — that
// stays `pigtail db list`'s job. Silent at warn, at ok, and when no
// destination is configured at all: same "no permanent line that usually says
// nothing" rule the deaths section above follows.
//
// The threshold is a rank comparison (pigtail.OffsiteSeverityAtLeast), not a
// string equality against "alarm": Severity travels over the wire as a named
// string type, and a daemon newer than this CLI could send a value this
// binary has never heard of. Comparing by equality would read that as "not
// alarm" and stay silent — exactly the silence this feature exists to close.
// Ranked by OffsiteSeverityAtLeast, an unrecognized value is never quieter
// than alarm, so it always shows here.
//
// The wording is store.RenderOffsiteDetail's own, carried verbatim on the
// wire as OffsiteView.Detail by GET /backup — this function composes it, it
// does not reword it.
//
// docs/wire-protocol.md's GET /backup "fallback" subsection binds a client
// driving a single glyph to take the max severity rank across
// OffsiteView.Severity and OffsiteView.Fallback.Severity. This function is
// that single-glyph client, so it opens a second, symmetric gate at the same
// alarm floor for the fallback route — its own detail, on its own line,
// never folded into the primary's. Folding by taking the max of the two
// severities but printing only the primary's Detail would be wrong: a
// fallback in an unrecognized (and therefore alarm-ranked, see
// offsiteSeverityRank) severity next to a healthy primary would print
// "⚠ offsite backup: ok", which is nonsense — the gate and the sentence must
// both be per-route.
//
// Today Fallback.Severity is documented as never reaching
// OffsiteSeverityAlarm (see OffsiteFallbackView's doc comment), so this
// second gate is presently unreachable except through an unrecognized
// severity value — this is deliberately ahead of that ceiling: the contract
// in wire-protocol.md is honored now, and the day the ceiling is lifted this
// CLI already complies without a second change.
func printOffsiteAlarm(w io.Writer, offsite *pigtail.OffsiteView) {
	if offsite == nil {
		return
	}
	if pigtail.OffsiteSeverityAtLeast(offsite.Severity, pigtail.OffsiteSeverityAlarm) {
		fmt.Fprintf(w, "⚠ offsite backup: %s\n", offsite.Detail)
	}
	if fb := offsite.Fallback; fb != nil && pigtail.OffsiteSeverityAtLeast(fb.Severity, pigtail.OffsiteSeverityAlarm) {
		fmt.Fprintf(w, "⚠ offsite fallback: %s\n", fb.Detail)
	}
}

// printMainWorktrees answers "is the main checkout I am about to read fresh?"
// from what the daemon already measured — this path never runs git, so the
// command stays a fast read.
//
// The opposite rule to the deaths section above: this one prints its good news
// too. A death that did not happen is a non-event, but "main is fresh" is the
// answer to a question actually being asked, and the section is at most one
// line per project. It disappears entirely only when the daemon has no verdict
// at all — no project, a just-started daemon, or `-refresh-main=false` — which
// is honestly nothing known rather than nothing wrong.
func printMainWorktrees(w io.Writer, mains []pigtail.MainWorktreeView) {
	if len(mains) == 0 {
		return
	}
	fmt.Fprintln(w, "main worktrees:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, m := range mains {
		name := m.ProjectName
		if name == "" {
			name = m.ProjectID
		}
		fmt.Fprintf(tw, "  %s\t%s\n", name, mainWorktreeLine(m))
	}
	tw.Flush()
}

// shortOwner trims the host from a derived identity when it is this machine's
// — in a table where every line would repeat it, the host is noise. Any other
// identity is printed as-is, and an unclaimed server gets a dash rather than a
// blank column. Pure.
func shortOwner(owner, myHost string) string {
	if owner == "" {
		return "—"
	}
	if suffix := "@" + myHost; myHost != "" && strings.HasSuffix(owner, suffix) {
		return strings.TrimSuffix(owner, suffix)
	}
	return owner
}

// serversNow is the clock the PIN column counts down against; a var so tests
// pin it.
var serversNow = time.Now

// pinColumn renders a server's pin: "until unpinned" for a durable pin, the
// time left for a timed one (shortDuration, the same wording as `pigtail db
// list`), "—" otherwise. A timed pin already past reads as none — the daemon
// drops it at its next look. Pure.
func pinColumn(s pigtail.ServerView, now time.Time) string {
	if s.PinnedDurable {
		return "until unpinned"
	}
	if s.PinnedUnixSeconds > 0 {
		if left := time.Unix(s.PinnedUnixSeconds, 0).Sub(now); left > 0 {
			return shortDuration(left)
		}
	}
	return "—"
}

// deadServerLabel names every server a death covers — `dev+web` for one
// process listed under two names — falling back to Server for a daemon that
// predates the servers field. Pure.
func deadServerLabel(d pigtail.DeadServerView) string {
	if len(d.Servers) == 0 {
		return d.Server
	}
	return strings.Join(d.Servers, "+")
}

// formatAgo renders a death's age as a short human duration: whole seconds
// under a minute, whole (rounded) minutes above — "45s", "4m", never a
// compound "4m12s" that reads as false precision for a background poll.
func formatAgo(seconds int) string {
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
}

// shortDuration is a copy of dbtool's and of store.OffsiteShortDuration, kept
// apart on purpose (TestShortDurationParity pins them).
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
