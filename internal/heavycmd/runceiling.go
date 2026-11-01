package heavycmd

import "time"

// runCeilings is how long a command of each class may run, in agent mode,
// before `pigtail admit` / `pigtail test` stop its tree.
//
// Sources, measured on 2026-10-29: the p99 durations of each class sit well
// under these; the maxima recorded in the daemon are truncated at 30 min by
// MaxHold (the admission lease is revoked then, nothing is killed), so they
// say nothing above it — hence 60 min as the generic bound, and a suite that
// really needs more passes `pigtail admit --max-run`.
var runCeilings = map[Class]time.Duration{
	ClassESLint:         30 * time.Minute,
	ClassJest:           30 * time.Minute,
	ClassBuild:          30 * time.Minute,
	ClassStorybookBuild: 30 * time.Minute,
	ClassTSC:            45 * time.Minute,
	ClassGoTest:         45 * time.Minute,
	ClassVitest:         60 * time.Minute,
	ClassVitestBrowser:  60 * time.Minute,
	ClassNx:             60 * time.Minute,
	ClassPrecommit:      60 * time.Minute,
	ClassGeneric:        60 * time.Minute,
}

// RunCeiling returns the run ceiling of class c; an unknown class is generic.
func RunCeiling(c Class) time.Duration {
	if d, ok := runCeilings[c]; ok {
		return d
	}
	return runCeilings[ClassGeneric]
}
