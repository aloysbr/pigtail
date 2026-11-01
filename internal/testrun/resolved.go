package testrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// resolvedFacts is what a wrapper records about the config it evaluated, for
// the *next* run to read.
//
// Why a write-back rather than a probe: pigtail must pick a run's admission
// class before node starts, and nothing in the argv distinguishes a
// browser-mode vitest run from a node one — `vitest --run` is both. The only
// place the answer exists is inside the evaluated config, which is exactly
// where the wrapper already stands. Probing instead would mean spawning a
// second node per run to evaluate the config twice, for a fact that changes
// about as often as the config file does.
//
// The cost of being wrong is bounded and self-correcting: the first run of a
// config pigtail has never evaluated is classed as a plain vitest run, which
// only means it reserves less memory and takes no class cap — the same
// position the tool was in before this existed. From the second run on it is
// right. That is the same bargain the learned costs make.
type resolvedFacts struct {
	// Config is the repo's own config this describes, and MTimeMS its
	// modification time when the facts were recorded. Both are checked on
	// read: a config edited since may have turned browser mode on or off.
	Config  string `json:"config"`
	MTimeMS int64  `json:"mtime_ms"`

	// Browser reports test.browser.enabled as vitest resolved it.
	Browser bool `json:"browser"`
	// MaxWorkers is the ceiling that actually applied — min(repo, pigtail),
	// so it is the number of live browsers a browser-mode run will hold.
	MaxWorkers int `json:"max_workers"`
	// Isolate mirrors test.isolate, recorded for diagnostics.
	Isolate bool `json:"isolate"`
}

// resolvedPath is where the facts about baseConfig live inside dir. The name
// is keyed by the config's path so that several projects of a workspace
// layout, whose wrappers all share one directory, do not overwrite each
// other's facts.
func resolvedPath(dir, baseConfig string) string {
	sum := sha256.Sum256([]byte(baseConfig))
	return filepath.Join(dir, "resolved."+hex.EncodeToString(sum[:8])+".json")
}

// ReadResolved returns what a previous run recorded about baseConfig, and
// whether it is usable. Anything unusable — no file, unreadable file, corrupt
// JSON, facts about another config, a config modified since — reports false
// and no error: these facts are an optimisation, never a precondition, and
// the caller must be able to proceed without them.
func ReadResolved(dir, baseConfig string) (resolvedFacts, bool) {
	data, err := os.ReadFile(resolvedPath(dir, baseConfig)) //nolint:gosec // path derived from a hash of the config path
	if err != nil {
		return resolvedFacts{}, false
	}
	var f resolvedFacts
	if err := json.Unmarshal(data, &f); err != nil {
		return resolvedFacts{}, false
	}
	if f.Config != baseConfig {
		return resolvedFacts{}, false
	}
	st, err := os.Stat(baseConfig)
	if err != nil || st.ModTime().UnixMilli() != f.MTimeMS {
		return resolvedFacts{}, false
	}
	return f, true
}
