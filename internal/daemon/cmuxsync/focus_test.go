package cmuxsync

import (
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func focusAt(id, name string, created time.Time, members ...pigtail.FocusMember) pigtail.Focus {
	return pigtail.Focus{ID: id, Name: name, CreatedAt: created, Members: members}
}

func fTheme(id string) pigtail.FocusMember {
	return pigtail.FocusMember{Kind: pigtail.FocusMemberTheme, ID: id}
}

func fSubject(id string) pigtail.FocusMember {
	return pigtail.FocusMember{Kind: pigtail.FocusMemberSubject, ID: id}
}

func noReserved(string) bool { return false }

// Focus D18: several open focus, the oldest on top.
func TestFocusViewOrderOldestFirst(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	v, dropped := newFocusView([]pigtail.Focus{
		focusAt("fcs_c", "Late", t0.Add(2*time.Hour)),
		focusAt("fcs_a", "Early", t0),
		focusAt("fcs_b", "Middle", t0.Add(time.Hour)),
	}, noReserved)
	if want := []string{"Early", "Middle", "Late"}; !reflect.DeepEqual(v.names, want) || len(dropped) != 0 {
		t.Fatalf("names = %q dropped %q, want %q", v.names, dropped, want)
	}
}

// A focus named like a space (or a reserved folder) is dropped, members
// included: else its sessions would file into that space's folder.
func TestFocusViewDropsReservedNames(t *testing.T) {
	t.Parallel()
	reserved := func(n string) bool { return n == "Hedge" }
	v, dropped := newFocusView([]pigtail.Focus{
		focusAt("fcs_m", "Hedge", time.Now(), fTheme("thm_a"), fSubject("sub_a")),
		focusAt("fcs_o", "Workshop-T", time.Now(), fTheme("thm_b")),
	}, reserved)
	if !reflect.DeepEqual(dropped, []string{"Hedge"}) {
		t.Fatalf("dropped = %q, want [Hedge]", dropped)
	}
	if !reflect.DeepEqual(v.names, []string{"Workshop-T"}) {
		t.Fatalf("names = %q, want [Workshop-T]", v.names)
	}
	if _, ok := v.bySubject["sub_a"]; ok {
		t.Fatalf("bySubject keeps the dropped focus's member: %v", v.bySubject)
	}
	if _, ok := v.byTheme["thm_a"]; ok {
		t.Fatalf("byTheme keeps the dropped focus's member: %v", v.byTheme)
	}
	if got := v.folderOf(boundWS{Subject: "sub_a", Theme: "thm_a", Space: "Terrace"}); got != "Terrace" {
		t.Fatalf("folderOf = %q, want Terrace", got)
	}
	if got := v.folderOf(boundWS{Subject: "sub_x", Theme: "thm_b", Space: "Terrace"}); got != "Workshop-T" {
		t.Fatalf("positive control: folderOf = %q, want Workshop-T", got)
	}
}

// D4 t2: a focus named « Pond » is dropped like any folder of the real table
// (reserved fed by the builtin FolderOrder, as readFocus does, not a stub), and
// its members never file into the Pond folder.
func TestFocusViewDropsReservedNamesLife(t *testing.T) {
	t.Parallel()
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	reserved := map[string]bool{space.Prio: true}
	for _, n := range tbl.FolderOrder() {
		reserved[n] = true
	}
	v, dropped := newFocusView([]pigtail.Focus{
		focusAt("fcs_l", "Pond", time.Now(), fTheme("thm_l"), fSubject("sub_l")),
		focusAt("fcs_o", "Workshop-L", time.Now(), fTheme("thm_o")),
	}, func(n string) bool { return reserved[n] })
	if !reflect.DeepEqual(dropped, []string{"Pond"}) {
		t.Fatalf("dropped = %q, want [Pond]", dropped)
	}
	if !reflect.DeepEqual(v.names, []string{"Workshop-L"}) {
		t.Fatalf("names = %q, want [Workshop-L]", v.names)
	}
	if got := v.folderOf(boundWS{Subject: "sub_l", Theme: "thm_l", Space: "Garden"}); got != "Garden" {
		t.Fatalf("folderOf = %q, want Garden (the dropped focus files nothing)", got)
	}
}

// D1: subject focus > theme focus > space.
func TestFolderOfPriority(t *testing.T) {
	t.Parallel()
	v, _ := newFocusView([]pigtail.Focus{
		focusAt("fcs_a", "A", time.Now(), fSubject("sub_s")),
		focusAt("fcs_b", "B", time.Now().Add(time.Second), fTheme("thm_t")),
	}, noReserved)
	for _, c := range []struct {
		name string
		b    boundWS
		want string
	}{
		{"subject over theme", boundWS{Subject: "sub_s", Theme: "thm_t", Space: "Hedge"}, "A"},
		{"theme only", boundWS{Subject: "sub_o", Theme: "thm_t", Space: "Hedge"}, "B"},
		{"none", boundWS{Subject: "sub_o", Theme: "thm_o", Space: "Hedge"}, "Hedge"},
		{"no space, no focus", boundWS{Subject: "sub_o", Theme: "thm_o"}, ""},
	} {
		if got := v.folderOf(c.b); got != c.want {
			t.Errorf("%s: folderOf(%+v) = %q, want %q", c.name, c.b, got, c.want)
		}
	}
}
