package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The fallback route (OffsiteView.Fallback) is a second, independent
// classification the daemon exposes alongside the primary — per
// docs/wire-protocol.md's GET /backup "fallback" subsection, a client
// driving a single glyph must take the max severity rank of the two routes,
// never fold them into one printed detail. These four cases pin that
// contract for `pigtail servers`: the fallback gets its own gate, at the
// same alarm floor as the primary, and its own sentence.

func offsiteOKPrimary(fb *pigtail.OffsiteFallbackView) *pigtail.OffsiteView {
	return &pigtail.OffsiteView{
		DestDir:  "/Volumes/nas/pigtail",
		Status:   "ok",
		Severity: "ok",
		Detail:   "ok",
		Fallback: fb,
	}
}

// TestPrintServersHidesAFallbackAtWarn pins the decision *not* to lower the
// fallback gate to warn: Fallback.Severity is documented as never reaching
// alarm today, so a warn-severity fallback next to a healthy primary is the
// routine, permanent-by-design state (see printOffsiteAlarm's doc comment)
// and must stay silent, exactly like the primary's own warn case.
func TestPrintServersHidesAFallbackAtWarn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := offsiteOKPrimary(&pigtail.OffsiteFallbackView{
		Dest:     "nas-fallback",
		Status:   "not_yet_exercised",
		Severity: "warn",
		Detail:   "NOT YET EXERCISED — fallback nas-fallback has not been tried",
	})
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if strings.Contains(out, "fallback") || strings.Contains(out, "FALLBACK") {
		t.Fatalf("a warn-severity fallback must stay silent in `pigtail servers`, got:\n%s", out)
	}
}

// TestPrintServersShowsAFallbackWithAnUnknownSeverity is the only case
// observable today: Fallback.Severity never reaches "alarm" through the
// documented classification, so the fallback gate can presently only open
// on a value this build does not recognize — which, ranked by
// OffsiteSeverityAtLeast, must never read as quieter than alarm.
func TestPrintServersShowsAFallbackWithAnUnknownSeverity(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := offsiteOKPrimary(&pigtail.OffsiteFallbackView{
		Dest:     "nas-fallback",
		Status:   "failing",
		Severity: "catastrophe",
		Detail:   "UNKNOWN FALLBACK SEVERITY \"catastrophe\" — treat as unhealthy",
	})
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if !strings.Contains(out, "⚠ offsite fallback: UNKNOWN FALLBACK SEVERITY \"catastrophe\" — treat as unhealthy") {
		t.Fatalf("an unrecognized fallback severity must not go silent, got:\n%s", out)
	}
}

// TestPrintServersShowsBothLinesWhenBothScream checks both gates can fire
// together, primary first — the order printOffsiteAlarm writes them in.
func TestPrintServersShowsBothLinesWhenBothScream(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := &pigtail.OffsiteView{
		DestDir:  "/Volumes/nas/pigtail",
		Status:   "failing",
		Severity: "alarm",
		Detail:   "FAILING — offsite destination unreachable: /Volumes/nas/pigtail",
		Fallback: &pigtail.OffsiteFallbackView{
			Dest:     "nas-fallback",
			Status:   "failing",
			Severity: "catastrophe",
			Detail:   "UNKNOWN FALLBACK SEVERITY \"catastrophe\" — treat as unhealthy",
		},
	}
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	primaryLine := "⚠ offsite backup: FAILING — offsite destination unreachable: /Volumes/nas/pigtail"
	fallbackLine := "⚠ offsite fallback: UNKNOWN FALLBACK SEVERITY \"catastrophe\" — treat as unhealthy"
	primaryIdx := strings.Index(out, primaryLine)
	fallbackIdx := strings.Index(out, fallbackLine)
	if primaryIdx == -1 || fallbackIdx == -1 {
		t.Fatalf("both the primary and fallback alarm lines must be present, got:\n%s", out)
	}
	if primaryIdx > fallbackIdx {
		t.Fatalf("the primary line must come before the fallback line, got:\n%s", out)
	}
}

// TestPrintServersNamesTheFallbackDetailNotThePrimaryOne is the test that
// forbids the naive fold: taking the max severity of the two routes but
// printing the primary's Detail. A healthy primary whose own Detail happens
// to be the literal string "ok", paired with an unrecognized-severity
// fallback, must never produce "offsite backup: ok" — each route prints its
// own sentence, gated by its own severity.
func TestPrintServersNamesTheFallbackDetailNotThePrimaryOne(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	offsite := offsiteOKPrimary(&pigtail.OffsiteFallbackView{
		Dest:     "nas-fallback",
		Status:   "failing",
		Severity: "catastrophe",
		Detail:   "UNKNOWN FALLBACK SEVERITY \"catastrophe\" — treat as unhealthy",
	})
	printServers(&buf, serversRespWithOneServer(), offsite)
	out := buf.String()
	if strings.Contains(out, "offsite backup: ok") {
		t.Fatalf("the primary's healthy detail must never be printed just because the fallback screamed, got:\n%s", out)
	}
}
