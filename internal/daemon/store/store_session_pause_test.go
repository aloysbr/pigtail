package store

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var pauseT0 = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

func pauseRow(session, surface string) pigtail.SessionPause {
	return pigtail.SessionPause{
		SessionID: session, PID: 8, SurfaceID: surface, CmuxWorkspaceID: "WS-" + session,
		SidPath: "/r/_dev_ttys001.sid", State: pigtail.PauseStopping, EnteredStandby: true, Since: pauseT0,
	}
}

// The pause row (sub_x000000000000036): begun stopping, signaled, paused,
// ended once.
func TestSessionPauseLifecycle(t *testing.T) {
	st := openMem(t)
	p := pauseRow("s1", "SURF")
	p.CmuxWorkspaceID = "ws-lower"
	if err := st.BeginPause(p); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPause("s1")
	if err != nil || got == nil || got.State != pigtail.PauseStopping || got.SignaledAt != nil || got.PausedAt != nil ||
		got.PID != 8 || got.SidPath != p.SidPath || !got.EnteredStandby || !got.Since.Equal(pauseT0) {
		t.Fatalf("after begin = %+v, %v", got, err)
	}
	if got.CmuxWorkspaceID != "WS-LOWER" {
		t.Fatalf("workspace = %q, want the cmux UUID in upper case", got.CmuxWorkspaceID)
	}
	if err := st.MarkSignaled("s1", pauseT0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetPause("s1"); got.SignaledAt == nil || !got.SignaledAt.Equal(pauseT0.Add(time.Second)) {
		t.Fatalf("after signal = %+v", got)
	}
	if err := st.MarkPaused("s1", pauseT0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetPause("s1"); got.State != pigtail.PausePaused || got.PausedAt == nil || !got.PausedAt.Equal(pauseT0.Add(2*time.Second)) {
		t.Fatalf("after paused = %+v", got)
	}
	if err := st.BeginPause(pauseRow("s2", "OTHER")); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.EndPause("s1"); err != nil || !ok {
		t.Fatalf("EndPause = %v, %v", ok, err)
	}
	if ok, err := st.EndPause("s1"); err != nil || ok {
		t.Fatalf("second EndPause = %v, %v", ok, err)
	}
	if got, err := st.GetPause("s1"); err != nil || got != nil {
		t.Fatalf("after end = %+v, %v", got, err)
	}
	if err := st.MarkPaused("s1", pauseT0); err == nil {
		t.Fatal("MarkPaused on a missing row succeeded")
	}
}

func TestBeginPauseTwiceFails(t *testing.T) {
	st := openMem(t)
	if err := st.BeginPause(pauseRow("s1", "SURF")); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginPause(pauseRow("s1", "SURF")); err == nil {
		t.Fatal("second BeginPause succeeded")
	}
}

func TestEndPauseReplacedOnSurface(t *testing.T) {
	st := openMem(t)
	if err := st.BeginPause(pauseRow("s1", "SURF")); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginPause(pauseRow("s2", "OTHER")); err != nil {
		t.Fatal(err)
	}
	ids, err := st.EndPauseReplacedOnSurface("surf", "s3")
	if err != nil || strings.Join(ids, ",") != "s1" {
		t.Fatalf("replaced = %v, %v; want [s1]", ids, err)
	}
	if got, _ := st.GetPause("s2"); got == nil {
		t.Fatal("s2 on another surface went")
	}
	if ids, _ := st.EndPauseReplacedOnSurface("OTHER", "s2"); len(ids) != 0 {
		t.Fatalf("the same session replaced itself: %v", ids)
	}
	if ids, _ := st.EndPauseReplacedOnSurface("", "s9"); len(ids) != 0 {
		t.Fatalf("empty surface: %v", ids)
	}
}

func TestListPausesOrdered(t *testing.T) {
	st := openMem(t)
	for _, s := range []string{"s3", "s1", "s2"} {
		if err := st.BeginPause(pauseRow(s, "SURF-"+s)); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := st.ListPauses()
	if err != nil || len(ps) != 3 || ps[0].SessionID != "s1" || ps[1].SessionID != "s2" || ps[2].SessionID != "s3" {
		t.Fatalf("ListPauses = %+v, %v", ps, err)
	}
}
