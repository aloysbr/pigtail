package main

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestParseLidHours(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want int
		ok   bool
	}{{nil, 8, true}, {[]string{"3"}, 3, true}, {[]string{"24"}, 24, true},
		{[]string{"0"}, 0, false}, {[]string{"25"}, 0, false}, {[]string{"abc"}, 0, false}, {[]string{"1.5"}, 0, false}} {
		got, err := parseLidHours(c.args)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("parseLidHours(%v) = %d, %v", c.args, got, err)
		}
	}
}

func TestPrintLid(t *testing.T) {
	t.Parallel()
	dl := time.Date(2026, 10, 16, 16, 44, 0, 0, time.Local)
	cases := []struct {
		r    pigtail.LidResponse
		want []string
	}{
		{pigtail.LidResponse{}, []string{"off — normal sleep"}},
		{pigtail.LidResponse{SleepDisabled: true, Deadline: &dl, BatteryFloor: 25}, []string{"on until 16:44", "<= 25%"}},
		{pigtail.LidResponse{SleepDisabled: true}, []string{"on (no timer)", "pigtail lid off"}},
		{pigtail.LidResponse{LastOff: &pigtail.LidOff{At: dl, Reason: "battery 14%"}, LastError: "auto-off (deadline) failed: x"},
			[]string{"last off: 16:44 (battery 14%)", "error: auto-off (deadline) failed: x"}},
	}
	for _, c := range cases {
		var b bytes.Buffer
		printLid(&b, c.r)
		for _, w := range c.want {
			if !strings.Contains(b.String(), w) {
				t.Errorf("missing %q in:\n%s", w, b.String())
			}
		}
	}
}

func TestPrintLidGuards(t *testing.T) {
	t.Parallel()
	dl := time.Date(2026, 10, 16, 16, 44, 0, 0, time.Local)
	closed, open := true, false
	cases := []struct {
		r    pigtail.LidResponse
		want []string
		not  []string
	}{
		{pigtail.LidResponse{SleepDisabled: true, Deadline: &dl, BatteryFloor: 25, ThermalState: "serious", LidClosed: &closed},
			[]string{"on until 16:44", "<= 25%", "or when the Mac runs hot with the lid closed", "thermal: serious, lid closed"}, nil},
		{pigtail.LidResponse{SleepDisabled: true, Deadline: &dl, BatteryFloor: 25, ThermalState: "serious", LidClosed: &open},
			[]string{"thermal: serious, lid open"}, nil},
		{pigtail.LidResponse{SleepDisabled: true, Deadline: &dl, BatteryFloor: 25, ThermalState: "serious"},
			[]string{"thermal: serious\n"}, []string{", lid"}},
		{pigtail.LidResponse{SleepDisabled: true, Deadline: &dl, BatteryFloor: 25, GuardError: "lid reading failed: x"},
			[]string{"guard: lid reading failed: x"}, nil},
		{pigtail.LidResponse{LastOff: &pigtail.LidOff{At: dl, Reason: "thermal critical"}},
			[]string{"last off: 16:44 (thermal critical)"}, nil},
	}
	for _, c := range cases {
		var b bytes.Buffer
		printLid(&b, c.r)
		for _, w := range c.want {
			if !strings.Contains(b.String(), w) {
				t.Errorf("missing %q in:\n%s", w, b.String())
			}
		}
		for _, n := range c.not {
			if strings.Contains(b.String(), n) {
				t.Errorf("unexpected %q in:\n%s", n, b.String())
			}
		}
	}
}

func TestLidErrorNotSetUp(t *testing.T) {
	t.Parallel()
	err := lidError(&pigtail.APIError{Code: pigtail.ErrLidNotSetUp, Message: "x"})
	if !strings.Contains(err.Error(), "pigtail lid setup") {
		t.Fatalf("got %v", err)
	}
	other := errors.New("dial unix: no such file")
	if lidError(other) != other { //nolint:errorlint // identity: the error passes through unchanged
		t.Fatal("other errors pass through unchanged")
	}
	late := lidError(&url.Error{Op: "Post", URL: "http://pigtail/lid", Err: context.DeadlineExceeded})
	if !strings.Contains(late.Error(), "did not answer within 90 s") || strings.Contains(late.Error(), "not reachable") {
		t.Fatalf("deadline: got %v", late)
	}
}

func TestLidTravelAlias(t *testing.T) {
	c, _, err := newRootCmd().Find([]string{"travel", "on"})
	if err != nil || c.Name() != "on" || c.Parent() == nil || c.Parent().Name() != "lid" {
		t.Fatalf("pigtail travel on → %v (%v), want lid on", c.CommandPath(), err)
	}
	if got := lidCmd().Aliases; len(got) != 1 || got[0] != "travel" {
		t.Fatalf("Aliases = %v", got)
	}
}
