package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// depsFixture seeds one work theme and one perso theme; mk adds a subject.
type depsFixture struct {
	t *testing.T
	s *Store
}

func newDepsFixture(t *testing.T) *depsFixture {
	t.Helper()
	s := newTestStore(t)
	now := time.Now()
	for _, th := range []pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: now},
		{ID: "thm_p", Name: "P", Kind: "project", VaultLink: "Perso/Projects/p", CreatedAt: now},
	} {
		th := th
		if err := s.CreateTheme(&th); err != nil {
			t.Fatal(err)
		}
	}
	return &depsFixture{t: t, s: s}
}

// mk creates a subject in the work theme (perso when other is true).
func (f *depsFixture) mk(id, status string, other bool, deps ...string) {
	f.t.Helper()
	theme := "thm_t"
	if other {
		theme = "thm_p"
	}
	now := time.Now()
	sub := pigtail.Subject{ID: id, ThemeID: theme, Title: id, Slug: id, Status: status, Deps: deps, CreatedAt: now, UpdatedAt: now}
	if err := f.s.CreateSubject(&sub); err != nil {
		f.t.Fatal(err)
	}
}

func (f *depsFixture) get(id string) *pigtail.Subject {
	f.t.Helper()
	sub, err := f.s.GetSubject(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return sub
}

func TestSubjectDepPath(t *testing.T) {
	f := newDepsFixture(t)
	b := pigtail.StatusBuilding
	f.mk("sub_a", b, false, "sub_b", "sub_c")
	f.mk("sub_b", b, false, "sub_d")
	f.mk("sub_c", b, false, "sub_d")
	f.mk("sub_d", b, false)
	f.mk("sub_x", b, false, "sub_y")
	f.mk("sub_y", b, false, "sub_z")
	f.mk("sub_z", b, false, "sub_gone")
	f.mk("sub_1", b, false)
	f.mk("sub_10", b, false, "sub_1")
	f.mk("sub_2", b, false, "sub_10")

	cases := []struct {
		name     string
		from, to string
		want     []string
	}{
		{"none", "sub_d", "sub_a", nil},
		{"direct", "sub_a", "sub_b", []string{"sub_a", "sub_b"}},
		{"length 3", "sub_x", "sub_z", []string{"sub_x", "sub_y", "sub_z"}},
		{"diamond", "sub_a", "sub_d", nil}, // replaced below: shortest, either branch
		{"same", "sub_a", "sub_a", nil},
		{"unknown end", "sub_x", "sub_gone", []string{"sub_x", "sub_y", "sub_z", "sub_gone"}},
		{"delimiters", "sub_2", "sub_1", []string{"sub_2", "sub_10", "sub_1"}},
		{"no false prefix", "sub_10", "sub_2", nil},
	}
	for _, c := range cases {
		got, err := f.s.SubjectDepPath(c.from, c.to)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.name == "diamond" {
			if len(got) != 3 || got[0] != "sub_a" || got[2] != "sub_d" {
				t.Errorf("diamond: got %v, want a 3-node path a..d", got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSubjectDepChains(t *testing.T) {
	f := newDepsFixture(t)
	b, d := pigtail.StatusBuilding, pigtail.StatusDone
	// root -> open1 -> open2 -> done1 -> hidden (stopped at the done one)
	f.mk("sub_hidden", b, false)
	f.mk("sub_done1", d, false, "sub_hidden")
	f.mk("sub_open2", b, false, "sub_done1")
	f.mk("sub_open1", b, false, "sub_open2")
	// diamond under root: root -> m1, m2 -> bottom ; plus an unknown id
	f.mk("sub_bottom", b, false)
	f.mk("sub_m2", b, false, "sub_bottom")
	f.mk("sub_m1", b, false, "sub_bottom")
	f.mk("sub_foreign", b, true, "sub_foreign_dep")
	f.mk("sub_foreign_dep", b, true)
	f.mk("sub_foreign_done", d, true)
	f.mk("sub_root", b, false, "sub_open1", "sub_m1", "sub_m2", "sub_unknown", "sub_foreign", "sub_foreign_done")
	// dependents of root: up1 -> root, up2 -> up1, upfar -> up2 (other scope dependent), updone done
	f.mk("sub_up1", b, false, "sub_root")
	f.mk("sub_up2", b, false, "sub_up1", "sub_m1")
	f.mk("sub_upforeign", b, true, "sub_root")
	f.mk("sub_updone", d, false, "sub_root")
	f.mk("sub_upabove", b, false, "sub_updone")

	byAll, blocks, blocksAll, err := f.s.SubjectDepChains(f.get("sub_root"))
	if err != nil {
		t.Fatal(err)
	}
	wantBy := []string{"sub_bottom", "sub_foreign", "sub_m1", "sub_m2", "sub_open1", "sub_open2", "sub_unknown"}
	if !reflect.DeepEqual(byAll, wantBy) {
		t.Errorf("blocked_by_all = %v, want %v", byAll, wantBy)
	}
	if want := []string{"sub_up1"}; !reflect.DeepEqual(blocks, want) {
		t.Errorf("blocks = %v, want %v", blocks, want)
	}
	if want := []string{"sub_up1", "sub_up2"}; !reflect.DeepEqual(blocksAll, want) {
		t.Errorf("blocks_all = %v, want %v", blocksAll, want)
	}

	// A done subject: blocks and blocks_all empty, blocked_by_all still computed.
	f.mk("sub_doneroot", d, false, "sub_open1")
	f.mk("sub_dep_of_done", b, false, "sub_doneroot")
	byAll, blocks, blocksAll, err = f.s.SubjectDepChains(f.get("sub_doneroot"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sub_open1", "sub_open2"}; !reflect.DeepEqual(byAll, want) {
		t.Errorf("done blocked_by_all = %v, want %v", byAll, want)
	}
	if len(blocks) != 0 || len(blocksAll) != 0 || blocks == nil || blocksAll == nil {
		t.Errorf("done subject: blocks=%#v blocks_all=%#v, want empty non-nil", blocks, blocksAll)
	}

	// Leaf: everything empty, never nil.
	byAll, blocks, blocksAll, err = f.s.SubjectDepChains(f.get("sub_hidden"))
	if err != nil {
		t.Fatal(err)
	}
	if byAll == nil || blocks == nil || blocksAll == nil || len(byAll)+len(blocks)+len(blocksAll) != 0 {
		// hidden is a dep of done1 only (a done dependent is not listed)
		t.Errorf("leaf: %#v %#v %#v", byAll, blocks, blocksAll)
	}
}

func TestSubjectDepChainsGrandfatheredCycle(t *testing.T) {
	f := newDepsFixture(t)
	b := pigtail.StatusBuilding
	f.mk("sub_a", b, false, "sub_b")
	f.mk("sub_b", b, false, "sub_c")
	f.mk("sub_c", b, false, "sub_a")
	byAll, blocks, blocksAll, err := f.s.SubjectDepChains(f.get("sub_a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sub_b", "sub_c"}; !reflect.DeepEqual(byAll, want) {
		t.Errorf("blocked_by_all = %v, want %v (root absent)", byAll, want)
	}
	if want := []string{"sub_c"}; !reflect.DeepEqual(blocks, want) {
		t.Errorf("blocks = %v, want %v", blocks, want)
	}
	if want := []string{"sub_b", "sub_c"}; !reflect.DeepEqual(blocksAll, want) {
		t.Errorf("blocks_all = %v, want %v", blocksAll, want)
	}
}

// A grandfathered self dependency never lists the subject itself, on either side.
func TestSubjectDepChainsGrandfatheredSelf(t *testing.T) {
	f := newDepsFixture(t)
	b := pigtail.StatusBuilding
	f.mk("sub_a", b, false, "sub_a", "sub_b")
	f.mk("sub_b", b, false)
	f.mk("sub_c", b, false, "sub_a")
	byAll, blocks, blocksAll, err := f.s.SubjectDepChains(f.get("sub_a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sub_b"}; !reflect.DeepEqual(byAll, want) {
		t.Errorf("blocked_by_all = %v, want %v", byAll, want)
	}
	if want := []string{"sub_c"}; !reflect.DeepEqual(blocks, want) {
		t.Errorf("blocks = %v, want %v", blocks, want)
	}
	if want := []string{"sub_c"}; !reflect.DeepEqual(blocksAll, want) {
		t.Errorf("blocks_all = %v, want %v", blocksAll, want)
	}
}

// A row whose deps is not valid JSON must not break the walks of other subjects.
func TestSubjectDepChainsInvalidDepsJSON(t *testing.T) {
	f := newDepsFixture(t)
	b := pigtail.StatusBuilding
	f.mk("sub_a", b, false)
	f.mk("sub_bad", b, false)
	f.mk("sub_b", b, false, "sub_a")
	if _, err := f.s.db.Exec(`UPDATE subjects SET deps = 'not json' WHERE id = 'sub_bad'`); err != nil {
		t.Fatal(err)
	}
	_, blocks, blocksAll, err := f.s.SubjectDepChains(f.get("sub_a"))
	if err != nil {
		t.Fatalf("chains with a malformed deps row: %v", err)
	}
	if want := []string{"sub_b"}; !reflect.DeepEqual(blocks, want) || !reflect.DeepEqual(blocksAll, want) {
		t.Errorf("blocks=%v blocks_all=%v, want %v", blocks, blocksAll, want)
	}
	if _, err := f.s.SubjectDepPath("sub_b", "sub_a"); err != nil {
		t.Fatalf("path with a malformed deps row: %v", err)
	}
}
