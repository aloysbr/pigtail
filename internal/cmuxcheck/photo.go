// Package cmuxcheck compares the Claude sessions cmux had before a relaunch
// with the ones running now, and repairs what can be repaired safely
// (subject sub_x000000000000043, lot 1).
package cmuxcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// Session files cmux keeps in its Application Support dir (F1).
const (
	CurrentFile  = "session-com.cmuxterm.app.json"
	PreviousFile = "session-com.cmuxterm.app-previous.json"
)

// CmuxDir is ~/Library/Application Support/cmux.
func CmuxDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "cmux")
}

// Binding is a panel's claude resumeBinding: what cmux types to resume it.
type Binding struct {
	AutoResume   bool
	CheckpointID string
	Command      string
	Cwd          string
	Env          map[string]string
	LaunchArgs   []string
}

// ConfigDir is the account the binding resumes on.
func (b Binding) ConfigDir() string { return b.Env["CLAUDE_CONFIG_DIR"] }

// Panel is one cmux terminal panel. SurfaceID (the panel id, CMUX_SURFACE_ID)
// survives a relaunch. No tty on purpose: cmux's ttyName goes stale across a
// relaunch (three surfaces on one tty, 08/10) — nothing may target by it.
//
// Title is the pane's own title (its terminal's, set by the session running
// in it). WorkspaceTitle is its workspace's custom title — for a bound
// workspace, the short title of a subject cmuxsync posed (D23): it belongs to
// that subject's scope, which may not be the pane's account's.
type Panel struct {
	SurfaceID      string
	WorkspaceID    string
	Title          string
	WorkspaceTitle string
	AgentMode      string
	Binding        *Binding // nil: no claude binding
}

// quotedModeRe reads --permission-mode inside a binding command, where cmux
// quotes each argv word with escaped single quotes (F4).
var (
	quotedModeRe = regexp.MustCompile(`--permission-mode'\\'' '\\''([A-Za-z]+)'`)
	quotedSkipRe = regexp.MustCompile(`'\\''--dangerously-skip-permissions'\\''`)
)

// ExpectedMode is the permission mode the session had before (D3):
// agent.permissionMode, else launchCommand.arguments, else the command. ""
// when none names one.
func (p Panel) ExpectedMode() string {
	if p.AgentMode != "" {
		return p.AgentMode
	}
	if p.Binding == nil {
		return ""
	}
	if m := claudesession.PermissionMode(strings.Join(p.Binding.LaunchArgs, " ")); m != "" {
		return m
	}
	return p.ExpectedModeFromCommand()
}

// ExpectedModeFromCommand is the mode the binding command itself passes
// (quoted by cmux, F4); the skip flag wins, as in claudesession.PermissionMode.
func (p Panel) ExpectedModeFromCommand() string {
	if p.Binding == nil {
		return ""
	}
	if quotedSkipRe.MatchString(p.Binding.Command) {
		return claudesession.ModeBypass
	}
	if m := quotedModeRe.FindStringSubmatch(p.Binding.Command); m != nil {
		return m[1]
	}
	return claudesession.PermissionMode(p.Binding.Command)
}

// Photo is the list of panels at one moment.
type Photo struct {
	Source  string // "current", "previous", "peers", "previous+peers", "given"
	TakenAt time.Time
	Panels  []Panel
}

// Panel finds a panel by surface id.
func (p Photo) Panel(surfaceID string) (Panel, bool) {
	for _, pn := range p.Panels {
		if pn.SurfaceID == surfaceID {
			return pn, true
		}
	}
	return Panel{}, false
}

type sessionFile struct {
	Windows []struct {
		TabManager struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspaceId"`
				CustomTitle string `json:"customTitle"`
				Panels      []struct {
					ID       string    `json:"id"`
					Title    string    `json:"title"`
					Terminal *struct { // ttyName deliberately not read (T-c)
						Agent *struct {
							PermissionMode string `json:"permissionMode"`
						} `json:"agent"`
						ResumeBinding *struct {
							Kind          string            `json:"kind"`
							AutoResume    bool              `json:"autoResume"`
							CheckpointID  string            `json:"checkpointId"`
							Command       string            `json:"command"`
							Cwd           string            `json:"cwd"`
							Environment   map[string]string `json:"environment"`
							LaunchCommand struct {
								Arguments []string `json:"arguments"`
							} `json:"launchCommand"`
						} `json:"resumeBinding"`
					} `json:"terminal"`
				} `json:"panels"`
			} `json:"workspaces"`
		} `json:"tabManager"`
	} `json:"windows"`
}

// LoadPhoto reads a cmux session file. A missing or unparsable file is an
// error: "could not read" must never look like "no session".
func LoadPhoto(path string) (Photo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Photo{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Photo{}, err
	}
	var f sessionFile
	if err := json.Unmarshal(b, &f); err != nil {
		return Photo{}, fmt.Errorf("parse %s: %w", path, err)
	}
	ph := Photo{Source: "current", TakenAt: st.ModTime()}
	if filepath.Base(path) == PreviousFile {
		ph.Source = "previous"
	}
	for _, w := range f.Windows {
		for _, ws := range w.TabManager.Workspaces {
			for _, pn := range ws.Panels {
				p := Panel{SurfaceID: pn.ID, WorkspaceID: ws.WorkspaceID, Title: pn.Title, WorkspaceTitle: ws.CustomTitle}
				if t := pn.Terminal; t != nil {
					if t.Agent != nil {
						p.AgentMode = t.Agent.PermissionMode
					}
					if rb := t.ResumeBinding; rb != nil && rb.Kind == "claude" {
						p.Binding = &Binding{AutoResume: rb.AutoResume, CheckpointID: rb.CheckpointID,
							Command: rb.Command, Cwd: rb.Cwd, Env: rb.Environment, LaunchArgs: rb.LaunchCommand.Arguments}
					}
				}
				ph.Panels = append(ph.Panels, p)
			}
		}
	}
	return ph, nil
}
