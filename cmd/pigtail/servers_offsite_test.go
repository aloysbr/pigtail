package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail servers` is where the eye already goes for dev servers; the
// offsite backup earns a line there only when it is screaming. Anything less
// belongs solely to `pigtail db list`, which is where the full detail lives.
// These four cases pin the boundary: alarm shows, warn/ok/unconfigured stay
// silent.

func serversRespWithOneServer() pigtail.ServersResponse {
	return pigtail.ServersResponse{
		Servers: []pigtail.ServerView{{WorkspaceID: "ws1", Server: "dev", Port: 3005}},
	}
}

func TestPrintServersShowsOffsiteAlarm(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := &pigtail.OffsiteView{
		DestDir:  "/Volumes/nas/pigtail",
		Status:   "failing",
		Severity: "alarm",
		Detail:   "FAILING — offsite destination unreachable: /Volumes/nas/pigtail",
	}
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if !strings.Contains(out, "FAILING — offsite destination unreachable: /Volumes/nas/pigtail") {
		t.Fatalf("an alarm-severity offsite state must be shown with its RenderOffsiteDetail wording, got:\n%s", out)
	}
}

func TestPrintServersHidesOffsiteAtWarn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := &pigtail.OffsiteView{
		DestDir:  "/Volumes/nas/pigtail",
		Status:   "stale",
		Severity: "warn",
		Detail:   "STALE — no copy for 4h00m, with nothing reported as failing",
	}
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if strings.Contains(out, "STALE") || strings.Contains(out, "offsite") {
		t.Fatalf("a warn-severity offsite state must stay silent in `pigtail servers`, got:\n%s", out)
	}
}

func TestPrintServersHidesOffsiteAtOK(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := &pigtail.OffsiteView{
		DestDir:  "/Volumes/nas/pigtail",
		Status:   "ok",
		Severity: "ok",
		Detail:   "ok",
	}
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if strings.Contains(out, "offsite") {
		t.Fatalf("a healthy offsite state must not appear in `pigtail servers`, got:\n%s", out)
	}
}

func TestPrintServersHidesOffsiteWhenUnconfigured(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printServers(&buf, serversRespWithOneServer(), nil)
	out := buf.String()
	if strings.Contains(out, "offsite") {
		t.Fatalf("no destination configured must mean no line at all, got:\n%s", out)
	}
}

// The regression this guards against: a daemon newer than this CLI could
// send a severity this build has never heard of. The old gate compared
// `offsite.Severity != "alarm"` by string equality, which reads any
// unrecognized value as "not alarm" and stays silent — a next tranche adding
// a severity would have gone quiet on exactly the daemons it was meant to
// warn about. An unrecognized severity must show, not disappear.
func TestPrintServersShowsOffsiteOnAnUnrecognizedSeverity(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := &pigtail.OffsiteView{
		DestDir:  "/Volumes/nas/pigtail",
		Status:   "degraded",
		Severity: "critical",
		Detail:   "UNKNOWN STATUS \"degraded\" — treat as unhealthy until this build is updated",
	}
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if !strings.Contains(out, "offsite backup") {
		t.Fatalf("an unrecognized severity must not go silent, got:\n%s", out)
	}
}
