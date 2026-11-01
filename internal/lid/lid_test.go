package lid

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSetArgsExact(t *testing.T) {
	want1 := []string{"-n", "/usr/bin/pmset", "-a", "disablesleep", "1"}
	want0 := []string{"-n", "/usr/bin/pmset", "-a", "disablesleep", "0"}
	if !slices.Equal(SetArgs(true), want1) || !slices.Equal(SetArgs(false), want0) {
		t.Fatalf("SetArgs = %v / %v", SetArgs(true), SetArgs(false))
	}
}

func TestProbeArgs(t *testing.T) {
	for _, cur := range []string{"0", "1"} {
		want := []string{"-n", "-k", "/usr/bin/pmset", "-a", "disablesleep", cur}
		if got := ProbeArgs(cur); !slices.Equal(got, want) {
			t.Fatalf("ProbeArgs(%q) = %v, want %v (-n never prompts; -k: a cached sudo timestamp must not count; value = current state, a no-op write)", cur, got, want)
		}
	}
	for _, bad := range []string{"", "2", "01", "-1", "on", "1 x"} {
		if ProbeArgs(bad) != nil {
			t.Errorf("ProbeArgs(%q) accepted: only the current state 0|1 may be written", bad)
		}
	}
}

func TestParseSleepDisabled(t *testing.T) {
	on := "System-wide power settings:\n SleepDisabled\t\t1\nCurrently in use:\n standby 1\n"
	off := "System-wide power settings:\n SleepDisabled\t\t0\n"
	if !ParseSleepDisabled(on) || ParseSleepDisabled(off) || ParseSleepDisabled("Currently in use:\n sleep 1\n") {
		t.Fatal("ParseSleepDisabled wrong")
	}
}

func TestClassifySudo(t *testing.T) {
	for _, s := range []string{"sudo: a password is required\n", "Sorry, user x is not allowed to execute '/usr/bin/pmset -a disablesleep 1' as root\n"} {
		if !errors.Is(classifySudo(s, errors.New("exit status 1")), ErrNotSetUp) {
			t.Errorf("%q not classified as ErrNotSetUp", s)
		}
	}
	if err := classifySudo("pmset: boom\n", errors.New("exit status 2")); errors.Is(err, ErrNotSetUp) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("other failure: %v", err)
	}
}
