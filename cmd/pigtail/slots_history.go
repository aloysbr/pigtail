package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// swappedBytes is how much the machine must have swapped out during a run
// for that run to count as "under swap". Not zero: the counter is
// machine-wide, and a twenty-minute run can catch a few stray pages that
// slowed nothing — 16 MiB is a thousand 16 KiB pages, a real episode.
const swappedBytes = 16 << 20

// minBaselineRuns is how many swap-free runs a (class, repo) needs before its
// median is trusted as the "normal" duration the swapped runs are held to.
const minBaselineRuns = 3

// ramReasons are the waits more memory would have spared; the others
// (max_jobs, class_limit) come from the configuration and would remain.
var ramReasons = map[string]bool{"memory": true, "pressure": true, "swapout": true, "compress": true, "compress_calm": true}

// runGroup compares one (class, repo)'s run durations with and without swap.
type runGroup struct {
	Class, Repo       string
	Clean, Swapped    int
	CleanMed, SwapMed time.Duration
	Lost              time.Duration
	EnoughBaseline    bool
}

// historySummary is what `pigtail slots --history` prints.
type historySummary struct {
	Commands     int
	Outcomes     map[string]int
	Wait         time.Duration
	WaitByReason map[string]time.Duration
	WaitRAM      time.Duration
	WaitConfig   time.Duration
	Groups       []runGroup
	SwapLost     time.Duration // Σ Lost over the groups with a baseline

	ExtraBytes    uint64
	RAMWaits      int           // commands whose wait was mostly for memory
	Avoidable     int           // … of which the extra memory would have spared
	AvoidableWait time.Duration // the RAM part of those waits
}

// Recoverable is the agent time the extra memory would have given back.
func (s historySummary) Recoverable() time.Duration { return s.AvoidableWait + s.SwapLost }

// summarizeHistory aggregates the history. Pure: floor is the admission
// floor the simulation weighs each command against, extra the memory added.
//
// The simulation is deliberately coarse: a command whose wait was mostly for
// memory is counted as spared when avail_at_enqueue + extra − cost clears the
// floor — the gate's own test, minus the reservations in flight that the
// record does not carry.
func summarizeHistory(recs []pigtail.AdmissionRecord, floor, extra uint64) historySummary {
	s := historySummary{
		Outcomes: map[string]int{}, WaitByReason: map[string]time.Duration{}, ExtraBytes: extra,
	}
	type key struct{ class, repo string }
	clean, swapped := map[key][]time.Duration{}, map[key][]time.Duration{}
	for _, r := range recs {
		s.Commands++
		s.Outcomes[r.Outcome]++
		s.Wait += ms(r.WaitedMS)
		var ram, dominant time.Duration
		var dominantRAM bool
		for reason, v := range r.WaitByReasonMS {
			d := ms(v)
			s.WaitByReason[reason] += d
			if ramReasons[reason] {
				s.WaitRAM += d
				ram += d
			} else {
				s.WaitConfig += d
			}
			if d > dominant || (d == dominant && ramReasons[reason]) {
				dominant, dominantRAM = d, ramReasons[reason]
			}
		}
		if dominantRAM && ram > 0 {
			s.RAMWaits++
			if r.AvailAtEnqueue+extra >= r.CostBytes+floor {
				s.Avoidable++
				s.AvoidableWait += ram
			}
		}
		// Only a released run's duration is its own: a revoked one stops at
		// MaxHold, a cancelled one never ran.
		if r.Outcome != pigtail.AdmissionOutcomeReleased {
			continue
		}
		k := key{r.Class, r.Repo}
		if r.SwapoutBytesDuring > swappedBytes {
			swapped[k] = append(swapped[k], ms(r.HeldMS))
		} else {
			clean[k] = append(clean[k], ms(r.HeldMS))
		}
	}
	keys := map[key]bool{}
	for k := range clean {
		keys[k] = true
	}
	for k := range swapped {
		keys[k] = true
	}
	for k := range keys {
		g := runGroup{Class: k.class, Repo: k.repo, Clean: len(clean[k]), Swapped: len(swapped[k]),
			CleanMed: median(clean[k]), SwapMed: median(swapped[k])}
		if g.Clean >= minBaselineRuns {
			g.EnoughBaseline = true
			// Signed sum, floored once: a swapped run that happened to be
			// quick offsets one that was slow, rather than being ignored.
			for _, d := range swapped[k] {
				g.Lost += d - g.CleanMed
			}
			if g.Lost < 0 {
				g.Lost = 0
			}
			s.SwapLost += g.Lost
		}
		s.Groups = append(s.Groups, g)
	}
	sort.Slice(s.Groups, func(i, j int) bool {
		if s.Groups[i].Lost != s.Groups[j].Lost {
			return s.Groups[i].Lost > s.Groups[j].Lost
		}
		if s.Groups[i].Repo != s.Groups[j].Repo {
			return s.Groups[i].Repo < s.Groups[j].Repo
		}
		return s.Groups[i].Class < s.Groups[j].Class
	})
	return s
}

