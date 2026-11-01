package cmuxsync

import (
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// mapReads is folderReads over maps: one bound tab "N" on the subject under test.
type mapReads struct {
	subjects map[string]*pigtail.Subject
	themes   map[string]*pigtail.Theme
	bindings []pigtail.SubjectCmuxBinding
	err      error
}

func (m *mapReads) ListPeers(bool) ([]pigtail.PeerSession, error) { return nil, nil }
func (m *mapReads) ListOffTopic() ([]pigtail.WorkspaceOffTopic, error) {
	return nil, nil
}
func (m *mapReads) ListAllCmuxBindings() ([]pigtail.SubjectCmuxBinding, error) {
	return m.bindings, nil
}

func (m *mapReads) GetSubject(id string) (*pigtail.Subject, error) {
	if m.err != nil {
		return nil, m.err
	}
	if s, ok := m.subjects[id]; ok {
		return s, nil
	}
	return nil, errors.New("not found")
}

func (m *mapReads) GetTheme(id string) (*pigtail.Theme, error) {
	if t, ok := m.themes[id]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}

// TestSubjectFolderMatchesFolderTargets: the folder SubjectFolder names is the
// one the loop files a tab bound to the subject in (readFocus → filing →
// folderTargets on the pass's folder order), for a subject in a focus, a
// theme in a focus, a space alone, a space that is no folder, a focus named
// like a folder (dropped, so the space wins), and the subject's own space
// (D3 of sub_x000000000000033: it wins over the theme's when it names a known
// space, never over a focus; an unknown one or a non-space folder falls back
// to the theme's).
func TestSubjectFolderMatchesFolderTargets(t *testing.T) {
	t.Parallel()
	folders := []string{"Hedge", "Terrace", space.NoSubject, space.Standby}
	t0 := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name, themeSpace, subjectSpace string
		focus                          []pigtail.Focus
		want                           string
	}{
		{"subject in a focus", "Hedge", "", []pigtail.Focus{{ID: "f1", Name: "Sprint", CreatedAt: t0, Members: []pigtail.FocusMember{{Kind: pigtail.FocusMemberSubject, ID: "sub_x"}}}}, "Sprint"},
		{"theme in a focus", "Hedge", "", []pigtail.Focus{{ID: "f2", Name: "Thème chaud", CreatedAt: t0, Members: []pigtail.FocusMember{{Kind: pigtail.FocusMemberTheme, ID: "thm_x"}}}}, "Thème chaud"},
		{"space only", "Terrace", "", nil, "Terrace"},
		{"space not a folder", "Ailleurs", "", nil, space.NoSubject},
		{"no space", "", "", nil, space.NoSubject},
		{"subject's own known space", "Hedge", "Terrace", nil, "Terrace"},
		{"subject's own space, theme with none", "", "Terrace", nil, "Terrace"},
		{"subject's own space unknown", "Hedge", "Ailleurs", nil, "Hedge"},
		{"subject's own space is Inbox", "Hedge", space.NoSubject, nil, "Hedge"},
		{"subject's own space is Standby", "Hedge", space.Standby, nil, "Hedge"},
		{"focus over the subject's own space", "Hedge", "Terrace", []pigtail.Focus{{ID: "f4", Name: "Sprint", CreatedAt: t0, Members: []pigtail.FocusMember{{Kind: pigtail.FocusMemberSubject, ID: "sub_x"}}}}, "Sprint"},
		{"focus named like a folder", "Hedge", "", []pigtail.Focus{{ID: "f3", Name: "Terrace", CreatedAt: t0, Members: []pigtail.FocusMember{{Kind: pigtail.FocusMemberSubject, ID: "sub_x"}}}}, "Hedge"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reads := &mapReads{
				subjects: map[string]*pigtail.Subject{"sub_x": {ID: "sub_x", ThemeID: "thm_x", Space: c.subjectSpace, Status: pigtail.StatusInbox}},
				themes:   map[string]*pigtail.Theme{"thm_x": {ID: "thm_x", Space: c.themeSpace}},
				bindings: []pigtail.SubjectCmuxBinding{{SubjectID: "sub_x", CmuxWorkspaceID: "N"}},
			}
			r := New(Config{}, Deps{
				FolderReads: reads,
				FolderOrder: func() []string { return append([]string(nil), folders...) },
				Focus:       func() ([]pigtail.Focus, error) { return c.focus, nil },
				Logf:        t.Logf,
			})
			r.allowed = map[string]bool{"N": true}
			fv, err := r.readFocus()
			if err != nil {
				t.Fatal(err)
			}
			f, err := r.filing(fv)
			if err != nil {
				t.Fatal(err)
			}
			loop := folderTargets([]string{"N"}, nil, nil, nil, nil, f, r.folderOrder(fv))["N"]

			got, err := SubjectFolder(reads, c.focus, folders, "sub_x")
			if err != nil {
				t.Fatal(err)
			}
			if got != loop || got != c.want {
				t.Fatalf("SubjectFolder = %q, loop files in %q, want %q", got, loop, c.want)
			}
		})
	}
}

// TestSubjectFolderReadError: a read error is returned, never read as « Inbox ».
func TestSubjectFolderReadError(t *testing.T) {
	t.Parallel()
	reads := &mapReads{err: errors.New("database is locked")}
	if got, err := SubjectFolder(reads, nil, []string{space.NoSubject}, "sub_x"); err == nil {
		t.Fatalf("SubjectFolder = %q, nil; want the read error", got)
	}
	noTheme := &mapReads{subjects: map[string]*pigtail.Subject{"sub_x": {ID: "sub_x", ThemeID: "thm_gone"}}}
	if got, err := SubjectFolder(noTheme, nil, []string{space.NoSubject}, "sub_x"); err == nil {
		t.Fatalf("SubjectFolder = %q, nil with an unreadable theme; want the error", got)
	}
}
