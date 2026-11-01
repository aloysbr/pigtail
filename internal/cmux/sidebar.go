package cmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Sidebar primitives: the slice of the cmux CLI the sidebar loop
// (internal/daemon/cmuxsync) drives — titles, descriptions and status pills of
// a workspace, plus the listings it reconciles against.
//
// Every write targets a workspace by UUID, explicitly: without --workspace
// cmux falls back to the "current" workspace, which under launchd is whatever
// happens to be selected. Every argument is validated before cmux sees it —
// a value starting with "-" is parsed by cmux as a flag (measured on 0.64.24:
// `--title --help` prints the help and exits 0, renaming nothing), and a
// newline would split a sidebar line.
//
// The primitives of this page order nothing: the sidebar order lives in
// folders.go only, applied while the cmux window is not key (D2,
// sub_x000000000000041; TestOnlyFoldersReorderOrGroup).

// ErrInvalidArg is returned, before any exec, for an argument cmux would
// misread.
var ErrInvalidArg = errors.New("cmux: invalid argument")

// ErrWorkspaceNotFound marks a call whose target cmux no longer has: cmux
// answers `Error: not_found: …` (measured 2026-10-24 on workspace-action) or
// `Error: ERROR: Tab not found` (list-status) once the tab is closed. The
// answer proves cmux is running. It means "tab closed" only for the sidebar
// calls aimed by --workspace: runError also sets it on every other cmux
// command that answers so (the folder and read-screen calls), which do not
// test it.
var ErrWorkspaceNotFound = errors.New("cmux: workspace not found")

// StatusEntry is one sidebar status pill (`cmux set-status`), one line under
// the workspace title. Icon is an SF Symbol name, Color a #RRGGBB hex; both
// optional. Higher Priority sorts first.
type StatusEntry struct {
	Key      string
	Value    string
	Icon     string
	Color    string
	Priority int
}

// Window is one entry of `cmux list-windows --json`.
type Window struct {
	ID                  string `json:"id"`
	Index               int    `json:"index"`
	Key                 bool   `json:"key"`
	SelectedWorkspaceID string `json:"selected_workspace_id"`
	WorkspaceCount      int    `json:"workspace_count"`
}

// WorkspaceInfo is the subset of `cmux workspace list --json` the sidebar
// loop reads. Title is what the sidebar shows; CustomTitle is set (and
// HasCustomTitle true) once someone renamed the workspace — cmux does not
// say who (spec C3/C12).
type WorkspaceInfo struct {
	ID               string
	Ref              string
	Index            int
	Title            string
	CustomTitle      string
	HasCustomTitle   bool
	Description      string
	Selected         bool
	CurrentDirectory string
	// Pinned is the row's `pinned`: a pinned workspace is never
	// filed in a folder (D13, sub_x000000000000041).
	Pinned bool
}

// Sidebar is what the sidebar loop needs from cmux. SidebarCLI implements it
// over the CLI; tests substitute a fake.
type Sidebar interface {
	Rename(workspaceID, title string) error
	ClearName(workspaceID string) error
	SetDescription(workspaceID, description string) error
	ClearDescription(workspaceID string) error
	SetStatus(workspaceID string, e StatusEntry) error
	ClearStatus(workspaceID, key string) error
	ListStatus(workspaceID string) ([]StatusEntry, error)
	ListWindows() ([]Window, error)
	ListWorkspaces(windowID string) ([]WorkspaceInfo, error)
	ListSurfaces(windowID string) (map[string][]string, error)
}

// SidebarCLI drives the sidebar through the cmux CLI. Run executes one cmux
// call; nil means the package's bounded runners (3 s per write and per
// `list-status`, 10 s per listing; CLI resolved by cliPath, app-bundle
// fallback included).
type SidebarCLI struct {
	Run func(args ...string) ([]byte, error)
	// RunListing executes a read-only listing; nil = Run when set (tests
	// inject one runner for everything), else the package's listing runner
	// (10 s, D1·B).
	RunListing func(args ...string) ([]byte, error)
}

var _ Sidebar = SidebarCLI{}

