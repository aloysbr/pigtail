package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// held stands in for a lease the daemon actually granted. Only its
// non-nilness matters to testLockEnv, which is exactly the point: the
// variable must never be set on a run that was not admitted.
func held() *pigtail.AdmissionLease { return &pigtail.AdmissionLease{} }

func TestTestLockEnvSetOnlyForHeldBrowserLease(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lease *pigtail.AdmissionLease
		class string
		env   []string
		want  []string
	}{
		{
			name:  "held browser lease disables the repo's own lock",
			lease: held(),
			class: string(heavycmd.ClassVitestBrowser),
			env:   []string{"PATH=/usr/bin"},
			want:  []string{"TEST_LOCK_DISABLE=1"},
		},
		{
			// Fail-open, PIGTAIL_ADMIT=0, admission disabled, nested: admit
			// returns a nil lease and the run is unarbitrated. Disarming the
			// repo's lock there would remove the only serialisation left.
			name:  "no lease held: leave the repo's lock armed",
			lease: nil,
			class: string(heavycmd.ClassVitestBrowser),
			env:   []string{"PATH=/usr/bin"},
			want:  nil,
		},
		{
			name:  "another class: not our business",
			lease: held(),
			class: string(heavycmd.ClassVitest),
			env:   []string{"PATH=/usr/bin"},
			want:  nil,
		},
		{
			name:  "user already set it: never overridden",
			lease: held(),
			class: string(heavycmd.ClassVitestBrowser),
			env:   []string{"PATH=/usr/bin", "TEST_LOCK_DISABLE=0"},
			want:  nil,
		},
		{
			name:  "user set it to 1 themselves: nothing to add",
			lease: held(),
			class: string(heavycmd.ClassVitestBrowser),
			env:   []string{"TEST_LOCK_DISABLE=1"},
			want:  nil,
		},
		{
			name:  "empty value still counts as set by the user",
			lease: held(),
			class: string(heavycmd.ClassVitestBrowser),
			env:   []string{"TEST_LOCK_DISABLE="},
			want:  nil,
		},
		{
			// A variable merely *containing* the name must not be mistaken
			// for it.
			name:  "lookalike variable does not count as set",
			lease: held(),
			class: string(heavycmd.ClassVitestBrowser),
			env:   []string{"MY_TEST_LOCK_DISABLE=0"},
			want:  []string{"TEST_LOCK_DISABLE=1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testLockEnv(tc.lease, tc.class, tc.env)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("testLockEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The unit table above proves the rule; this proves it is wired to the
// command that actually runs — the gap that has cost this repo a bug per PR
// when only the generated text, and not the running thing, was asserted on.
func TestRunAdmittedPassesTheTestLockHatchDown(t *testing.T) {
	startAdmissionDaemon(t, 2)
	out := filepath.Join(t.TempDir(), "seen")
	script := `echo "${TEST_LOCK_DISABLE-unset}" > "$0"`

	if code := runAdmitted([]string{"sh", "-c", script, out},
		admitOptions{class: string(heavycmd.ClassVitestBrowser), maxWait: time.Second, onTimeout: "fail"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got, _ := os.ReadFile(out); strings.TrimSpace(string(got)) != "1" {
		t.Fatalf("browser run saw TEST_LOCK_DISABLE=%q, want 1", strings.TrimSpace(string(got)))
	}

	if code := runAdmitted([]string{"sh", "-c", script, out},
		admitOptions{class: string(heavycmd.ClassVitest), maxWait: time.Second, onTimeout: "fail"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got, _ := os.ReadFile(out); strings.TrimSpace(string(got)) != "unset" {
		t.Fatalf("node-mode run saw TEST_LOCK_DISABLE=%q, want it unset", strings.TrimSpace(string(got)))
	}
}

// Fail-open is the case that matters most: with no daemon the run is not
// serialised by pigtail at all, so the repo's own lock must stay armed.
func TestRunAdmittedLeavesTheLockArmedWithoutADaemon(t *testing.T) {
	pointClientAt(t, filepath.Join(t.TempDir(), "absent.sock"))
	out := filepath.Join(t.TempDir(), "seen")
	if code := runAdmitted([]string{"sh", "-c", `echo "${TEST_LOCK_DISABLE-unset}" > "$0"`, out},
		admitOptions{class: string(heavycmd.ClassVitestBrowser), maxWait: time.Second, onTimeout: "fail"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got, _ := os.ReadFile(out); strings.TrimSpace(string(got)) != "unset" {
		t.Fatalf("unadmitted run saw TEST_LOCK_DISABLE=%q, want it unset", strings.TrimSpace(string(got)))
	}
}

// A2 (2026-10-28): the hook routes `npm run test-storybook` as vitest-browser,
// so the lease it takes disarms the repo's own lock, as `pigtail test` does.
func TestHookRoutedStorybookDisarmsTheRepoLock(t *testing.T) {
	app, err := filepath.Abs(filepath.Join("..", "..", "internal", "heavycmd", "testdata", "app"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := heavycmd.Classify([]string{"npm", "run", "test-storybook", "--", "--run"}, app)
	if c != heavycmd.ClassVitestBrowser {
		t.Fatalf("class = %q, want vitest-browser", c)
	}
	if got := testLockEnv(held(), string(c), []string{"PATH=/usr/bin"}); !slices.Equal(got, []string{"TEST_LOCK_DISABLE=1"}) {
		t.Fatalf("env = %q", got)
	}
}
