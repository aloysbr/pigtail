package space

import (
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var errNotFound = errors.New("not found")

// offTopicWorld is a test table of subjects and themes for OffTopicSpace.
type offTopicWorld struct {
	subjects map[string]pigtail.Subject
	themes   map[string]pigtail.Theme
	fail     error // returned by Subject when set
}

func (w offTopicWorld) lookup() Lookup {
	return Lookup{
		Subject: func(id string) (*pigtail.Subject, error) {
			if w.fail != nil {
				return nil, w.fail
			}
			s, ok := w.subjects[id]
			if !ok {
				return nil, errNotFound
			}
			return &s, nil
		},
		Theme: func(id string) (*pigtail.Theme, error) {
			t, ok := w.themes[id]
			if !ok {
				return nil, errNotFound
			}
			return &t, nil
		},
		NotFound: func(err error) bool { return errors.Is(err, errNotFound) },
	}
}

func TestOffTopicSpace(t *testing.T) {
	master, other := string(scope.Master()), string(scope.Default())
	if master == other {
		t.Fatal("the test needs a master scope distinct from the default one")
	}
	w := offTopicWorld{
		subjects: map[string]pigtail.Subject{
			"sub_abc123": {ID: "sub_abc123", ThemeID: "thm_sys", Scope: master},
			"sub_other1": {ID: "sub_other1", ThemeID: "thm_oth", Scope: other},
			"sub_nospc1": {ID: "sub_nospc1", ThemeID: "thm_none", Scope: master},
		},
		themes: map[string]pigtail.Theme{
			"thm_sys":  {ID: "thm_sys", Space: "Terrace", Scope: master},
			"thm_oth":  {ID: "thm_oth", Space: "Lawn", Scope: other},
			"thm_none": {ID: "thm_none", Scope: master},
		},
	}
	cases := []struct {
		name         string
		o            pigtail.WorkspaceOffTopic
		want         string
		wantInferred bool
	}{
		{"explicit wins over a cited subject", pigtail.WorkspaceOffTopic{Scope: master, Space: "Hedge", Reason: "revue de sub_abc123"}, "Hedge", false},
		{"cited subject gives its theme's space", pigtail.WorkspaceOffTopic{Scope: master, Reason: "suite de sub_abc123 côté design"}, "Terrace", true},
		{"subject the declaration's scope may not read", pigtail.WorkspaceOffTopic{Scope: other, Reason: "suite de sub_abc123"}, "", false},
		{"master cites a subject of the other scope", pigtail.WorkspaceOffTopic{Scope: master, Reason: "vu sub_other1"}, "Lawn", true},
		{"unknown subject", pigtail.WorkspaceOffTopic{Scope: master, Reason: "sub_zzz999 disparu"}, "", false},
		{"theme without space, then a resolvable subject (Q3)", pigtail.WorkspaceOffTopic{Scope: master, Reason: "sub_nospc1 puis sub_abc123"}, "Terrace", true},
		{"sub_ glued to a word is ignored", pigtail.WorkspaceOffTopic{Scope: master, Reason: "xsub_abc123"}, "", false},
		{"no reason, no space", pigtail.WorkspaceOffTopic{Scope: master}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, inferred, err := OffTopicSpace(c.o, w.lookup())
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want || inferred != c.wantInferred {
				t.Fatalf("OffTopicSpace = %q, inferred=%v; want %q, %v", got, inferred, c.want, c.wantInferred)
			}
		})
	}
}

func TestOffTopicSpaceLookupError(t *testing.T) {
	boom := errors.New("disk on fire")
	w := offTopicWorld{fail: boom}
	o := pigtail.WorkspaceOffTopic{Scope: string(scope.Master()), Reason: "revue de sub_abc123"}
	if _, _, err := OffTopicSpace(o, w.lookup()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup error (never read as « none »)", err)
	}
}

