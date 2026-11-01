package sysmem

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

const mib = 1 << 20

func TestJudgeSelfFootprint(t *testing.T) {
	cases := []struct {
		name      string
		r         SelfFootprint
		ok        bool
		reason    string // substring; "" means must be empty
		fallsBack bool
	}{
		{"healthy fresh daemon (measured 0.61)", SelfFootprint{Footprint: 29 * mib / 10, Resident: 42 * mib / 10, RSS: 47 * mib / 10}, true, "", false},
		{"healthy long-running daemon (measured 1.06)", SelfFootprint{Footprint: 363 * mib / 10, Resident: 343 * mib / 10, RSS: 343 * mib / 10}, true, "", false},
		{"call failed", SelfFootprint{Err: syscall.ENOTSUP, RSS: 34 * mib}, false, "footprint call failed", true},
		{"ps unavailable", SelfFootprint{Footprint: 36 * mib, Resident: 34 * mib, RSSErr: errors.New("fork: resource temporarily unavailable")}, true, "unverified", false},
		{"ps returned zero", SelfFootprint{Footprint: 36 * mib, Resident: 34 * mib}, true, "unverified", false},
		{"zero footprint", SelfFootprint{Footprint: 0, Resident: 34 * mib, RSS: 34 * mib}, false, "footprint is 0", false},
		{"zero footprint without ps", SelfFootprint{Footprint: 0, RSSErr: errors.New("ps: timed out after 2s")}, false, "footprint is 0", false},
		{"resident mismatch, ratio fine", SelfFootprint{Footprint: 34 * mib, Resident: 3 * mib, RSS: 34 * mib}, false, "struct layout changed", false},
		{"footprint far above RSS", SelfFootprint{Footprint: 200 * mib, Resident: 34 * mib, RSS: 34 * mib}, false, "ratio", false},
		{"footprint far below RSS", SelfFootprint{Footprint: 2 * mib, Resident: 34 * mib, RSS: 34 * mib}, false, "ratio", false},
		{"at lower bounds", SelfFootprint{Footprint: 4 * mib, Resident: 16 * mib, RSS: 32 * mib}, true, "", false},
		{"at upper bounds", SelfFootprint{Footprint: 128 * mib, Resident: 64 * mib, RSS: 32 * mib}, true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason, fallsBack := JudgeSelfFootprint(tc.r)
			if ok != tc.ok || fallsBack != tc.fallsBack {
				t.Fatalf("JudgeSelfFootprint = (%v, %q, %v), want ok=%v fallsBack=%v", ok, reason, fallsBack, tc.ok, tc.fallsBack)
			}
			if tc.reason == "" && reason != "" {
				t.Fatalf("reason = %q, want none", reason)
			}
			if tc.reason != "" && !strings.Contains(reason, tc.reason) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.reason)
			}
		})
	}
}

func TestJudgeSelfFootprintZeroWarnsWithoutPS(t *testing.T) {
	// Rule C1.2 runs before C1.3: a live pigtaild never weighs 0, so a zero
	// footprint is a WARNING even with no ps figure to compare against.
	ok, reason, fallsBack := JudgeSelfFootprint(SelfFootprint{Footprint: 0, RSSErr: errors.New("exec: \"ps\": not found")})
	if ok || fallsBack || !strings.Contains(reason, "footprint is 0") {
		t.Fatalf("= (%v, %q, %v), want a broken verdict \"footprint is 0\"", ok, reason, fallsBack)
	}
}

func TestJudgeSelfFootprintUnverifiedWithoutPS(t *testing.T) {
	// A non-zero footprint with no ps figure: nothing to compare, no WARNING.
	ok, reason, _ := JudgeSelfFootprint(SelfFootprint{Footprint: 36 * mib, Resident: 34 * mib, RSSErr: errors.New("exec: \"ps\": not found")})
	if !ok || !strings.Contains(reason, "unverified") {
		t.Fatalf("= (%v, %q), want ok with an unverified reason", ok, reason)
	}
}

func TestFormatSelfCheck(t *testing.T) {
	cases := []struct {
		name string
		r    SelfFootprint
		want []string
		not  []string
	}{
		{"ok", SelfFootprint{Footprint: 36 * mib, Resident: 34 * mib, RSS: 34 * mib},
			[]string{"sysmem: self footprint=36MiB rss=34MiB (footprint reader ok)"}, []string{"WARNING"}},
		{"call failed", SelfFootprint{Err: syscall.ENOTSUP, RSS: 34 * mib},
			[]string{"sysmem: WARNING self footprint=? rss=34MiB", "footprint reader may be broken (macOS update?)", "footprint call failed", "memory figures fall back to ps RSS"}, nil},
		{"zero", SelfFootprint{Resident: 34 * mib, RSS: 34 * mib},
			[]string{"sysmem: WARNING self footprint=0MiB rss=34MiB", "footprint reader may be broken (macOS update?)", "footprint is 0", "memory figures are wrong"}, nil},
		{"zero without ps", SelfFootprint{RSSErr: errors.New("boom")},
			[]string{"sysmem: WARNING self footprint=0MiB rss=?", "footprint is 0", "memory figures are wrong"}, nil},
		{"unverified", SelfFootprint{Footprint: 36 * mib, Resident: 34 * mib, RSSErr: errors.New("boom")},
			[]string{"sysmem: self footprint=36MiB rss=? (footprint reader unverified: ps RSS unavailable: boom)"}, []string{"WARNING"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatSelfCheck(tc.r)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("line %q lacks %q", got, w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("line %q must not contain %q", got, n)
				}
			}
		})
	}
}
