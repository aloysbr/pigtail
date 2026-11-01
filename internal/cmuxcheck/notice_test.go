package cmuxcheck

import (
	"strings"
	"testing"
	"time"
)

func started(name string) Row {
	return Row{Title: name, Done: []Action{ActStart}, Result: "repaired"}
}

// D2: nothing when every session came back.
func TestNoticeLineSilentWhenAllFine(t *testing.T) {
	r := Report{Rows: []Row{started("a"), started("b"), {Title: "c", Info: []string{"binding autoResume=false: not resumed, by design"}}}}
	if got := NoticeLine(r); got != "" {
		t.Fatalf("NoticeLine = %q, want empty (D2)", got)
	}
}

// D2: one line — the count, then what is left for the user, named with why;
// three names at most, then +N; the command to read the rest.
func TestNoticeLineNamesWhatIsLeft(t *testing.T) {
	rows := []Row{}
	for i := 0; i < 14; i++ {
		rows = append(rows, started("s"))
	}
	rows = append(rows,
		Row{WorkspaceTitle: "Rain gauge report", Remaining: true, Blocked: "busy: status waiting"},
		Row{Title: "Console", Remaining: true, Blocked: "busy: " + focusReason},
		Row{Title: "Danger", Remaining: true, Blocked: "binding runs bypassPermissions: start it by hand (D8)"},
		Row{Title: "Broken", Remaining: true, Result: "start failed: x", Actions: []Action{ActStart}},
	)
	got := NoticeLine(Report{Rows: rows})
	want := "14 relancées · Rain gauge report attend ta réponse · Console au premier plan, à lancer · Danger à lancer à la main +1 — pigtail cmux check --last"
	if got != want {
		t.Fatalf("NoticeLine =\n%q\nwant\n%q", got, want)
	}
}

// Review Focus 5: a pass that looked at nothing says so.
func TestNoticeLineFatal(t *testing.T) {
	got := NoticeLine(Report{Fatal: "cmux unreachable (socket): refused"})
	if !strings.HasPrefix(got, "Reprise cmux impossible") || !strings.Contains(got, "pigtail cmux check --fix") {
		t.Fatalf("NoticeLine = %q", got)
	}
}

// Un démarrage arrêté sur un dialogue : pas de Blocked, Result « start
// failed: stopped on a dialog » — le bandeau dit « attend ta réponse », pas
// « relance ratée ». Et jamais un identifiant : sans titre, « une session ».
func TestNoticeLineDialogStopAndNoID(t *testing.T) {
	rows := []Row{
		{Title: "Dlg", Remaining: true, Actions: []Action{ActStart},
			Result: "start failed: stopped on a dialog (trust): nothing typed — answer it, then run `pigtail cmux check --fix`"},
		{SurfaceID: "9F3A2B1C-0000-4000-8000-000000000000", Remaining: true, Blocked: "busy: " + focusReason},
	}
	got := NoticeLine(Report{Rows: rows})
	if !strings.Contains(got, "Dlg attend ta réponse") || strings.Contains(got, "relance ratée") {
		t.Fatalf("NoticeLine = %q, want the dialog stop as « attend ta réponse »", got)
	}
	if !strings.Contains(got, "une session au premier plan") || strings.Contains(strings.ToLower(got), "9f3a2b1c") {
		t.Fatalf("NoticeLine = %q, want « une session », no surface id", got)
	}
}

func TestNoticeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	n := Notice{LaunchPID: 42, At: time.Date(2026, 10, 28, 8, 30, 0, 0, time.UTC), Line: "x"}
	if err := SaveNotice(dir, n); err != nil {
		t.Fatal(err)
	}
	got, err := LoadNotice(dir)
	if err != nil || got != n {
		t.Fatalf("LoadNotice = %+v, %v", got, err)
	}
}
