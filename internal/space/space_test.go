package space

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
)

// former is the space table pigtail shipped before the neutral builtin; the
// tests of folder order, icons and visibility read it over scopetest.Three's
// scopes.
var former = []byte(spacetest.FormerYAML)

func threeScopes(t *testing.T) *scope.Table { return scopetest.ThreeTable(t) }

// The neutral builtin declares no space and no icon: « Inbox », « Maintenance »
// and « Standby » stay the only folders, whatever the scope table.
func TestBuiltinSpacesEmpty(t *testing.T) {
	for name, scopes := range map[string]*scope.Table{"builtin": scope.Builtin(), "three": scopetest.ThreeTable(t)} {
		tbl, err := Builtin(scopes)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(tbl.Names()) != 0 || len(tbl.icons) != 0 {
			t.Fatalf("%s: builtin spaces %q icons %v, want none", name, tbl.Names(), tbl.icons)
		}
		if got := tbl.FolderOrder(); !reflect.DeepEqual(got, []string{NoSubject, MacMaintenance, Standby}) {
			t.Fatalf("%s: FolderOrder = %q", name, got)
		}
	}
	if strings.Contains(string(builtinYAML), "sub_") {
		t.Fatal("the builtin's comment cites a subject id")
	}
}

var d6 = []string{"Garden"} // the one name of D6 that survives the retirement of the pre-taxonomy names

// areas are the aires of sub_x000000000000010 (D4, D5; Pond: D4 t2), declared after Garden.
var areas = []string{"Vineyard", "Meadow", "Greenhouse", "Nursery", "Allotment", "Compost", "Pond"}

var allNames = append(append([]string{}, d6...), areas...)

