package sessbudget

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Kind is who asks for a launch slot.
type Kind string

const (
	// KindHuman is the user's own launch, through the launcher's gate.
	KindHuman Kind = "human"
	// KindAgent is an agent's `pigtail session spawn`.
	KindAgent Kind = "agent"
	// KindWake is the relay waking an idle session to read its inbox.
	KindWake Kind = "wake"
)

// The reason labels (D8): the only wording Decide returns and the CLI lines
// print. The others are built by ReasonLowMemory, ReasonCompressor and
// ReasonGap.
const (
	ReasonFull    = "budget plein"
	ReasonSwap    = "écriture en swap"
	ReasonPending = "mesure en attente"
)

// ReasonLowMemory is `mémoire disponible <x> Go < <min> Go`.
func ReasonLowMemory(available, minimum uint64) string {
	return fmt.Sprintf("mémoire disponible %s Go < %s Go", frGiB(available), frGiB(minimum))
}

// ReasonCompressor is `compresseur <r> Mo/s`, r in MiB/s rounded.
func ReasonCompressor(bytesPerSec float64) string {
	return fmt.Sprintf("compresseur %d Mo/s", int(math.Round(bytesPerSec/(1<<20))))
}

// ReasonGap is `démarrage progressif, <s> s`, s the seconds left, rounded up.
func ReasonGap(left time.Duration) string {
	return fmt.Sprintf("démarrage progressif, %d s", int(math.Ceil(left.Seconds())))
}

// GateLine is the gate's state for a decision: `porte ouverte`, or
// `porte fermée (<raison>)`.
func GateLine(d Decision) string {
	if d.Admitted {
		return "porte ouverte"
	}
	return "porte fermée (" + d.Reason + ")"
}

// frGiB renders bytes as GiB with one decimal comma, a whole value bare.
func frGiB(b uint64) string {
	s := strings.Replace(fmt.Sprintf("%.1f", float64(b)/(1<<30)), ".", ",", 1)
	return strings.TrimSuffix(s, ",0")
}

// freshWaiverPeriods is how many missed sample periods waive row 5's « a
// sample taken after the last admission » condition (the gap still applies).
const freshWaiverPeriods = 3

// DecideInput is everything Decide reads. The controller builds it; tests
// outside TestDecideTable take it from the sessbudgettest fixtures.
type DecideInput struct {
	Kind Kind
	// Busy is n: the busy sessions of the latest sample plus the pending
	// admissions.
	Busy int
	// Budget is m, Budget(pressure) of the moment.
	Budget int
	Floor  int
	// RedCause is the pressure's red cause, "" when green.
	RedCause string
	// Observed: a sample was observed since the daemon started.
	Observed bool
	// HasAdmission: an agent or wake admission was recorded; SinceAdmission
	// is the time since it.
	HasAdmission   bool
	SinceAdmission time.Duration
	MinGap         time.Duration
	// SampleSinceAdmission: a sample was taken after the last admission.
	SampleSinceAdmission bool
	// MissedPeriods is the number of sample periods elapsed since the last
	// admission without a sample.
	MissedPeriods int
}

// Decision is Decide's answer.
type Decision struct {
	Admitted bool
	// Reason is the D8 label of a refusal, or of a flagged human launch.
	Reason string
	// OverBudget flags a human launch made while n ≥ m.
	OverBudget bool
	// Row is the row of « Table d'admission » that decided.
	Row int
	// FreshWaived: row 5's sample condition was waived (missed periods), for
	// the controller to log.
	FreshWaived bool
}

// Decide is « Table d'admission »: the single source of every admission rule.
// The rows are evaluated in order, the first match wins.
func Decide(in DecideInput) Decision {
	full := func() string {
		if in.RedCause != "" {
			return in.RedCause
		}
		return ReasonFull
	}
	// Row 1: the user's launch, always admitted; flagged only when n ≥ m.
	if in.Kind == KindHuman {
		if in.Busy >= in.Budget {
			return Decision{Admitted: true, OverBudget: true, Reason: full(), Row: 1}
		}
		return Decision{Admitted: true, Row: 1}
	}
	// Row 2: no sample yet, n would read 0.
	if !in.Observed {
		return Decision{Reason: ReasonPending, Row: 2}
	}
	// Row 3: under the floor, whatever the gap or the freshness.
	if in.Busy < in.Floor {
		return Decision{Admitted: true, Row: 3}
	}
	// Row 4: full (m is the floor under red pressure).
	if in.Busy >= in.Budget {
		return Decision{Reason: full(), Row: 4}
	}
	// Row 5: the progressive start — the gap, then a fresh sample.
	if in.HasAdmission {
		if in.SinceAdmission < in.MinGap {
			return Decision{Reason: ReasonGap(in.MinGap - in.SinceAdmission), Row: 5}
		}
		if !in.SampleSinceAdmission {
			if in.MissedPeriods < freshWaiverPeriods {
				return Decision{Reason: ReasonPending, Row: 5}
			}
			return Decision{Admitted: true, Row: 6, FreshWaived: true}
		}
	}
	// Row 6.
	return Decision{Admitted: true, Row: 6}
}

// Config is the session budget's configuration (pigtaild's -session-* flags).
// Ceiling 0 disables the gate: every ask is admitted; a zero SamplePeriod
// means the sampler is off.
type Config struct {
	// Floor is the budget under red pressure, and the count under which
	// every ask is admitted.
	Floor int
	// Ceiling is the budget under green pressure; 0 = gate disabled.
	Ceiling int
	// Window is the span the pressure is judged over.
	Window time.Duration
	// MinGap is the minimum time between two agent or wake admissions above
	// the floor.
	MinGap time.Duration
	// CompressBytesPerSec is the mean compressor rate over which the
	// pressure is red.
	CompressBytesPerSec float64
	// MinAvailable is the available memory under which the pressure is red.
	MinAvailable uint64
	// SamplePeriod is the sampler's cadence (0 = sampler off).
	SamplePeriod time.Duration
}

// Enabled reports whether the gate is on.
func (c Config) Enabled() bool { return c.Ceiling > 0 }

// Thresholds are the pressure limits of this configuration.
func (c Config) Thresholds() Thresholds {
	return Thresholds{MinAvailable: c.MinAvailable, CompressBytesPerSec: c.CompressBytesPerSec}
}
