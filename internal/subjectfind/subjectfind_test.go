package subjectfind

import (
	"reflect"
	"testing"

	"github.com/aloysbr/pigtail/internal/suggest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func ids(cs []Candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// filler keeps the idf of the words under test above zero.
var filler = []Subject{
	{ID: "sub_f1", Title: "Board columns", Theme: "pigtail", Status: pigtail.StatusBuilding},
	{ID: "sub_f2", Title: "Garden shortcut", Theme: "garden", Status: pigtail.StatusBuilding},
	{ID: "sub_f3", Title: "Ledger export", Theme: "garden", Status: pigtail.StatusInbox},
}

func corpus(s ...Subject) []Subject { return append(append([]Subject{}, s...), filler...) }

func TestTermsKeepsAcronyms(t *testing.T) {
	got := Terms("Revue DS du PR #12 : UI à revoir")
	if want := []string{"revue", "revoir", "ds", "pr", "ui"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms = %v, want %v", got, want)
	}
	// suggest's own tokens stay as they were: its hooks run on every prompt.
	if got := suggest.Tokens("Revue DS du PR"); !reflect.DeepEqual(got, []string{"revue"}) {
		t.Fatalf("suggest.Tokens changed: %v", got)
	}
}

func TestRankPrefix(t *testing.T) {
	got := Rank("notif", corpus(Subject{ID: "sub_n", Title: "Notifications push", Status: pigtail.StatusBuilding}))
	if !reflect.DeepEqual(ids(got), []string{"sub_n"}) || !reflect.DeepEqual(got[0].Matched, []string{"notif"}) {
		t.Fatalf("got %+v, want sub_n matched on notif", got)
	}
}

// The measured miss of suggest (research): the exact subject is done, four
// open ones share only « sujets ».
func TestRankClosedSubjectFound(t *testing.T) {
	got := Rank("noms de sujets", corpus(
		Subject{ID: "sub_exact", Title: "Noms de sujets", Status: pigtail.StatusDone},
		Subject{ID: "sub_o1", Title: "Sujets parkés", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_o2", Title: "Lister les sujets", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_o3", Title: "Sujets du board", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_o4", Title: "Sujets orphelins", Status: pigtail.StatusBuilding},
	))
	if len(got) != 5 || got[0].ID != "sub_exact" {
		t.Fatalf("got %v, want sub_exact first of 5", ids(got))
	}
}

func TestRankOpenBeforeClosedWhenComparable(t *testing.T) {
	got := Rank("garden tone", corpus(
		Subject{ID: "sub_done", Title: "Garden tone card", Status: pigtail.StatusDone},
		Subject{ID: "sub_open", Title: "Garden tone card v2", Status: pigtail.StatusBuilding},
	))
	if ids(got)[0] != "sub_open" || ids(got)[1] != "sub_done" {
		t.Fatalf("comparable: got %v, want sub_open then sub_done", ids(got))
	}
	got = Rank("garden tone card", corpus(
		Subject{ID: "sub_done", Title: "Garden tone card", Status: pigtail.StatusDone},
		Subject{ID: "sub_open", Title: "Garden settings", Status: pigtail.StatusBuilding},
	))
	if ids(got)[0] != "sub_done" {
		t.Fatalf("clearly better closed: got %v, want sub_done first", ids(got))
	}
}

func TestRankThemeBonus(t *testing.T) {
	got := Rank("pigtail notifications", corpus(
		Subject{ID: "sub_arl", Title: "Notifications", Theme: "garden", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_pig", Title: "Notifications", Theme: "pigtail", Status: pigtail.StatusBuilding},
	))
	if len(got) < 2 || got[0].ID != "sub_pig" || !reflect.DeepEqual(got[0].Matched, []string{"pigtail", "notifications"}) {
		t.Fatalf("got %+v, want sub_pig first, matched [pigtail notifications]", got)
	}
	found := false
	for _, c := range got {
		found = found || c.ID == "sub_arl"
	}
	if !found {
		t.Fatalf("sub_arl dropped: %v", ids(got))
	}
}

func TestRankRecencyTieBreak(t *testing.T) {
	got := Rank("badge counters", corpus(
		Subject{ID: "sub_b", Title: "Badge counters", Status: pigtail.StatusBuilding, LastActivity: 100},
		Subject{ID: "sub_c", Title: "Badge counters", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_a", Title: "Badge counters", Status: pigtail.StatusBuilding, LastActivity: 300},
		Subject{ID: "sub_d", Title: "Badge counters", Status: pigtail.StatusBuilding},
	))
	if want := []string{"sub_a", "sub_b", "sub_c", "sub_d"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v, want %v", ids(got), want)
	}
}

func TestRankAcronymExact(t *testing.T) {
	got := Rank("ds", corpus(
		Subject{ID: "sub_ds", Title: "DS tokens", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_dsfr", Title: "DSFR migration", Status: pigtail.StatusBuilding},
	))
	if !reflect.DeepEqual(ids(got), []string{"sub_ds"}) {
		t.Fatalf("got %v, want only sub_ds", ids(got))
	}
}

func TestRankPluralBothWays(t *testing.T) {
	c := corpus(
		Subject{ID: "sub_pl", Title: "Noms de sujets", Status: pigtail.StatusBuilding},
		Subject{ID: "sub_sg", Title: "Revue du sujet", Status: pigtail.StatusBuilding},
	)
	for _, q := range []string{"sujet", "sujets"} {
		if got := ids(Rank(q, c)); len(got) != 2 {
			t.Errorf("%q: got %v, want both", q, got)
		}
	}
}

func TestRankNoMatch(t *testing.T) {
	c := corpus(Subject{ID: "sub_x", Title: "Notifications", Status: pigtail.StatusBuilding})
	for _, q := range []string{"zzz inconnu", "le de", ""} {
		if got := Rank(q, c); len(got) != 0 {
			t.Errorf("%q: got %v, want none", q, ids(got))
		}
	}
	if got := Rank("notifications", nil); len(got) != 0 {
		t.Errorf("empty corpus: got %v", ids(got))
	}
}
