package cmuxsync

import (
	"sort"
	"time"

	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// focusView is the open focus folders, read once per pass (D1).
type focusView struct {
	names     []string          // folder order: oldest focus first (focus D18)
	bySubject map[string]string // subject id → focus name
	byTheme   map[string]string // theme id → focus name
}

// newFocusView builds the view; a focus whose name collides with reserved
// (a space, NoSubject, Standby) is dropped and named in dropped: out of
// names, and its members are kept out of bySubject and byTheme (else a focus
// that later collides with a space files its sessions in that space's folder).
func newFocusView(fs []pigtail.Focus, reserved func(string) bool) (v focusView, dropped []string) {
	v = focusView{bySubject: map[string]string{}, byTheme: map[string]string{}}
	sorted := append([]pigtail.Focus(nil), fs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	for _, f := range sorted {
		if reserved(f.Name) {
			dropped = append(dropped, f.Name)
			continue
		}
		v.names = append(v.names, f.Name)
		for _, m := range f.Members {
			switch m.Kind {
			case pigtail.FocusMemberSubject:
				v.bySubject[m.ID] = f.Name
			case pigtail.FocusMemberTheme:
				v.byTheme[m.ID] = f.Name
			}
		}
	}
	return v, dropped
}

// reservedFolderNames is the names a focus may not take: the folders of the
// folder order (a space, « Inbox », « Standby ») and « Prio » (D24 of
// sub_x000000000000014). A focus named so is never filed.
func reservedFolderNames(folders []string) func(string) bool {
	reserved := map[string]bool{space.Prio: true}
	for _, n := range folders {
		reserved[n] = true
	}
	return func(n string) bool { return reserved[n] }
}

// passFolders is a pass's folder order: the open focus, oldest first (D4,
// focus D18), then the folder order (D6).
func (v focusView) passFolders(folders []string) []string {
	out := append([]string(nil), v.names...)
	return append(out, folders...)
}

// boundWS is what a bound workspace's folder depends on. Space is the
// effective one (the subject's own when it names a known space, else its
// theme's); ThemeSpace is the theme's, Scope the subject's; Status is the
// subject's, for its band (BandOf).
type boundWS struct{ Subject, Theme, Space, ThemeSpace, Scope, Status string }

// folderOf is the folder a bound workspace wants (D1, D3 of
// sub_x000000000000033): its subject's focus, else its theme's focus, else its
// subject's own space, else its theme's space (b.Space is already the first
// that applies of the last two). Standby is decided before, in folderTargets
// (#210).
func (v focusView) folderOf(b boundWS) string {
	if n, ok := v.bySubject[b.Subject]; ok && b.Subject != "" {
		return n
	}
	if n, ok := v.byTheme[b.Theme]; ok && b.Theme != "" {
		return n
	}
	return b.Space
}

// FocusSource is Deps.Focus over a store read (Store.ActiveFocus): read(clock())
// at every call, so a deadline is judged at read, never at construction. A
// seal error is logged once per distinct error and never returned: the
// filtered list stays served and the arrangement goes on (a focus past its
// deadline already counts no more). err, a read error, is returned.
func FocusSource(read func(time.Time) ([]pigtail.Focus, error, error), clock func() time.Time, logf func(string, ...any)) func() ([]pigtail.Focus, error) {
	last := ""
	return func() ([]pigtail.Focus, error) {
		fs, sealErr, err := read(clock())
		switch {
		case sealErr == nil:
			last = ""
		case sealErr.Error() != last:
			last = sealErr.Error()
			logf("cmuxsync: %v", sealErr)
		}
		return fs, err
	}
}
