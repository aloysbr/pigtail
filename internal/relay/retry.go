package relay

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ContinueLine is what the relay types into a session stopped on an API error,
// or at the reset of a usage limit that nothing resumed (D3).
const ContinueLine = "continue"

// RetryDelays are the waits before the 1st..5th try of an API error that is
// not a network failure, counted from the stop that followed the previous try.
var RetryDelays = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute}

const (
	// MaxOtherTries is the number of tries after which a non-network error gives up.
	MaxOtherTries = 5
	// NetworkGiveUp bounds a series stopped on a network failure, from its first stop.
	NetworkGiveUp = 12 * time.Hour
	// LimitResumeGrace is how long after its reset a usage limit still standing
	// is considered not resumed by Claude Code itself.
	LimitResumeGrace = 2 * time.Minute
	// LimitResumeHoldMax bounds how long past the grace the idle pause waits
	// for the resume's decision, marker or not.
	LimitResumeHoldMax = 10 * time.Minute
)

// Reasons shown on the API error pill (D7).
const (
	ReasonNetwork   = "relance au retour réseau"
	ReasonExhausted = "relances épuisées"
	ReasonHeld      = "relance en attente"
)

// ReasonAt is the pill reason of a try due at at: "relance à 15:04 (n/5)".
// loc nil is time.Local, the sidebar's own rule (cmuxsync compute.go).
func ReasonAt(at time.Time, loc *time.Location, n int) string {
	if loc == nil {
		loc = time.Local
	}
	return fmt.Sprintf("relance à %s (%d/%d)", at.In(loc).Format("15:04"), n, MaxOtherTries)
}

// Series is the state of a retry series read from the journal; the zero value
// is "no try yet".
type Series struct {
	Anchor     time.Time // the first stop of the series
	Tries      int       // every "continue" typed
	OtherTries int       // those typed for a non-network error
	LastTry    time.Time // when the last one was typed
}

// RetryStep is the plan for the stop being handled.
type RetryStep struct {
	Exhausted   bool      // no more tries: write the marker, type nothing
	Due         time.Time // when the next try may be typed
	NeedNetwork bool      // the try also waits for the API to be reachable
	Reason      string    // the pill reason
	Try         int       // rank of the try, all classes
	OtherTry    int       // rank among the non-network tries
}

// PlanRetry plans the next try of a session stopped at stop on an error of the
// given class (claudesession.ClassNetwork / ClassOther), in series s.
func PlanRetry(s Series, class string, stop, now time.Time, loc *time.Location) RetryStep {
	if class == claudesession.ClassNetwork {
		anchor := s.Anchor
		if anchor.IsZero() {
			anchor = stop
		}
		if !now.Before(anchor.Add(NetworkGiveUp)) {
			return RetryStep{Exhausted: true, Reason: ReasonExhausted}
		}
		return RetryStep{NeedNetwork: true, Due: stop, Reason: ReasonNetwork, Try: s.Tries + 1, OtherTry: s.OtherTries}
	}
	if s.OtherTries >= MaxOtherTries {
		return RetryStep{Exhausted: true, Reason: ReasonExhausted}
	}
	due := stop.Add(RetryDelays[s.OtherTries])
	return RetryStep{Due: due, Reason: ReasonAt(due, loc, s.OtherTries+1), Try: s.Tries + 1, OtherTry: s.OtherTries + 1}
}

// RetryAllowed is Eligible for the "continue": the session is idle (the native
// status shell, a background task, is refused), not parked, never in Standby
// (readable or not), and its Stop watcher is no refusal (it does not resume a
// failed turn). The automatic api_error and limit waits are what the retry
// handles, so they do not refuse; any other blocking wait does.
func RetryAllowed(c Candidate) (bool, string) {
	switch {
	case c.Standby && c.StandbyReason != "":
		return false, c.StandbyReason
	case c.Standby:
		return false, "standby (D16)"
	case c.Native.ParkedJobID != "":
		return false, "parked as a background job"
	case c.Native.Status != "idle":
		return false, "status " + c.Native.Status
	}
	waits := make([]pigtail.SessionWait, 0, len(c.Waits))
	for _, w := range c.Waits {
		if w.Source == pigtail.WaitSourceAuto && (w.Type == "api_error" || w.Type == "limit") {
			continue
		}
		waits = append(waits, w)
	}
	c.Waits = waits
	c.Watched = false
	return Eligible(c)
}

// resetRefusedMarkers are the lines that still refuse once a limit has reset:
// Claude Code's own auto-resume markers. The stop lines are not in this list,
// they are tolerated after the reset.
var resetRefusedMarkers = []string{"continuing automatically", "waiting"}

func resetRefusedLine(line string) bool {
	line = strings.ToLower(line)
	for _, m := range resetRefusedMarkers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// ScreenAllowsAfterLimitReset is ScreenAllows for a "continue" typed after a
// usage limit's reset: the stop lines ("You've hit your", "/usage-credits",
// "Usage limit reached") are tolerated, since nothing else would ever clear
// them, but Claude Code's own auto-resume markers ("continuing automatically",
// "Waiting") still refuse. Focus, input box and draft checks are unchanged.
func ScreenAllowsAfterLimitReset(screen string, focused bool) (bool, string) {
	return screenAllows(screen, focused, resetRefusedLine)
}

// limitResumeHeld: the session has an automatic limit wait whose end is
// before now, and the hold (reset + grace + LimitResumeHoldMax) is not over.
func limitResumeHeld(c Candidate) bool {
	for _, w := range c.Waits {
		if w.Source == pigtail.WaitSourceAuto && w.Type == "limit" && w.Until != nil {
			return c.Now.Before(w.Until.Add(LimitResumeGrace + LimitResumeHoldMax))
		}
	}
	return false
}
