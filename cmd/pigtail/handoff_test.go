package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runCLIOut is runCLI that keeps what the command printed on os.Stdout.
func runCLIOut(t *testing.T, s *stubDaemon, root func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	testutil.SerialOnly(t)
	var out bytes.Buffer
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		rd, wr, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stdout = wr
		done := make(chan struct{})
		go func() { _, _ = io.Copy(&out, rd); close(done) }()
		cmd := root()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
		_ = wr.Close()
		<-done
		os.Stdout = stdout
		_ = rd.Close()
	})
	return out.String(), err
}

func seedHandoffs(t *testing.T, st *store.Store, ents ...pigtail.SubjectLogEntry) {
	t.Helper()
	for i := range ents {
		ents[i].SubjectID = "sub_p"
		if err := st.AppendLog(&ents[i]); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	hTime0 = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	hMan   = pigtail.SubjectLogEntry{ID: "log_man", Timestamp: hTime0, Kind: pigtail.LogHandoff, Text: "manual text"}
	hAutoN = pigtail.SubjectLogEntry{ID: "handoff-auto:s-1:t-9", Timestamp: hTime0.Add(time.Hour), Kind: pigtail.LogHandoffAuto, Text: "[automatique]\nauto text"}
	hAutoO = pigtail.SubjectLogEntry{ID: "handoff-auto:s-0:t-1", Timestamp: hTime0.Add(-time.Hour), Kind: pigtail.LogHandoffAuto, Text: "old auto"}
)

func TestHandoffShowManualOnly(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	seedHandoffs(t, st, hMan)
	out, err := runCLIOut(t, s, handoffCmd, "show", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	want := "# handoff log_man — 2026-10-30T09:00:00Z\n\nmanual text\n"
	if out != want {
		t.Fatalf("out = %q\nwant %q", out, want)
	}
}

func TestHandoffShowAutoAfterManual(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	seedHandoffs(t, st, hMan, hAutoN)
	out, err := runCLIOut(t, s, handoffCmd, "show", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	want := "# handoff log_man — 2026-10-30T09:00:00Z\n\nmanual text\n" +
		"\n# handoff automatique handoff-auto:s-1:t-9 — session s-1 — 2026-10-30T10:00:00Z\n\n[automatique]\nauto text\n"
	if out != want {
		t.Fatalf("out = %q\nwant %q", out, want)
	}
}

func TestHandoffShowAutoOlderThanManualHidden(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	seedHandoffs(t, st, hMan, hAutoO)
	out, err := runCLIOut(t, s, handoffCmd, "show", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "automatique") || !strings.Contains(out, "manual text") {
		t.Fatalf("out = %q", out)
	}
}

func TestHandoffShowAutoOnly(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	out, err := runCLIOut(t, s, handoffCmd, "show", "sub_p")
	if err != nil || out != "no handoff yet\n" {
		t.Fatalf("empty: out=%q err=%v", out, err)
	}
	seedHandoffs(t, st, hAutoN)
	out, err = runCLIOut(t, s, handoffCmd, "show", "sub_p")
	if err != nil {
		t.Fatal(err)
	}
	want := "no manual handoff yet\n\n# handoff automatique handoff-auto:s-1:t-9 — session s-1 — 2026-10-30T10:00:00Z\n\n[automatique]\nauto text\n"
	if out != want {
		t.Fatalf("out = %q\nwant %q", out, want)
	}
}

func TestHandoffShowJSONFields(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	seedHandoffs(t, st, hMan, hAutoN)
	out, err := runCLIOut(t, s, handoffCmd, "show", "sub_p", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Handoff *struct {
			ID, Text string
			TS       time.Time
		}
		Auto *struct {
			ID, Session, Turn, Text string
			TS                      time.Time
		}
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v in %q", err, out)
	}
	if got.Handoff == nil || got.Handoff.ID != "log_man" || got.Handoff.Text != "manual text" || !got.Handoff.TS.Equal(hTime0) {
		t.Fatalf("handoff = %+v", got.Handoff)
	}
	if got.Auto == nil || got.Auto.Session != "s-1" || got.Auto.Turn != "t-9" || got.Auto.ID != hAutoN.ID || !strings.Contains(got.Auto.Text, "auto text") {
		t.Fatalf("auto = %+v", got.Auto)
	}
	// null when absent
	_, st2 := realSubjectDaemon(t)
	_ = st2
	s3, st3 := realSubjectDaemon(t)
	seedHandoffs(t, st3, hMan, hAutoO)
	out, _ = runCLIOut(t, s3, handoffCmd, "show", "sub_p", "--json")
	if !strings.Contains(out, `"auto":null`) {
		t.Fatalf("older auto must be null: %q", out)
	}
}

func TestParseHandoffAutoID(t *testing.T) {
	s, turn, ok := parseHandoffAutoID("handoff-auto:s-1:t-9")
	if !ok || s != "s-1" || turn != "t-9" {
		t.Fatalf("%q %q %v", s, turn, ok)
	}
	for _, bad := range []string{"log_x", "handoff-auto:", "handoff-auto:only", "handoff-auto::t"} {
		if _, _, ok := parseHandoffAutoID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
