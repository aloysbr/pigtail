package cmuxsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// SettingsPath is cmux's user settings file (JSONC).
func SettingsPath() string { return cmux.SettingsPath() }

// SettingsWarnings reads cmux's settings file and warns about the settings
// that would hide what the loop writes (spec §6): pills need
// sidebar.showCustomMetadata, the subject line needs
// sidebar.showWorkspaceDescription. A missing file gives no warning: cmux's
// defaults show both. Otherwise an unreadable or unparseable file gives none
// either, except in unsorted mode (unsorted: folders on and
// --cmux-folder-sort=false), where P1 of sub_x000000000000040 rests on
// workspaceGroups.newWorkspacePlacement being "afterCurrent" (cmux's
// default): a ⌘N session is then born right under the selected folder
// member. Any other value, or a file this reader cannot check, is said.
//
// startsSessions (D1, sub_x000000000000037): pigtaild starts the restored
// sessions itself (check mode fix), so cmux's own resume must be off; the
// drift either way is said.
//
// The reader handles what cmux's own template uses — whole-line // comments
// and trailing commas — not every JSONC form.
func SettingsWarnings(path string, unsorted, startsSessions bool) []string {
	const unchecked = "cmux settings.json not read by pigtail (%v): the ⌘N placement is not checked (workspaceGroups.newWorkspacePlacement)"
	clean, err := cmux.ReadSettingsJSON(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		if unsorted {
			return []string{fmt.Sprintf(unchecked, err)}
		}
		return nil
	}
	var s struct {
		Sidebar struct {
			ShowCustomMetadata       *bool `json:"showCustomMetadata"`
			ShowWorkspaceDescription *bool `json:"showWorkspaceDescription"`
		} `json:"sidebar"`
		WorkspaceGroups struct {
			NewWorkspacePlacement *string `json:"newWorkspacePlacement"`
		} `json:"workspaceGroups"`
	}
	if err := json.Unmarshal(clean, &s); err != nil {
		if unsorted {
			return []string{fmt.Sprintf(unchecked, err)}
		}
		return nil
	}
	var out []string
	if v := s.Sidebar.ShowCustomMetadata; v != nil && !*v {
		out = append(out, "cmux sidebar.showCustomMetadata is false: pigtail's pills are hidden")
	}
	if v := s.Sidebar.ShowWorkspaceDescription; v != nil && !*v {
		out = append(out, "cmux sidebar.showWorkspaceDescription is false: the subject line is hidden")
	}
	if v := s.WorkspaceGroups.NewWorkspacePlacement; unsorted && v != nil && *v != "afterCurrent" {
		out = append(out, fmt.Sprintf(`cmux workspaceGroups.newWorkspacePlacement is %q: a session made by ⌘N from a folder member is not born right under it (P1/D2 want "afterCurrent", cmux's default)`, *v))
	}
	if on, known := cachedAutoResume(path); known {
		switch {
		case on && startsSessions:
			out = append(out, "cmux "+cmux.AgentAutoResumeKey+" is on: cmux resumes the sessions itself, frozen at their start width (D1: off, pigtaild starts them)")
		case !on && !startsSessions:
			out = append(out, "cmux "+cmux.AgentAutoResumeKey+" is off but pigtaild does not start sessions (--cmux-check-after-relaunch is not fix): tabs stay at the shell after a reopen")
		}
	}
	return out
}

// autoResumeTTL bounds how long an auto-resume read is reused: SettingsWarnings
// runs on request paths and the read may fork `defaults` (up to 3 s).
const autoResumeTTL = 60 * time.Second

var (
	autoResumeNow = time.Now
	autoResumeMu  sync.Mutex
	autoResumeC   struct {
		path   string
		mtime  time.Time
		at     time.Time
		on, ok bool
		valid  bool
	}
)

// cachedAutoResume is cmux.AgentAutoResume cached per settings.json mtime
// and a short TTL (the UserDefaults fallback has no mtime to key on).
func cachedAutoResume(path string) (on, known bool) {
	var mt time.Time
	if fi, err := os.Stat(path); err == nil {
		mt = fi.ModTime()
	}
	now := autoResumeNow()
	autoResumeMu.Lock()
	defer autoResumeMu.Unlock()
	c := &autoResumeC
	if c.valid && c.path == path && c.mtime.Equal(mt) && now.Sub(c.at) < autoResumeTTL {
		return c.on, c.ok
	}
	on, known = cmux.AgentAutoResume(path)
	c.path, c.mtime, c.at, c.on, c.ok, c.valid = path, mt, now, on, known, true
	return on, known
}
