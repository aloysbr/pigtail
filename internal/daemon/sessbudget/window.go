package sessbudget

import (
	"time"
)

// Reading is one memory reading of the pressure window: what a sample says
// about the machine, at the sample's time.
type Reading struct {
	At time.Time
	// Available is the available memory, bytes.
	Available uint64
	// CompressorRate is the compressor's traffic, bytes/s (60 s average).
	CompressorRate float64
	// Swapouts is the swap-out rate, bytes/s; any value above 0 is a swap
	// write.
	Swapouts float64
}

// ReadingOf is the memory reading a sample carries.
func ReadingOf(s Sample) Reading {
	return Reading{
		At: s.TS, Available: s.AvailableBytes,
		CompressorRate: s.CompressBytesPerSec, Swapouts: s.SwapoutBytesPerSec,
	}
}

// Thresholds are the pressure limits the window is judged against.
type Thresholds struct {
	// MinAvailable is the available memory every reading must stay above.
	MinAvailable uint64
	// CompressBytesPerSec is the mean compressor rate the window must stay
	// at or under.
	CompressBytesPerSec float64
}

// Pressure is the window's verdict: green, or red with its cause (a D8 label).
type Pressure struct {
	Green bool
	// Cause is the red cause, "" when green.
	Cause string
}

// Window keeps the readings of the last Length. Pure: it has no clock, the
// readings carry their time. Not safe for concurrent use; the controller
// guards it.
type Window struct {
	Length   time.Duration
	readings []Reading
}

// NewWindow returns an empty window of the given length.
func NewWindow(length time.Duration) *Window { return &Window{Length: length} }

// Add takes one reading and drops those older than Length before it.
func (w *Window) Add(r Reading) {
	w.readings = append(w.readings, r)
	cut := 0
	for cut < len(w.readings) && r.At.Sub(w.readings[cut].At) > w.Length {
		cut++
	}
	w.readings = w.readings[cut:]
}

// Len is the number of readings held.
func (w *Window) Len() int { return len(w.readings) }

// Judge answers green when every reading's available memory stays above the
// minimum, the mean compressor rate is under the threshold and no reading
// wrote to swap. A partial window (daemon start) is judged on the readings it
// has; an empty one is red, cause « mesure en attente ». When several causes
// are red the first of swap > memory > compressor is named.
func (w *Window) Judge(th Thresholds) Pressure {
	if len(w.readings) == 0 {
		return Pressure{Cause: ReasonPending}
	}
	var swap bool
	minAvail := w.readings[0].Available
	var sum float64
	for _, r := range w.readings {
		if r.Swapouts > 0 {
			swap = true
		}
		if r.Available < minAvail {
			minAvail = r.Available
		}
		sum += r.CompressorRate
	}
	mean := sum / float64(len(w.readings))
	switch {
	case swap:
		return Pressure{Cause: ReasonSwap}
	case minAvail < th.MinAvailable:
		return Pressure{Cause: ReasonLowMemory(minAvail, th.MinAvailable)}
	case mean > th.CompressBytesPerSec:
		return Pressure{Cause: ReasonCompressor(mean)}
	}
	return Pressure{Green: true}
}

// Budget is the budget m of the moment: the floor when the pressure is red,
// the ceiling when it is green.
func Budget(p Pressure, floor, ceiling int) int {
	if !p.Green {
		return floor
	}
	return ceiling
}