func ms(v int64) time.Duration { return time.Duration(v) * time.Millisecond }

// median of ds (the mean of the two middle values for an even count); 0 for
// none.
func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func printHistory(w io.Writer, s historySummary, span string) {
	if s.Commands == 0 {
		fmt.Fprintf(w, "no admitted command recorded over the last %s\n", span)
		return
	}
	outcomes := make([]string, 0, len(s.Outcomes))
	for _, o := range []string{pigtail.AdmissionOutcomeReleased, pigtail.AdmissionOutcomeRevoked, pigtail.AdmissionOutcomeCancelled} {
		outcomes = append(outcomes, fmt.Sprintf("%s %d", o, s.Outcomes[o]))
	}
	fmt.Fprintf(w, "last %s: %d commands (%s)\n", span, s.Commands, strings.Join(outcomes, " · "))
	fmt.Fprintf(w, "waited %s in all: RAM %s (%s) · config %s (%s)\n", fmtHours(s.Wait),
		fmtHours(s.WaitRAM), reasonsLine(s.WaitByReason, true),
		fmtHours(s.WaitConfig), reasonsLine(s.WaitByReason, false))
	if other := s.WaitByReason["other"]; other > 0 {
		fmt.Fprintf(w, "  (+ %s booked to no reason)\n", fmtHours(other))
	}

	if len(s.Groups) > 0 {
		fmt.Fprintf(w, "\nrun time, without vs with swap (> %s swapped out while it ran):\n", fmtBytes(swappedBytes))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  CLASS\tREPO\tNO SWAP\tWITH SWAP\tLOST")
		for _, g := range s.Groups {
			lost := "insufficient"
			switch {
			case g.Swapped == 0:
				lost = "—"
			case g.EnoughBaseline:
				lost = fmtHours(g.Lost)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", g.Class, g.Repo,
				runsLine(g.CleanMed, g.Clean), runsLine(g.SwapMed, g.Swapped), lost)
		}
		tw.Flush()
		fmt.Fprintf(w, "  (lost = Σ(run − swap-free median) over the swapped runs; needs %d swap-free runs)\n", minBaselineRuns)
	}

	fmt.Fprintf(w, "\nwith %s more memory: %d of %d memory-bound waits spared, %s\n",
		fmtBytes(s.ExtraBytes), s.Avoidable, s.RAMWaits, fmtHours(s.AvoidableWait))
	fmt.Fprintf(w, "recoverable: %s of agent time (%s waiting + %s slowed by swap)\n",
		fmtHours(s.Recoverable()), fmtHours(s.AvoidableWait), fmtHours(s.SwapLost))
	fmt.Fprintln(w, "caveat: over-estimated where swap was only one cause of a slow run (CPU contention rises with it);"+
		" under-estimated since commands run outside admission, and the slowdown they felt, are not recorded")
}

func reasonsLine(by map[string]time.Duration, ram bool) string {
	var parts []string
	for _, r := range []string{"memory", "pressure", "swapout", "compress", "compress_calm", "max_jobs", "class_limit", "disk"} {
		if ramReasons[r] == ram {
			parts = append(parts, fmt.Sprintf("%s %s", r, fmtHours(by[r])))
		}
	}
	return strings.Join(parts, ", ")
}

func runsLine(med time.Duration, n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%s (%d)", med.Round(time.Second), n)
}

// fmtHours renders a total the way it is going to be weighed: hours past
// one, minutes below.
func fmtHours(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return d.Round(time.Second).String()
}

// parseSpan reads --since: a number of days ("7d") or a Go duration ("36h").
func parseSpan(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("--since %q: want a positive number of days, like 7d", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since %q: want 7d or a duration like 36h", s)
	}
	return d, nil
}

// parseSize reads --extra-ram: a number with a G or M suffix, binary units
// ("64G" is 64 GiB, as the machine counts it).
func parseSize(s string) (uint64, error) {
	u := strings.ToUpper(strings.TrimSpace(s))
	u = strings.TrimSuffix(strings.TrimSuffix(u, "B"), "I")
	shift := 0
	switch {
	case strings.HasSuffix(u, "G"):
		shift, u = 30, strings.TrimSuffix(u, "G")
	case strings.HasSuffix(u, "M"):
		shift, u = 20, strings.TrimSuffix(u, "M")
	default:
		return 0, fmt.Errorf("--extra-ram %q: want a size like 64G or 512M", s)
	}
	n, err := strconv.ParseUint(u, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("--extra-ram %q: want a size like 64G or 512M", s)
	}
	return n << shift, nil
}