func TestFormerOrderIsD6(t *testing.T) {
	tbl, err := Parse(former, "former", threeScopes(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Names(); !reflect.DeepEqual(got, allNames) {
		t.Fatalf("Names = %q, want %q", got, allNames)
	}
	want := append(append([]string{}, allNames...), NoSubject, MacMaintenance, Standby)
	if got := tbl.FolderOrder(); !reflect.DeepEqual(got, want) {
		t.Fatalf("FolderOrder = %q, want %q", got, want)
	}
	if got := tbl.FolderOrder(); got[len(got)-3] != "Inbox" || got[len(got)-2] != "Maintenance" || got[len(got)-1] != "Standby" {
		t.Fatalf("FolderOrder ends %q, want [Inbox Maintenance Standby]", got[len(got)-3:])
	}
	if got := tbl.Icon("Inbox"); got != "tray" {
		t.Fatalf("Icon(Inbox) = %q, want tray", got)
	}
}

func TestParseAcceptsFormerNoSubjectName(t *testing.T) {
	if _, err := Parse([]byte("spaces:\n- {name: Sans sujet, scope: perso}\n"), "test", threeScopes(t)); err != nil {
		t.Fatalf("D9: the former label is no longer reserved, Parse refused it: %v", err)
	}
}

// Lot 6 (sub_x000000000000010): « Other » is no longer reserved, « Inbox » is.
func TestParseAcceptsFormerOtherName(t *testing.T) {
	if _, err := Parse([]byte("spaces:\n- {name: Other, scope: perso}\n"), "test", threeScopes(t)); err != nil {
		t.Fatalf("lot 6: « Other » is no longer reserved, Parse refused it: %v", err)
	}
	if FormerNoSubject != "Other" {
		t.Fatalf("FormerNoSubject = %q", FormerNoSubject)
	}
}

// Lot 6, criterion 1: the focus name rules with literals (TestCheckFocusNameReserved
// uses the constant NoSubject, green on both sides).
func TestCheckFocusNameInboxReservedOtherFree(t *testing.T) {
	if err := CheckFocusName("Inbox"); err == nil {
		t.Error("lot 6: CheckFocusName(\"Inbox\") accepted, want refused (reserved)")
	}
	if err := CheckFocusName("Other"); err != nil {
		t.Errorf("lot 6: CheckFocusName(\"Other\") = %v, want accepted", err)
	}
}

func TestParseRefuses(t *testing.T) {
	for name, y := range map[string]string{
		"duplicate":      "spaces:\n- {name: A, scope: perso}\n- {name: A, scope: perso}\n",
		"reserved inbox": "spaces:\n- {name: Inbox, scope: perso}\n",
		"reserved stdby": "spaces:\n- {name: Standby, scope: perso}\n",
		"reserved prio":  "spaces:\n- {name: Prio, scope: perso}\n",
		"unknown scope":  "spaces:\n- {name: A, scope: nope}\n",
		"alias scope":    "spaces:\n- {name: A, scope: gx}\n",
		"empty name":     "spaces:\n- {name: '', scope: perso}\n",
		"flag-like":      "spaces:\n- {name: '-x', scope: perso}\n",
		"newline":        "spaces:\n- {name: \"a\\nb\", scope: perso}\n",
		"too long":       "spaces:\n- {name: " + strings.Repeat("x", 65) + ", scope: perso}\n",
		"icon scope":     "spaces:\n- {name: A, scope: perso}\nicons: {nope: tray}\n",
		"icon symbol":    "spaces:\n- {name: A, scope: perso}\nicons: {perso: 'house fill'}\n",
	} {
		if _, err := Parse([]byte(y), name, threeScopes(t)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVisibleToFollowsScope(t *testing.T) {
	tbl, _ := Parse(former, "former", threeScopes(t))
	if n := len(tbl.VisibleTo("perso", threeScopes(t))); n != 8 {
		t.Errorf("master sees %d spaces, want 8", n)
	}
	got := tbl.VisibleTo("globex", threeScopes(t))
	if len(got) != 4 {
		t.Fatalf("globex sees %d spaces, want 4: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Scope != "globex" {
			t.Errorf("globex sees %q of scope %q", s.Name, s.Scope)
		}
	}
}

func TestIconFollowsSpaceScope(t *testing.T) { // D11
	tbl, _ := Parse(former, "former", threeScopes(t))
	for folder, want := range map[string]string{
		"Hedge": "", "Lawn": "", "Terrace": "",
		"Garden": "house.fill", "Vineyard": "briefcase.fill", "Meadow": "briefcase.fill", "Greenhouse": "briefcase.fill", "Nursery": "briefcase.fill",
		"Allotment": "house.fill", "Compost": "house.fill", "Pond": "house.fill", NoSubject: "tray", Standby: "moon.zzz", MacMaintenance: "wrench.and.screwdriver", "Nope": "",
	} {
		if got := tbl.Icon(folder); got != want {
			t.Errorf("Icon(%q) = %q, want %q", folder, got, want)
		}
	}
}

func TestLoadOrBuiltin(t *testing.T) {
	dir := t.TempDir()
	tbl, err := LoadOrBuiltin(dir, threeScopes(t))
	if err != nil || len(tbl.Names()) != 0 || tbl.source != "builtin" {
		t.Fatalf("no file: %v, %v", tbl, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spaces.yaml"), []byte("spaces:\n- {name: Solo, scope: perso}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err = LoadOrBuiltin(dir, threeScopes(t))
	if err != nil || !reflect.DeepEqual(tbl.Names(), []string{"Solo"}) {
		t.Fatalf("file: %v, %v", tbl, err)
	}
}

// A focus folder name follows the space rules, the reserved folders included.
func TestCheckFocusNameReserved(t *testing.T) {
	for _, n := range []string{NoSubject, Standby, "", " ", "-x", "a\nb", strings.Repeat("x", 65)} {
		if err := CheckFocusName(n); err == nil {
			t.Errorf("CheckFocusName(%q) accepted", n)
		}
	}
	for _, n := range []string{"Workshop", "Sans sujet", strings.Repeat("é", 64)} {
		if err := CheckFocusName(n); err != nil {
			t.Errorf("CheckFocusName(%q) = %v", n, err)
		}
	}
}

// « Prio » is reserved like « Standby », for a space and for a focus, with the
// same message (D24 of sub_x000000000000014: the folder is gone, the name stays).
func TestCheckFocusNameRefusesPrio(t *testing.T) {
	err := CheckFocusName(Prio)
	if err == nil || err.Error() != `focus name "Prio" is reserved` {
		t.Fatalf("CheckFocusName(Prio) = %v", err)
	}
	if err := checkName(Prio); err == nil || err.Error() != `space name "Prio" is reserved` {
		t.Fatalf("checkName(Prio) = %v", err)
	}
}

// Focus D19: the focus icon says « temporary », distinct from every folder
// icon of the table — a constant guard; the icon a pass really sets is
// proven in cmuxsync (TestFocusFolderGetsFocusIcon).
func TestFocusIconDistinctFromScopeIcons(t *testing.T) {
	tbl, err := Parse(former, "former", threeScopes(t))
	if err != nil {
		t.Fatal(err)
	}
	if FocusIcon == "" || FocusIcon == NoSubjectIcon || FocusIcon == StandbyIcon {
		t.Fatalf("FocusIcon = %q, collides with a reserved folder icon", FocusIcon)
	}
	if len(tbl.icons) == 0 {
		t.Fatal("former table has no scope icon: the guard reads nothing")
	}
	for sc, sym := range tbl.icons {
		if sym == FocusIcon {
			t.Fatalf("FocusIcon %q = icon of scope %q", FocusIcon, sc)
		}
	}
}

// TestFolderOrderKeepsExisting: the names that existed on origin/main keep
// their relative order at the head of the folder order; the new aires come
// after (sub_x000000000000041: folders stay where they are).
func TestFolderOrderKeepsExisting(t *testing.T) {
	tbl, err := Parse(former, "former", threeScopes(t))
	if err != nil {
		t.Fatal(err)
	}
	order := tbl.FolderOrder()
	if got := order[:len(d6)]; !reflect.DeepEqual(got, d6) {
		t.Fatalf("FolderOrder head = %q, want Garden first %q", got, d6)
	}
	if got := order[len(d6) : len(d6)+len(areas)]; !reflect.DeepEqual(got, areas) {
		t.Fatalf("FolderOrder after Garden = %q, want the aires %q", got, areas)
	}
}

// The icons block survives the table change, read unchanged.
func TestFormerIconsUnchanged(t *testing.T) {
	tbl, err := Parse(former, "former", threeScopes(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Icon("Vineyard"); got != "briefcase.fill" {
		t.Errorf("Icon(Vineyard) = %q, want briefcase.fill", got)
	}
	if got := tbl.Icon("Allotment"); got != "house.fill" {
		t.Errorf("Icon(Allotment) = %q, want house.fill", got)
	}
}

// Maintenance window (G1): « Maintenance » is pigtail's own folder, reserved
// like « Standby » for a space and for a focus.
func TestMaintenanceNameReserved(t *testing.T) {
	if MacMaintenance != "Maintenance" || MacMaintenanceIcon != "wrench.and.screwdriver" {
		t.Fatalf("MacMaintenance = %q, icon %q", MacMaintenance, MacMaintenanceIcon)
	}
	if err := checkName(MacMaintenance); err == nil || err.Error() != `space name "Maintenance" is reserved` {
		t.Fatalf("checkName = %v", err)
	}
	if err := CheckFocusName(MacMaintenance); err == nil || err.Error() != `focus name "Maintenance" is reserved` {
		t.Fatalf("CheckFocusName = %v", err)
	}
	if _, err := Parse([]byte("spaces:\n- {name: Maintenance, scope: perso}\n"), "t", threeScopes(t)); err == nil {
		t.Fatal("a space named Maintenance was accepted")
	}
	if MacMaintenanceIcon == FocusIcon || MacMaintenanceIcon == NoSubjectIcon || MacMaintenanceIcon == StandbyIcon {
		t.Fatalf("icon %q collides with another folder icon", MacMaintenanceIcon)
	}
}

// G1: the folder sits just above « Standby », under « Inbox ».
func TestFolderOrderMaintenanceBeforeStandby(t *testing.T) {
	tbl, err := Parse(former, "former", threeScopes(t))
	if err != nil {
		t.Fatal(err)
	}
	o := tbl.FolderOrder()
	n := len(o)
	if !reflect.DeepEqual(o[n-3:], []string{NoSubject, MacMaintenance, Standby}) {
		t.Fatalf("FolderOrder ends %q", o[n-3:])
	}
	if got := tbl.Icon(MacMaintenance); got != "wrench.and.screwdriver" {
		t.Fatalf("Icon = %q", got)
	}
}
