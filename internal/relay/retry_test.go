package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestRetryPlan(t *testing.T) {
	stop := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		s       Series
		class   string
		stop    time.Time
		now     time.Time
		loc     *time.Location
		want    RetryStep
		wantLoc bool // the reason is checked against time.Local instead
	}{
		{name: "other, no try", class: claudesession.ClassOther, stop: stop, now: stop, loc: time.UTC,
			want: RetryStep{Due: stop.Add(time.Minute), Reason: "relance à 10:01 (1/5)", Try: 1, OtherTry: 1}},
		{name: "other, zone Paris", class: claudesession.ClassOther, stop: stop, now: stop, loc: paris,
			want: RetryStep{Due: stop.Add(time.Minute), Reason: "relance à 11:01 (1/5)", Try: 1, OtherTry: 1}},
		{name: "other, last try", s: Series{Tries: 4, OtherTries: 4}, class: claudesession.ClassOther, stop: stop, now: stop, loc: time.UTC,
			want: RetryStep{Due: stop.Add(20 * time.Minute), Reason: "relance à 10:20 (5/5)", Try: 5, OtherTry: 5}},
		{name: "other, exhausted", s: Series{Tries: 5, OtherTries: 5}, class: claudesession.ClassOther, stop: stop, now: stop, loc: time.UTC,
			want: RetryStep{Exhausted: true, Reason: ReasonExhausted}},
		{name: "network, waits", s: Series{Anchor: midnight}, class: claudesession.ClassNetwork, stop: stop, now: midnight.Add(11*time.Hour + 59*time.Minute), loc: time.UTC,
			want: RetryStep{NeedNetwork: true, Due: stop, Reason: ReasonNetwork, Try: 1}},
		{name: "network, 12 h", s: Series{Anchor: midnight}, class: claudesession.ClassNetwork, stop: stop, now: midnight.Add(12 * time.Hour), loc: time.UTC,
			want: RetryStep{Exhausted: true, Reason: ReasonExhausted}},
		{name: "network, no anchor = the stop", class: claudesession.ClassNetwork, stop: stop, now: stop.Add(11*time.Hour + 59*time.Minute), loc: time.UTC,
			want: RetryStep{NeedNetwork: true, Due: stop, Reason: ReasonNetwork, Try: 1}},
		{name: "mixed series", s: Series{Tries: 30, OtherTries: 2}, class: claudesession.ClassOther, stop: stop, now: stop, loc: time.UTC,
			want: RetryStep{Due: stop.Add(5 * time.Minute), Reason: "relance à 10:05 (3/5)", Try: 31, OtherTry: 3}},
		{name: "nil zone is time.Local", class: claudesession.ClassOther, stop: stop, now: stop, loc: nil, wantLoc: true,
			want: RetryStep{Due: stop.Add(time.Minute), Try: 1, OtherTry: 1}},
	}
	for _, c := range cases {
		got := PlanRetry(c.s, c.class, c.stop, c.now, c.loc)
		if c.wantLoc {
			c.want.Reason = "relance à " + stop.Add(time.Minute).In(time.Local).Format("15:04") + " (1/5)"
		}
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestRetryAllowed(t *testing.T) {
	apiWait := func() []pigtail.SessionWait { return wait("api_error", pigtail.WaitSourceAuto) }
	cases := []struct {
		name string
		edit func(*Candidate)
		want bool
	}{
		{"api_error wait, idle", func(c *Candidate) { c.Waits = apiWait() }, true},
		{"limit wait after its end", func(c *Candidate) {
			c.Waits = waitEnd("limit", pigtail.WaitSourceAuto, t0.Add(-time.Hour), 0)
		}, true},
		{"open watcher is no refusal", func(c *Candidate) { c.Waits = apiWait(); c.Watched = true }, true},
		{"standby", func(c *Candidate) { c.Waits = apiWait(); c.Standby = true }, false},
		{"standby unreadable", func(c *Candidate) { c.Waits = apiWait(); c.Standby = true; c.StandbyReason = "standby unreadable" }, false},
		{"status busy", func(c *Candidate) { c.Waits = apiWait(); c.Native.Status = "busy" }, false},
		{"status shell", func(c *Candidate) { c.Waits = apiWait(); c.Native.Status = "shell" }, false},
		{"parked", func(c *Candidate) { c.Waits = apiWait(); c.Native.ParkedJobID = "job1" }, false},
		{"frozen", func(c *Candidate) { c.Waits = apiWait(); c.Frozen = true }, false},
		{"declared review", func(c *Candidate) { c.Waits = append(apiWait(), wait("review", pigtail.WaitSourceDeclared)...) }, false},
		{"no surface", func(c *Candidate) { c.Waits = apiWait(); c.SurfaceID = "" }, false},
	}
	for _, tc := range cases {
		c := ready()
		tc.edit(&c)
		if ok, why := RetryAllowed(c); ok != tc.want {
			t.Errorf("%s: RetryAllowed = %v (%q), want %v", tc.name, ok, why, tc.want)
		}
	}
}

// stopScreen is the full stop screen of a usage limit, the lines of
// wake_test.go's fixtures (a real post-reset capture replaces it at verify).
func screenWithAboveBox(t *testing.T, block string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	free := string(b)
	i := strings.Index(free, "──────────")
	if i < 0 {
		t.Fatal("fixture has no box")
	}
	return free[:i] + block + free[i:]
}

func TestScreenAllowsAfterLimitReset(t *testing.T) {
	stop := "⏺ Usage limit reached · resets 3pm\n  ⎿  You've hit your session limit · resets 11:40pm\n     /usage-credits to finish what you’re working on.\n\n"
	s := screenWithAboveBox(t, stop)
	if ok, why := ScreenAllowsAfterLimitReset(s, false); !ok {
		t.Errorf("full stop screen after the reset: refused (%s)", why)
	}
	if ok, _ := ScreenAllows(s, false); ok {
		t.Error("ScreenAllows must still refuse the stop screen")
	}
	for name, block := range map[string]string{
		"auto-continue banner": "⏺ Usage limit reached · continuing automatically at 3pm\n\n",
		"waiting + continuing": "⏺ Usage limit reached · resets 3pm\n  ⎿  Waiting… continuing automatically at 3pm\n\n",
		"waiting alone":        "⏺ Reprise\n  ⎿  Waiting…\n\n",
		"stop line and banner": "  ⎿  You've hit your limit · continuing automatically\n\n",
	} {
		if ok, _ := ScreenAllowsAfterLimitReset(screenWithAboveBox(t, block), false); ok {
			t.Errorf("%s: allowed", name)
		}
	}
	if ok, _ := ScreenAllowsAfterLimitReset(s, true); ok {
		t.Error("focused pane allowed")
	}
	draft := withInput(s, "❯ du texte en cours")
	if ok, why := ScreenAllowsAfterLimitReset(draft, false); ok || why != "draft in the input" {
		t.Errorf("draft: %v %q", ok, why)
	}
}

func TestPauseHoldsLimitUntilResume(t *testing.T) {
	reset := t0.Add(-10 * time.Second)
	c := ready()
	c.Native.StatusUpdatedAt = t0.Add(-2 * time.Hour).UnixMilli()
	c.Waits = waitEnd("limit", pigtail.WaitSourceAuto, reset, 0)
	c.LimitResumePending = true
	const threshold = time.Hour
	if ok, why := PauseAllowed(c, threshold); ok || why != "limit reset: resume pending" {
		t.Fatalf("pending at the reset: %v %q", ok, why)
	}
	c.Now = reset.Add(LimitResumeGrace)
	if ok, why := PauseAllowed(c, threshold); ok || why != "limit reset: resume pending" {
		t.Fatalf("pending at reset+grace: %v %q", ok, why)
	}
	c.Now = reset.Add(LimitResumeGrace + LimitResumeHoldMax - time.Second)
	if ok, _ := PauseAllowed(c, threshold); ok {
		t.Fatal("pending 1 s before the bound")
	}
	c.Now = reset.Add(LimitResumeGrace + LimitResumeHoldMax)
	if ok, why := PauseAllowed(c, threshold); !ok {
		t.Fatalf("hold must end at the bound: %q", why)
	}
	c.Now = reset.Add(LimitResumeGrace)
	c.LimitResumePending = false // a marker, or a Standby session
	if ok, why := PauseAllowed(c, threshold); !ok {
		t.Fatalf("no pending flag, no hold: %q", why)
	}
}