func (c SidebarCLI) exec(args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(args...)
	}
	return run(args...)
}

func (c SidebarCLI) execListing(args ...string) ([]byte, error) {
	switch {
	case c.RunListing != nil:
		return c.RunListing(args...)
	case c.Run != nil:
		return c.Run(args...)
	}
	return runListing(args...)
}

var (
	uuidArgRe   = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	statusKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	iconRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9.]{0,63}$`)
	colorRe     = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// maxTextRunes bounds a title, description or status value: the sidebar
// shows one line, and anything longer is a bug upstream.
const maxTextRunes = 200

func invalid(what, v string) error {
	return fmt.Errorf("%w: %s %q", ErrInvalidArg, what, v)
}

func checkUUID(what, id string) error {
	if !uuidArgRe.MatchString(id) {
		return invalid(what, id)
	}
	return nil
}

// checkText accepts one non-empty line that cmux cannot take for a flag.
func checkText(what, s string) error {
	if strings.TrimSpace(s) == "" || strings.HasPrefix(s, "-") || len([]rune(s)) > maxTextRunes {
		return invalid(what, s)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return invalid(what, s)
		}
	}
	return nil
}

func checkKey(key string) error {
	if !statusKeyRe.MatchString(key) {
		return invalid("status key", key)
	}
	return nil
}

func workspaceActionArgs(workspaceID, action string, extra ...string) ([]string, error) {
	if err := checkUUID("workspace id", workspaceID); err != nil {
		return nil, err
	}
	return append([]string{"workspace-action", "--action", action, "--workspace", workspaceID}, extra...), nil
}

func renameArgs(workspaceID, title string) ([]string, error) {
	if err := checkText("title", title); err != nil {
		return nil, err
	}
	return workspaceActionArgs(workspaceID, "rename", "--title", title)
}

func setDescriptionArgs(workspaceID, description string) ([]string, error) {
	if err := checkText("description", description); err != nil {
		return nil, err
	}
	return workspaceActionArgs(workspaceID, "set-description", "--description", description)
}

func setSidebarStatusArgs(workspaceID string, e StatusEntry) ([]string, error) {
	if err := checkUUID("workspace id", workspaceID); err != nil {
		return nil, err
	}
	if err := checkKey(e.Key); err != nil {
		return nil, err
	}
	if err := checkText("status value", e.Value); err != nil {
		return nil, err
	}
	args := []string{"set-status", e.Key, e.Value}
	if e.Icon != "" {
		if !iconRe.MatchString(e.Icon) {
			return nil, invalid("icon", e.Icon)
		}
		args = append(args, "--icon", e.Icon)
	}
	if e.Color != "" {
		if !colorRe.MatchString(e.Color) {
			return nil, invalid("color", e.Color)
		}
		args = append(args, "--color", e.Color)
	}
	return append(args, "--priority", strconv.Itoa(e.Priority), "--workspace", workspaceID), nil
}

func clearStatusArgs(workspaceID, key string) ([]string, error) {
	if err := checkUUID("workspace id", workspaceID); err != nil {
		return nil, err
	}
	if err := checkKey(key); err != nil {
		return nil, err
	}
	return []string{"clear-status", key, "--workspace", workspaceID}, nil
}

func listStatusArgs(workspaceID string) ([]string, error) {
	if err := checkUUID("workspace id", workspaceID); err != nil {
		return nil, err
	}
	return []string{"list-status", "--workspace", workspaceID}, nil
}

func listWorkspacesArgs(windowID string) ([]string, error) {
	if err := checkUUID("window id", windowID); err != nil {
		return nil, err
	}
	return []string{"workspace", "list", "--window", windowID, "--json"}, nil
}

func treeArgs(windowID string) ([]string, error) {
	if err := checkUUID("window id", windowID); err != nil {
		return nil, err
	}
	return []string{"tree", "--window", windowID, "--json", "--id-format", "both"}, nil
}

func (c SidebarCLI) do(args []string, err error) error {
	if err != nil {
		return err
	}
	_, err = c.exec(args...)
	return err
}

// Rename gives the workspace a custom title (`workspace-action rename`). A
// custom title resists the terminal's OSC titles (spec C1).
func (c SidebarCLI) Rename(workspaceID, title string) error {
	return c.do(renameArgs(workspaceID, title))
}

// ClearName drops the custom title: the sidebar shows the live automatic
// title again (spec C1).
func (c SidebarCLI) ClearName(workspaceID string) error {
	return c.do(workspaceActionArgs(workspaceID, "clear-name"))
}

// SetDescription sets the line shown right under the title.
func (c SidebarCLI) SetDescription(workspaceID, description string) error {
	return c.do(setDescriptionArgs(workspaceID, description))
}

// ClearDescription removes that line.
func (c SidebarCLI) ClearDescription(workspaceID string) error {
	return c.do(workspaceActionArgs(workspaceID, "clear-description"))
}

// SetStatus upserts a status pill by key.
func (c SidebarCLI) SetStatus(workspaceID string, e StatusEntry) error {
	return c.do(setSidebarStatusArgs(workspaceID, e))
}

// ClearStatus removes a status pill. Clearing an absent key succeeds.
func (c SidebarCLI) ClearStatus(workspaceID, key string) error {
	return c.do(clearStatusArgs(workspaceID, key))
}

// ListStatus reads the pills currently on a workspace, every key included.
func (c SidebarCLI) ListStatus(workspaceID string) ([]StatusEntry, error) {
	args, err := listStatusArgs(workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := c.exec(args...)
	if err != nil {
		return nil, err
	}
	return ParseStatusList(string(out)), nil
}

// ListWindows reads `cmux list-windows --json`.
func (c SidebarCLI) ListWindows() ([]Window, error) {
	out, err := c.execListing("list-windows", "--json")
	if err != nil {
		return nil, err
	}
	return ParseWindows(out)
}

// ListWorkspaces reads `cmux workspace list --window <UUID> --json`. The
// window is mandatory: without it cmux answers for the calling (or key)
// window only (spec C4).
func (c SidebarCLI) ListWorkspaces(windowID string) ([]WorkspaceInfo, error) {
	args, err := listWorkspacesArgs(windowID)
	if err != nil {
		return nil, err
	}
	out, err := c.execListing(args...)
	if err != nil {
		return nil, err
	}
	return ParseWorkspaces(out)
}

// ListSurfaces reads `cmux tree --window <UUID> --json --id-format both`
// and returns the open surfaces of every workspace of that window, by
// workspace id. It is the only listing of surfaces cmux has that is both
// window-wide and read in one call (~40 ms on 0.64.24, 44 workspaces):
// `workspace list` carries none, `list-pane-surfaces` answers for one pane.
func (c SidebarCLI) ListSurfaces(windowID string) (map[string][]string, error) {
	args, err := treeArgs(windowID)
	if err != nil {
		return nil, err
	}
	out, err := c.execListing(args...)
	if err != nil {
		return nil, err
	}
	return ParseTree(out)
}

// ParseTree decodes `cmux tree --json`: workspace id → its surface ids,
// both upper case, surfaces sorted. A missing windows array, a workspace
// without an id or without a panes array is an error: the loop would
// otherwise trust an empty set and conclude every pane closed.
func ParseTree(b []byte) (map[string][]string, error) {
	var raw struct {
		Windows *[]struct {
			Workspaces []struct {
				ID    string `json:"id"`
				Panes *[]struct {
					SurfaceIDs []string `json:"surface_ids"`
					Surfaces   []struct {
						ID string `json:"id"`
					} `json:"surfaces"`
				} `json:"panes"`
			} `json:"workspaces"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("cmux tree: unexpected output: %w", err)
	}
	if raw.Windows == nil {
		return nil, errors.New("cmux tree: no windows array in output")
	}
	out := map[string][]string{}
	for _, win := range *raw.Windows {
		for _, w := range win.Workspaces {
			if w.ID == "" || w.Panes == nil {
				return nil, fmt.Errorf("cmux tree: workspace %q without id or panes", w.ID)
			}
			seen := map[string]bool{}
			for _, p := range *w.Panes {
				ids := append([]string(nil), p.SurfaceIDs...)
				for _, s := range p.Surfaces {
					ids = append(ids, s.ID)
				}
				for _, id := range ids {
					if id != "" {
						seen[strings.ToUpper(id)] = true
					}
				}
			}
			sfs := make([]string, 0, len(seen))
			for id := range seen {
				sfs = append(sfs, id)
			}
			sort.Strings(sfs)
			out[strings.ToUpper(w.ID)] = sfs
		}
	}
	return out, nil
}

