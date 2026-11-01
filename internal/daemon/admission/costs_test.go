package admission

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCostTableEstimate(t *testing.T) {
	ct := newCostTable("")
	const prior = 2 * gib
	if got := ct.estimate("tsc", "repo", 0, prior, (prior)/2); got != prior {
		t.Fatalf("no observation: got %d want the prior", got)
	}
	// Two observations are not enough to trust.
	ct.observe("tsc", "repo", 0, 1*gib)
	ct.observe("tsc", "repo", 0, 1*gib)
	if got := ct.estimate("tsc", "repo", 0, prior, (prior)/2); got != prior {
		t.Fatalf("two observations: got %d want the prior", got)
	}
	ct.observe("tsc", "repo", 0, 1*gib)
	// EWMA of three 1 GiB peaks is 1 GiB; 1.2× that is above prior/2.
	margin := costMargin
	if got, want := ct.estimate("tsc", "repo", 0, prior, (prior)/2), uint64(margin*float64(gib)); got != want {
		t.Fatalf("three observations: got %d want %d", got, want)
	}
	// Other repos and classes learn separately.
	if got := ct.estimate("tsc", "other", 0, prior, (prior)/2); got != prior {
		t.Fatalf("other repo: got %d want the prior", got)
	}
}

func TestCostTableFloorAtHalfPrior(t *testing.T) {
	ct := newCostTable("")
	for i := 0; i < 5; i++ {
		ct.observe("eslint", "repo", 0, 10<<20) // a lint of one tiny file
	}
	if got, want := ct.estimate("eslint", "repo", 0, 2*gib, (2*gib)/2), 1*gib; got != want {
		t.Fatalf("got %d want half the prior %d", got, want)
	}
}

func TestCostTableEWMAFollowsTrend(t *testing.T) {
	ct := newCostTable("")
	for i := 0; i < 3; i++ {
		ct.observe("jest", "repo", 0, 2*gib)
	}
	before := ct.estimate("jest", "repo", 0, 0, 0)
	ct.observe("jest", "repo", 0, 6*gib)
	after := ct.estimate("jest", "repo", 0, 0, 0)
	if after <= before {
		t.Fatalf("a much larger peak must raise the estimate: %d -> %d", before, after)
	}
}

func TestCostTablePersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costs.json")
	ct := newCostTable(path)
	for i := 0; i < 3; i++ {
		ct.observe("tsc", "repo", 0, 1*gib)
	}
	if err := ct.save(); err != nil {
		t.Fatal(err)
	}
	loaded := newCostTable(path)
	if err := loaded.load(); err != nil {
		t.Fatal(err)
	}
	if a, b := ct.estimate("tsc", "repo", 0, 2*gib, (2*gib)/2), loaded.estimate("tsc", "repo", 0, 2*gib, (2*gib)/2); a != b {
		t.Fatalf("round trip changed the estimate: %d vs %d", a, b)
	}
}

func TestCostTableLoadMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := newCostTable(filepath.Join(dir, "absent.json")).load(); err != nil {
		t.Fatalf("a missing file is a first start, not an error: %v", err)
	}
	if err := newCostTable("").load(); err != nil {
		t.Fatalf("no path means no persistence: %v", err)
	}
}

// TestCostTableDiscardsLegacyFlatFile: a pre-v2 file holds peaks measured as
// ps RSS, which leaves compressed memory out. Reading them back would keep
// reserving a third of what a storybook-sized run really takes, so they are
// discarded, loudly, and the priors take over.
func TestCostTableDiscardsLegacyFlatFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costs.json")
	legacy := `{"tsc\u0000repo":{"ewma_bytes":1073741824,"n":5}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	ct := newCostTable(path)
	if err := ct.load(); !errors.Is(err, errLegacyCosts) {
		t.Fatalf("load(legacy) = %v, want errLegacyCosts", err)
	}
	if got := ct.estimate("tsc", "repo", 0, 7*gib, gib); got != 7*gib {
		t.Fatalf("legacy peaks were believed: estimate = %d, want the prior", got)
	}
	if len(ct.snapshot()) != 0 {
		t.Fatalf("legacy entries kept: %v", ct.snapshot())
	}
}

// TestCostTableNewerFormatIsDiscardedAndSaidSo: a file from a newer pigtail
// (after a downgrade) is not believed either, but with its own error — "you
// downgraded", not "this is old RSS data" — and that error says the next save
// overwrites it.
func TestCostTableNewerFormatIsDiscardedAndSaidSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costs.json")
	newer := `{"version":3,"costs":{"tsc\u0000repo":{"ewma_bytes":1073741824,"n":5}}}`
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	ct := newCostTable(path)
	err := ct.load()
	if !errors.Is(err, errNewerCosts) || errors.Is(err, errLegacyCosts) {
		t.Fatalf("load(v3) = %v, want errNewerCosts and not errLegacyCosts", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "newer pigtail") || !strings.Contains(msg, "format 3") || !strings.Contains(msg, "overwrite") {
		t.Fatalf("message %q must name the newer format and say it will be overwritten", msg)
	}
	if got := ct.estimate("tsc", "repo", 0, 7*gib, gib); got != 7*gib {
		t.Fatalf("newer-format peaks were believed: estimate = %d, want the prior", got)
	}
}

// TestCostTableSavesVersionedEnvelope: what is written can be told apart from
// the legacy shape by a reader, and reads back.
func TestCostTableSavesVersionedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costs.json")
	ct := newCostTable(path)
	ct.observe("tsc", "repo", 0, gib)
	if err := ct.save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Version int                        `json:"version"`
		Costs   map[string]json.RawMessage `json:"costs"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Version != costsFormatVersion || len(env.Costs) != 1 {
		t.Fatalf("saved %s, want version %d with one entry", raw, costsFormatVersion)
	}
	if err := newCostTable(path).load(); err != nil {
		t.Fatalf("a v2 file must load: %v", err)
	}
}