// R2 of sub_x000000000000027: « délie : <id> refusé » names no folder — a
// subject cited as refused is skipped, never inferred.
func TestOffTopicSpaceSkipsRefused(t *testing.T) {
	master, other := string(scope.Master()), string(scope.Default())
	w := offTopicWorld{
		subjects: map[string]pigtail.Subject{
			"sub_abc123": {ID: "sub_abc123", ThemeID: "thm_sys", Scope: master},
			"sub_other1": {ID: "sub_other1", ThemeID: "thm_oth", Scope: other},
		},
		themes: map[string]pigtail.Theme{
			"thm_sys": {ID: "thm_sys", Space: "Terrace", Scope: master},
			"thm_oth": {ID: "thm_oth", Space: "Lawn", Scope: other},
		},
	}
	cases := []struct {
		name string
		o    pigtail.WorkspaceOffTopic
		want string
	}{
		{"refused subject names no folder", pigtail.WorkspaceOffTopic{Scope: master, Reason: "délie : sub_abc123 refusé"}, ""},
		{"another cited subject still does", pigtail.WorkspaceOffTopic{Scope: master, Reason: "délie : sub_abc123 refusé — voir sub_other1"}, "Lawn"},
		{"cited without refusé is not a refusal", pigtail.WorkspaceOffTopic{Scope: master, Reason: "suite de sub_abc123 côté design"}, "Terrace"},
		{"explicit space wins", pigtail.WorkspaceOffTopic{Scope: master, Space: "Hedge", Reason: "délie : sub_abc123 refusé"}, "Hedge"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := OffTopicSpace(c.o, w.lookup())
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Fatalf("OffTopicSpace = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRefusedIDs(t *testing.T) {
	cases := map[string][]string{
		"délie : sub_abc123 refusé":                                {"sub_abc123"},
		"délie : sub_a1 refusé, puis sub_b2 refusé":                {"sub_a1", "sub_b2"},
		"Ada a dit délie sur sub_abc123":                         nil,
		"sub_abc123  refusé":                                       nil, // two spaces: not glued
		"xsub_abc123 refusé":                                       nil,
		"polish follow-up of merged #9225 — délie : sub_x9 refusé": {"sub_x9"},
	}
	for reason, want := range cases {
		got := RefusedIDs(reason)
		if len(got) != len(want) {
			t.Errorf("RefusedIDs(%q) = %v, want %v", reason, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("RefusedIDs(%q) = %v, want %v", reason, got, want)
			}
		}
	}
}

// sub_x000000000000033 (D3, D5): a cited subject files by its effective space.
func subjectSpaceWorld(subSpace string) offTopicWorld {
	return offTopicWorld{
		subjects: map[string]pigtail.Subject{"sub_x": {ID: "sub_x", ThemeID: "thm_sys", Scope: "perso", Space: subSpace}},
		themes:   map[string]pigtail.Theme{"thm_sys": {ID: "thm_sys", Space: "Terrace", Scope: "perso"}},
	}
}

func TestOffTopicSpaceUsesSubjectSpace(t *testing.T) {
	l := subjectSpaceWorld("Hedge").lookup()
	l.KnownSpace = func(name, sc string) bool { return name == "Hedge" && sc == "perso" }
	sp, inferred, err := OffTopicSpace(pigtail.WorkspaceOffTopic{Scope: "perso", Reason: "see sub_x"}, l)
	if err != nil || sp != "Hedge" || !inferred {
		t.Fatalf("got %q inferred=%v err=%v, want Hedge inferred", sp, inferred, err)
	}
}

func TestOffTopicInferStaleSubjectSpaceFallsBackToTheme(t *testing.T) {
	l := subjectSpaceWorld("Gone").lookup()
	l.KnownSpace = func(name, _ string) bool { return name != "Gone" }
	sp, inferred, err := OffTopicSpace(pigtail.WorkspaceOffTopic{Scope: "perso", Reason: "see sub_x"}, l)
	if err != nil || sp != "Terrace" || !inferred {
		t.Fatalf("got %q inferred=%v err=%v, want Terrace inferred", sp, inferred, err)
	}
}
