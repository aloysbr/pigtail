// Package powersrc detects whether the machine runs on battery or mains
// power, mirroring rammon's platform-split sampler pattern. Sampling errors
// are reported as mains so a detector failure never triggers an eviction.
package powersrc

// Source reports the current power source. Implemented per platform, faked
// in tests.
type Source interface {
	// OnBattery reports true when running off battery, false on mains/AC.
	OnBattery() (bool, error)
}

// SampleOnBattery resolves a Source to a plain bool, mapping any sampling
// error to false (mains) — the fail-safe: never evict on uncertainty.
func SampleOnBattery(s Source) bool {
	onBattery, err := s.OnBattery()
	if err != nil {
		return false
	}
	return onBattery
}
