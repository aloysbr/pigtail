// Package sessbudgettest holds the named load fixtures of the session budget:
// the one place a test's load state (n, pressure, m) is defined. Tests in
// sessbudget, handlers, daemon and cmd/pigtail start from AboveFloor,
// BelowFloor or RedFull, never from literal n/m values.
package sessbudgettest

import (
	"fmt"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The configuration every fixture is expressed against.
const (
	Floor   = 6
	Ceiling = 20
	// MinGap is the progressive-start gap between two admissions.
	MinGap = 40 * time.Second
	// Period is the sampler's cadence.
	Period = 30 * time.Second
	// WindowLength is the pressure window's span.
	WindowLength = 5 * time.Minute
	// MinAvailable is the available memory under which pressure is red.
	MinAvailable = uint64(6) << 30
	// CompressBytesPerSec is the compressor threshold.
	CompressBytesPerSec = float64(16 << 20)
	// RedCause is RedFull's red cause.
	RedCause = "compresseur 18 Mo/s"
)

// Start is the fixtures' clock origin.
var Start = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// Config is the session budget configuration the fixtures assume (gate on).
func Config() sessbudget.Config {
	return sessbudget.Config{
		Floor: Floor, Ceiling: Ceiling, Window: WindowLength, MinGap: MinGap,
		CompressBytesPerSec: CompressBytesPerSec, MinAvailable: MinAvailable,
		SamplePeriod: Period,
	}
}

// Clock is a fake clock, safe for concurrent use.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock returns a clock at Start.
func NewClock() *Clock { return &Clock{t: Start} }

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Fixture is a named load state: its pressure readings and its busy count n.
type Fixture struct {
	Name string
	// Busy is n, the busy sessions its samples show.
	Busy int
	// Memory is the reading every sample of the fixture carries (At unset).
	Memory sessbudget.Reading
}

var greenMemory = sessbudget.Reading{Available: 10 << 30, CompressorRate: 1 << 20}

// The named fixtures.
var (
	// AboveFloor: green pressure, m = Ceiling, Floor ≤ n < m.
	AboveFloor = Fixture{Name: "AboveFloor", Busy: 8, Memory: greenMemory}
	// BelowFloor: green pressure, n < Floor.
	BelowFloor = Fixture{Name: "BelowFloor", Busy: 3, Memory: greenMemory}
	// RedFull: red pressure (compressor, RedCause), m = Floor, n = Floor.
	RedFull = Fixture{Name: "RedFull", Busy: Floor,
		Memory: sessbudget.Reading{Available: 10 << 30, CompressorRate: 18 << 20}}
)

// WithBusy is the fixture with n changed, nothing else.
func (f Fixture) WithBusy(n int) Fixture {
	f.Busy = n
	f.Name = fmt.Sprintf("%s.WithBusy(%d)", f.Name, n)
	return f
}

// Pressure is the fixture's pressure, judged by the real Window.
func (f Fixture) Pressure() sessbudget.Pressure {
	w := sessbudget.NewWindow(WindowLength)
	r := f.Memory
	r.At = Start
	w.Add(r)
	return w.Judge(Config().Thresholds())
}

// Budget is m for the fixture's pressure.
func (f Fixture) Budget() int { return sessbudget.Budget(f.Pressure(), Floor, Ceiling) }

// InputAt is the agent DecideInput of the fixture's load at now, the last
// admission at lastAdmission (zero = none) and the last sample at lastSample.
func (f Fixture) InputAt(lastAdmission, lastSample, now time.Time) sessbudget.DecideInput {
	p := f.Pressure()
	in := sessbudget.DecideInput{
		Kind: sessbudget.KindAgent, Busy: f.Busy, Budget: sessbudget.Budget(p, Floor, Ceiling),
		Floor: Floor, RedCause: p.Cause, Observed: !lastSample.IsZero(), MinGap: MinGap,
	}
	if !lastAdmission.IsZero() {
		in.HasAdmission = true
		in.SinceAdmission = now.Sub(lastAdmission)
		in.SampleSinceAdmission = lastSample.After(lastAdmission)
		if !in.SampleSinceAdmission {
			in.MissedPeriods = int(now.Sub(lastAdmission) / Period)
		}
	}
	return in
}

// Input is the fixture inside the gap: an admission 10 s ago, a sample taken
// after it.
func (f Fixture) Input() sessbudget.DecideInput {
	return f.InputAt(Start, Start.Add(5*time.Second), Start.Add(10*time.Second))
}

// InputPastGap is the fixture once the gap has elapsed, with a sample taken
// after the last admission.
func (f Fixture) InputPastGap() sessbudget.DecideInput {
	return f.InputAt(Start, Start.Add(5*time.Second), Start.Add(MinGap+time.Second))
}

// Sample is one sampler record of the fixture's load at at: n busy sessions
// (busy-1…busy-n) and two idle ones.
func (f Fixture) Sample(at time.Time) sessbudget.Sample {
	s := sessbudget.Sample{
		TS: at, Busy: f.Busy, Live: f.Busy + 2,
		AvailableBytes: f.Memory.Available, CompressBytesPerSec: f.Memory.CompressorRate,
		SwapoutBytesPerSec: f.Memory.Swapouts,
	}
	for i := 1; i <= f.Busy; i++ {
		s.Sessions = append(s.Sessions, pigtail.SessionSampleRow{SessionID: fmt.Sprintf("busy-%d", i), PID: 1000 + i, Status: "busy"})
	}
	for i := 1; i <= 2; i++ {
		s.Sessions = append(s.Sessions, pigtail.SessionSampleRow{SessionID: fmt.Sprintf("idle-%d", i), PID: 2000 + i, Status: "idle"})
	}
	return s
}

// Samples are the records to pass to Controller.Observe to put it in the
// fixture's state at the clock's time.
func (f Fixture) Samples(c *Clock) []sessbudget.Sample {
	return []sessbudget.Sample{f.Sample(c.Now())}
}

// AdvanceSample moves the clock one sample period forward and returns the
// fixture's sample taken then.
func (f Fixture) AdvanceSample(c *Clock) sessbudget.Sample {
	c.Advance(Period)
	return f.Sample(c.Now())
}
