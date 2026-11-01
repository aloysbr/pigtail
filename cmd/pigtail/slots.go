package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func slotsCmd() *cobra.Command {
	var (
		asJSON, history bool
		since, extraRAM string
	)
	cmd := &cobra.Command{
		Use:   "slots",
		Short: "Show how CPU slots and memory admission are shared on this machine",
		Long: `Show how the machine's CPU slots are shared between test runs, then the
memory admission controller: heavy commands running (with the memory their
process tree holds now, at its peak, and what was reserved for it), those
waiting and why, and the costs learned per class and repo.

SHARE is instantaneous, not a ceiling: it is what a group is entitled to
right now given who else is asking, not the budget it could reach. A lone
group holding 1 slot with nothing else waiting shows "share 1" — that is the
arbiter working as designed, not stuck. The same group shows the full budget
the moment another group starts waiting.

--history reads what the admission controller recorded instead: how long
commands waited and why, how much slower they ran while the machine swapped,
and what --extra-ram more memory would have given back over the period.`,
		Annotations: scopedRead(),
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if history {
				return runSlotsHistory(cx, c, since, extraRAM, asJSON, os.Stdout, os.Stderr)
			}
			resp, err := c.CPUSlots(cx)
			if err != nil {
				return err
			}
			// A daemon older than the admission endpoint answers 404: report
			// that rather than failing the whole command.
			adm, admErr := c.Admission(cx)
			// The session budget (GET /session-budget); a daemon without it, or
			// with the gate off, shows no sessions section.
			sb, sbErr := c.SessionBudget(cx)
			if asJSON {
				// The CPU fields stay at the top level, as before; admission
				// and the session budget are added alongside, so existing
				// consumers keep working.
				out := struct {
					pigtail.CPUSlotsResponse
					Admission     *pigtail.AdmissionStatus    `json:"admission,omitempty"`
					SessionBudget *pigtail.SessionBudgetState `json:"session_budget,omitempty"`
				}{CPUSlotsResponse: resp}
				if admErr == nil {
					out.Admission = &adm
				}
				if sbErr == nil && sb.Enabled {
					out.SessionBudget = &sb
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			printCPUSlots(os.Stdout, resp)
			fmt.Fprintln(os.Stdout)
			if admErr != nil {
				fmt.Fprintf(os.Stdout, "memory admission = unavailable (%v)\n", admErr)
			} else {
				printAdmission(os.Stdout, adm)
			}
			if sbErr == nil && sb.Enabled {
				fmt.Fprintln(os.Stdout)
				printSessionBudget(os.Stdout, sb)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table (with --history: the raw records)")
	cmd.Flags().BoolVar(&history, "history", false, "summarise the recorded admissions instead of the live state")
	cmd.Flags().StringVar(&since, "since", "7d", "with --history: how far back to look (7d, 36h)")
	cmd.Flags().StringVar(&extraRAM, "extra-ram", "64G", "with --history: the memory to simulate adding")
	return cmd
}

// printSessionBudget is the slots sessions section (D9): n/m and the gate,
// then, only when some wakes were deferred in the relay's last pass, their
// count and the oldest one's age. Nothing when the gate is off.
func printSessionBudget(w io.Writer, st pigtail.SessionBudgetState) {
	if !st.Enabled {
		return
	}
	gate := sessbudget.GateLine(sessbudget.Decision{Admitted: st.Gate != "closed", Reason: st.Reason})
	fmt.Fprintf(w, "Sessions : %d/%d au travail · %s\n", st.Busy, st.Budget, gate)
	if st.DeferredWakes.Count > 0 {
		fmt.Fprintf(w, "réveils différés : %d (plus ancien %s)\n", st.DeferredWakes.Count, frAge(st.DeferredWakes.OldestAgeSeconds))
	}
}

// frAge renders an age in seconds as « 40 s », « 3 min » or « 2 h ».
func frAge(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d min", seconds/60)
	}
	return fmt.Sprintf("%d h", seconds/3600)
}

func runSlotsHistory(ctx context.Context, c *pigtail.Client, since, extraRAM string, asJSON bool, stdout, stderr io.Writer) error {
	span, err := parseSpan(since)
	if err != nil {
		return err
	}
	extra, err := parseSize(extraRAM)
	if err != nil {
		return err
	}
	from := time.Now().Add(-span)
	h, err := c.AdmissionHistory(ctx, from)
	if err != nil {
		return err
	}
	// The CPU history is best effort: a daemon older than the route answers
	// 404, which must not fail the memory part.
	cpu, cpuErr := c.CPUSlotsHistory(ctx, from)
	if asJSON {
		out := struct {
			pigtail.AdmissionHistory
			CPUSlots *[]pigtail.CPUSlotRecord `json:"cpu_slots,omitempty"`
		}{AdmissionHistory: h}
		if cpuErr == nil {
			out.CPUSlots = &cpu.Records
		} else {
			fmt.Fprintf(stderr, "cpu slots history unavailable: %s\n", strings.TrimSpace(cpuErr.Error()))
		}
		return json.NewEncoder(stdout).Encode(out)
	}
	printHistory(stdout, summarizeHistory(h.Records, h.FloorBytes, extra), since)
	fmt.Fprintln(stdout)
	if cpuErr != nil {
		fmt.Fprintf(stdout, "cpu slots history = unavailable (%s)\n", strings.TrimSpace(cpuErr.Error()))
		return nil
	}
	printCPUHistory(stdout, cpu.Records, since)
	return nil
}

func printCPUHistory(w io.Writer, recs []pigtail.CPUSlotRecord, span string) {
	if len(recs) == 0 {
		fmt.Fprintf(w, "cpu slots: no hold recorded over the last %s\n", span)
		return
	}
	counts := map[string]int{}
	var waited, held int64
	for _, r := range recs {
		counts[r.Outcome]++
		waited += r.WaitedMS
		held += r.HeldMS
	}
	fmt.Fprintf(w, "cpu slots, last %s: %d holds (released %d · revoked %d · cancelled %d)\n", span, len(recs),
		counts[pigtail.CPUSlotOutcomeReleased], counts[pigtail.CPUSlotOutcomeRevoked], counts[pigtail.CPUSlotOutcomeCancelled])
	fmt.Fprintf(w, "waited %s in all · held %s in all\n",
		(time.Duration(waited) * time.Millisecond).Round(time.Second),
		(time.Duration(held) * time.Millisecond).Round(time.Second))
}

func printCPUSlots(w io.Writer, resp pigtail.CPUSlotsResponse) {
	if resp.Budget <= 0 {
		fmt.Fprintln(w, "cpu slots = off (daemon started with -cpu-slots=0)")
		return
	}
	fmt.Fprintf(w, "budget=%d · held=%d · waiting=%d\n", resp.Budget, resp.Held, resp.Waiting)
	if len(resp.Groups) == 0 {
		fmt.Fprintln(w, "no active group")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GROUP\tHELD\tWAITING\tSHARE")
	for _, g := range resp.Groups {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", g.Group, g.Held, g.Waiting, g.Share)
	}
	tw.Flush()
}

func printAdmission(w io.Writer, st pigtail.AdmissionStatus) {
	if st.MaxJobs <= 0 {
		fmt.Fprintln(w, "memory admission = off (daemon started with -admit-max-jobs=0)")
		return
	}
	fmt.Fprintf(w, "memory admission: jobs=%d/%d · waiting=%d · available=%s (after reservations %s) · floor=%s · pressure=%s · swapout=%s/s · compressor=%s · unused=%s · compress=%s/s\n",
		len(st.Leases), st.MaxJobs, len(st.Queue), fmtBytes(st.AvailableBytes), fmtBytes(st.EffectiveBytes),
		fmtBytes(st.FloorBytes), pressureName(st.Pressure), fmtBytes(uint64(st.SwapoutBytesPerSec)),
		fmtBytes(st.CompressorBytes), fmtBytes(st.UnusedBytes), fmtBytes(uint64(st.CompressRateBytesPerSec)))
	fmt.Fprintln(w, diskLine(st))
	// Under pressure the floor in the line above is not the one being applied:
	// say so, or the numbers do not add up for whoever is reading them while
	// a run waits.
	if st.Pressure >= st.PressureGate && st.PressureGate > 0 && st.PressureFloorFactor > 1 {
		fmt.Fprintf(w, "under pressure: floor raised to %s and any swapping holds the queue — %s\n",
			fmtBytes(uint64(float64(st.FloorBytes)*st.PressureFloorFactor)),
			starveNote(st.MaxStarveSeconds))
	}
	if limits := formatClassLimits(st); limits != "" {
		// Named out loud because it is the gate that does NOT come from the
		// memory reading above: someone staring at 40 free gigabytes while a
		// run waits needs to see why.
		fmt.Fprintf(w, "class caps: %s (runs of one class held to that many at a time)\n", limits)
	}
	if line := calmGateLine(st); line != "" {
		fmt.Fprintln(w, line)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(st.Leases) > 0 {
		fmt.Fprintln(tw, "RUNNING\tCLASS\tMEM\tPEAK\tRESERVED\tAGE\tWAITED\tCOMMAND")
		for _, l := range st.Leases {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", l.Group, l.Class, fmtBytes(l.RSSBytes),
				fmtBytes(l.PeakBytes), fmtBytes(l.CostBytes), time.Duration(l.AgeSeconds)*time.Second,
				(time.Duration(l.WaitedMS) * time.Millisecond).Round(time.Second), l.Label)
		}
	}
	if len(st.Queue) > 0 {
		fmt.Fprintln(tw, "WAITING\tCLASS\tNEEDS\tAGE\tREASON\tCOMMAND")
		for _, q := range st.Queue {
			reason := q.Reason
			if reason == "" {
				reason = "behind the head"
			}
			group := q.Group
			if q.Focus {
				group = "* " + group
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", group, q.Class, fmtBytes(q.CostBytes),
				time.Duration(q.AgeSeconds)*time.Second, reason, q.Label)
		}
	}
	tw.Flush()
	if len(st.Costs) > 0 {
		fmt.Fprintln(w, "learned costs (EWMA of peak memory):")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, c := range st.Costs {
			class := c.Class
			if c.Workers > 0 {
				class = fmt.Sprintf("%s ×%d", class, c.Workers)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t(%d runs)\n", class, c.Repo, fmtBytes(c.EWMABytes), c.Samples)
		}
		tw.Flush()
	}
}

// diskLine is the slots disk line: the free space and the floor under which
// nothing is admitted (criterion 6).
func diskLine(st pigtail.AdmissionStatus) string {
	floor := "no floor"
	if st.DiskFloorBytes > 0 {
		floor = "floor " + fmtGB(st.DiskFloorBytes)
	}
	if !st.DiskKnown {
		return fmt.Sprintf("disk: unknown (%s)", floor)
	}
	return fmt.Sprintf("disk: %s free (%s)", fmtGB(st.DiskFreeBytes), floor)
}

// starveNote says what still gets a waiter through when the gates do not.
func starveNote(seconds int) string {
	if seconds <= 0 {
		return "nothing lifts it but the machine recovering"
	}
	return fmt.Sprintf("a wait past %s is admitted anyway", (time.Duration(seconds) * time.Second).String())
}

// calmGateLine names the rule that holds the heavy classes on the compressor
// even past the starvation wait (D1, 2026-10-28): the one gate a run can sit
// behind for longer than max-starve, so it is said out loud.
func calmGateLine(st pigtail.AdmissionStatus) string {
	if st.CompressCalmWindowSeconds <= 0 || len(st.CompressCalmClasses) == 0 || st.CompressGateBytesPerSec <= 0 {
		return ""
	}
	window := time.Duration(st.CompressCalmWindowSeconds) * time.Second
	lift := "no wait lifts it"
	if st.CompressCalmMaxStarveSeconds > 0 {
		lift = fmt.Sprintf("a wait past %s lifts it", time.Duration(st.CompressCalmMaxStarveSeconds)*time.Second)
	}
	return fmt.Sprintf("calm gate: %s start only under %s/s of compressor traffic over 10s and over %s (now %s/s over %s) — %s",
		strings.Join(st.CompressCalmClasses, ", "), fmtBytes(uint64(st.CompressGateBytesPerSec)), window,
		fmtBytes(uint64(st.CompressCalmRateBytesPerSec)), window, lift)
}

// formatClassLimits renders the active per-class caps, sorted so the line is
// stable between runs. Caps set to zero or less are off and are left out.
func formatClassLimits(st pigtail.AdmissionStatus) string {
	classes := make([]string, 0, len(st.ClassLimits))
	for class, n := range st.ClassLimits {
		if n > 0 {
			classes = append(classes, class)
		}
	}
	if len(classes) == 0 {
		return ""
	}
	sort.Strings(classes)
	parts := make([]string, 0, len(classes))
	for _, class := range classes {
		parts = append(parts, fmt.Sprintf("%s=%d", class, st.ClassLimits[class]))
	}
	return strings.Join(parts, " · ")
}

func pressureName(level int) string {
	switch level {
	case 1:
		return "normal"
	case 2:
		return "warn"
	case 4:
		return "critical"
	}
	return "unknown"
}