// ParseWindows decodes `cmux list-windows --json`. Output that is not a JSON
// array is an error, never an empty list: the loop must not conclude that
// every workspace closed because cmux printed an error.
func ParseWindows(b []byte) ([]Window, error) {
	var ws []Window
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil, fmt.Errorf("cmux list-windows: unexpected output: %w", err)
	}
	for i := range ws {
		ws[i].ID = strings.ToUpper(ws[i].ID)
		ws[i].SelectedWorkspaceID = strings.ToUpper(ws[i].SelectedWorkspaceID)
	}
	return ws, nil
}

// ParseWorkspaces decodes `cmux workspace list --json`. IDs come out upper
// case, the form subject_cmux_bindings keys on. A missing workspaces array
// is an error (same reason as ParseWindows).
func ParseWorkspaces(b []byte) ([]WorkspaceInfo, error) {
	var raw struct {
		Workspaces *[]struct {
			ID               string  `json:"id"`
			Ref              string  `json:"ref"`
			Index            int     `json:"index"`
			Title            string  `json:"title"`
			CustomTitle      *string `json:"custom_title"`
			HasCustomTitle   bool    `json:"has_custom_title"`
			Description      *string `json:"description"`
			Selected         bool    `json:"selected"`
			CurrentDirectory string  `json:"current_directory"`
			Pinned           bool    `json:"pinned"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("cmux workspace list: unexpected output: %w", err)
	}
	if raw.Workspaces == nil {
		return nil, errors.New("cmux workspace list: no workspaces array in output")
	}
	out := make([]WorkspaceInfo, 0, len(*raw.Workspaces))
	for _, w := range *raw.Workspaces {
		out = append(out, WorkspaceInfo{
			ID:               strings.ToUpper(w.ID),
			Ref:              w.Ref,
			Index:            w.Index,
			Title:            w.Title,
			CustomTitle:      deref(w.CustomTitle),
			HasCustomTitle:   w.HasCustomTitle,
			Description:      deref(w.Description),
			Selected:         w.Selected,
			CurrentDirectory: w.CurrentDirectory,
			Pinned:           w.Pinned,
		})
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var (
	statusPriorityRe = regexp.MustCompile(` priority=(-?\d+)$`)
	statusColorRe    = regexp.MustCompile(` color=(#[0-9A-Fa-f]{6})$`)
	statusIconRe     = regexp.MustCompile(` icon=(\S+)$`)
)

// ParseStatusList decodes `cmux list-status`, which has no JSON form in
// 0.64.24: one `key=value[ icon=X][ color=#hex][ priority=N]` line per pill,
// or "No status entries". The trailing attributes are peeled from the right,
// in the order cmux prints them. A value that itself ends in " icon=…" is
// misread — harmless for the loop: its own values never do, and a misread
// foreign value is never written back.
func ParseStatusList(out string) []StatusEntry {
	var es []StatusEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		key, rest, ok := strings.Cut(line, "=")
		if !ok || !statusKeyRe.MatchString(key) {
			continue
		}
		e := StatusEntry{Key: key}
		if m := statusPriorityRe.FindStringSubmatch(rest); m != nil {
			e.Priority, _ = strconv.Atoi(m[1])
			rest = strings.TrimSuffix(rest, m[0])
		}
		if m := statusColorRe.FindStringSubmatch(rest); m != nil {
			e.Color = m[1]
			rest = strings.TrimSuffix(rest, m[0])
		}
		if m := statusIconRe.FindStringSubmatch(rest); m != nil {
			e.Icon = m[1]
			rest = strings.TrimSuffix(rest, m[0])
		}
		e.Value = rest
		es = append(es, e)
	}
	return es
}
