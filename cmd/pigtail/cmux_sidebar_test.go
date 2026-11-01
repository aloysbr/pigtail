package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// retitle hands a hand-renamed workspace back to pigtail; perso writing a
// work subject needs --cross-scope; without a loop in the daemon, sidebar
// status still answers "off".
func TestCmuxRetitleAndSidebarE2E(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	for ws, sub := range map[string]string{attachWS: "sub_p", attachWS2: "sub_t"} {
		if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: sub, Source: "attach", BoundAt: time.UnixMilli(1)}); err != nil {
			t.Fatal(err)
		}
		_ = st.SetCmuxOwner(ws, store.CmuxFieldTitle, pigtail.CmuxOwnerUser)
	}
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	if err := runCLI(t, s, cmuxCmd, "retitle"); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.GetCmuxBinding(attachWS); b.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("not retitled: %+v", b)
	}
	wantScopeMismatch(t, "retitle work without --cross-scope", runCLI(t, s, cmuxCmd, "retitle", "--workspace", attachWS2))
	if err := runCLI(t, s, cmuxCmd, "retitle", "--workspace", attachWS2, "--cross-scope"); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, s, cmuxCmd, "sidebar", "status"); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, s, cmuxCmd, "sidebar", "sideways"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestPrintCmuxSidebar(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	printCmuxSidebar(&buf, &pigtail.CmuxSidebarState{
		Enabled: true, CmuxAvailable: true, LastReconcileAt: &at, Warnings: []string{"w1"},
		Workspaces: []pigtail.CmuxSidebarWorkspace{
			{CmuxWorkspaceID: attachWS, Attention: "waiting", WaitingCount: 2, TitleOwner: "user",
				Subject: &pigtail.CmuxSidebarSubject{ID: "sub_p", ShortTitle: "Sidebar pigtail"}},
			{CmuxWorkspaceID: attachWS2, Attention: "waiting", WaitingCount: 1, WaitType: "arbitrage", WaitReason: "pick one"},
			{CmuxWorkspaceID: "CCCCCCCC-0000-4000-8000-000000000003", TitleOwner: "pigtail", DisplayName: "Relance",
				Subject: &pigtail.CmuxSidebarSubject{ID: "sub_r", ShortTitle: "Ancien court"}},
		},
	})
	out := buf.String()
	// An old daemon (title_owner "user", no display_name): the short title
	// alone; a new one: its display_name. Never "renamed by hand" (D5).
	for _, want := range []string{"sidebar: on (cmux reachable)", "warning: w1", "waiting ×2", "Decision · pick one", "Sidebar pigtail", "sub_r", "Relance", attachWS2} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, not := range []string{"renamed by hand", "Ancien court"} {
		if strings.Contains(out, not) {
			t.Errorf("unexpected %q in:\n%s", not, out)
		}
	}
}

// Spec § 5 bis: the sidebar table names the new states by their Label(),
// with no reason given and no alignment on the pill text.
func TestPrintCmuxSidebarNewStates(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printCmuxSidebar(&buf, &pigtail.CmuxSidebarState{Enabled: true, CmuxAvailable: true, Workspaces: []pigtail.CmuxSidebarWorkspace{
		{CmuxWorkspaceID: attachWS, Attention: "waiting", WaitingCount: 1, WaitType: "your_turn"},
		{CmuxWorkspaceID: attachWS2, Attention: "waiting", WaitingCount: 1, WaitType: "done"},
		{CmuxWorkspaceID: "CCCCCCCC-0000-4000-8000-000000000003", Attention: "waiting", WaitingCount: 1, WaitType: "wrapped"},
	}})
	out := buf.String()
	for _, want := range []string{"Your turn", "Done", "Wrapped"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, not := range []string{"Waiting for you", "Done · "} {
		if strings.Contains(out, not) {
			t.Errorf("unexpected %q in:\n%s", not, out)
		}
	}
}

// The folder arrangement's state is one technical line (Task 7 of
// sub_x000000000000041): on or off, with the note that says why it idles.
func TestPrintCmuxSidebarFolders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		st   pigtail.CmuxSidebarState
		want string
	}{
		{pigtail.CmuxSidebarState{Enabled: true}, "folders: off\n"},
		{pigtail.CmuxSidebarState{Enabled: true, Folders: true}, "folders: on\n"},
		{pigtail.CmuxSidebarState{Enabled: true, Folders: true, FoldersNote: "folder created but never listed by cmux: folders stopped"}, "folders: on — folder created but never listed by cmux: folders stopped\n"},
	} {
		var buf bytes.Buffer
		printCmuxSidebar(&buf, &tc.st)
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("missing %q in:\n%s", tc.want, buf.String())
		}
	}
}

// D3 of sub_x000000000000019: the folder round owed and not yet applied, and
// the window the folders arrange (texts frozen by D7); per window since D32 of
// sub_x000000000000007.
func TestPrintCmuxSidebarFoldersWait(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-(3*time.Minute + 12*time.Second))
	st := pigtail.CmuxSidebarState{Enabled: true, Folders: true, FoldersWindows: []string{"6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5"},
		FoldersWait: []pigtail.CmuxFoldersWait{{Window: "6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5", Since: since.UTC(), Reason: "key"}}}
	var buf bytes.Buffer
	printCmuxSidebar(&buf, &st)
	for _, want := range []string{
		"folders wait: key since " + since.Local().Format("15:04:05") + " (3m12s)\n",
		"folders window: 6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
	st.FoldersWait, st.FoldersWindows = nil, nil
	buf.Reset()
	printCmuxSidebar(&buf, &st)
	if out := buf.String(); strings.Contains(out, "folders wait") || strings.Contains(out, "folders window") {
		t.Errorf("no wait, no window: got\n%s", out)
	}
}

// D32 of sub_x000000000000007: several windows, each its own wait line.
func TestPrintCmuxSidebarFoldersWaitSeveralWindows(t *testing.T) {
	t.Parallel()
	since := time.Now().Add(-time.Minute)
	a, b := "6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5", "11111111-2222-4333-8444-555555555555"
	st := pigtail.CmuxSidebarState{Enabled: true, Folders: true, FoldersWindows: []string{a, b},
		FoldersWait: []pigtail.CmuxFoldersWait{{Window: b, Since: since.UTC(), Reason: "settle"}}}
	var buf bytes.Buffer
	printCmuxSidebar(&buf, &st)
	for _, want := range []string{
		"folders wait 11111111: settle since " + since.Local().Format("15:04:05") + " (1m0s)\n",
		"folders windows: " + a + ", " + b + "\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "folders wait 6A73C8EF") {
		t.Errorf("a window that does not wait has a line:\n%s", buf.String())
	}
}
