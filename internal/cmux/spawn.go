package cmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// spawnCallTimeout bounds new-workspace: cmux starts a terminal before answering.
var spawnCallTimeout = 15 * time.Second

// WorkspaceSpec is the workspace `pigtail session spawn` asks cmux for.
type WorkspaceSpec struct {
	Name      string
	Cwd       string
	Command   string   // the new terminal's initial command (a shell line)
	Env       []string // KEY=VALUE, one --env each
	GroupRef  string   // workspace_group:N; "" = the caller is in no group
	CallerRef string   // workspace:N of the caller; with GroupRef it is born right under it (D1)
	// WindowID is the window (UUID) whose group GroupRef names, for a launch
	// without a caller (pigtaild's): passed as --window before --group, and
	// only with a GroupRef. "" = the caller's own window.
	WindowID string
}

// newWorkspaceArgs builds `cmux new-workspace`. --focus false always: a
// spawned session never takes the user's view (D5). In the caller's group it
// is born right under the caller (D1 of sub_x000000000000040, which overturns
// D8 of sub_x000000000000030: the subjects of a caller and its spawn are
// usually linked): --group-placement afterCurrent with the caller as
// --group-reference, a birth place, not a reorder. Without a
// caller ref it falls back to the bottom: afterCurrent alone would follow the
// window's selection, which is wherever the user looks. When the subject is
// filed in another folder, the daemon's first filing takes it to that
// folder's bottom (D11 (A)).
func newWorkspaceArgs(s WorkspaceSpec) []string {
	args := []string{"new-workspace", "--name", s.Name, "--cwd", s.Cwd}
	for _, e := range s.Env {
		args = append(args, "--env", e)
	}
	args = append(args, "--command", s.Command, "--focus", "false")
	if s.GroupRef != "" {
		if s.WindowID != "" {
			args = append(args, "--window", s.WindowID)
		}
		args = append(args, "--group", s.GroupRef)
		if s.CallerRef != "" {
			args = append(args, "--group-placement", "afterCurrent", "--group-reference", s.CallerRef)
		} else {
			args = append(args, "--group-placement", "end")
		}
	}
	return args
}

// NewWorkspaceArgs exposes the argv for --dry-run.
func NewWorkspaceArgs(s WorkspaceSpec) []string { return newWorkspaceArgs(s) }

var workspaceRefRe = regexp.MustCompile(`workspace:\d+`)

func parseNewWorkspaceRef(out string) (string, error) {
	ref := workspaceRefRe.FindString(out)
	if ref == "" {
		return "", fmt.Errorf("no workspace ref in cmux output: %q", strings.TrimSpace(out))
	}
	return ref, nil
}

// checkSpec validates what cmux could misread: WindowID must be a UUID.
func checkSpec(s WorkspaceSpec) error {
	if s.WindowID != "" {
		return checkUUID("window", s.WindowID)
	}
	return nil
}

// NewWorkspace creates the workspace and returns its `workspace:N` ref.
func NewWorkspace(s WorkspaceSpec) (string, error) {
	if err := checkSpec(s); err != nil {
		return "", err
	}
	out, err := runTimeout(spawnCallTimeout, newWorkspaceArgs(s)...)
	if err != nil {
		return "", err
	}
	return parseNewWorkspaceRef(string(out))
}

// parseCallerWorkspaceRef reads caller.workspace_ref from `cmux identify`.
// The focused workspace is never a fallback: it is wherever the user looks.
func parseCallerWorkspaceRef(out []byte) (string, error) {
	var id struct {
		Caller *struct {
			WorkspaceRef *string `json:"workspace_ref"`
		} `json:"caller"`
	}
	if err := json.Unmarshal(out, &id); err != nil {
		return "", fmt.Errorf("cmux identify: unexpected output: %q", strings.TrimSpace(string(out)))
	}
	if id.Caller == nil || id.Caller.WorkspaceRef == nil {
		return "", errors.New("cmux identify: cmux does not know the calling workspace")
	}
	ref := *id.Caller.WorkspaceRef
	if !regexp.MustCompile(`^workspace:\d+$`).MatchString(ref) {
		return "", fmt.Errorf("cmux identify: unexpected caller workspace ref %q", ref)
	}
	return ref, nil
}

// CallerWorkspaceRef returns the `workspace:N` of the pane pigtail runs in.
func CallerWorkspaceRef() (string, error) {
	out, err := run("identify")
	if err != nil {
		return "", err
	}
	return parseCallerWorkspaceRef(out)
}

func parseGroupOf(out []byte, wsRef string) (string, error) {
	var l struct {
		Groups []struct {
			Ref     string   `json:"ref"`
			Members []string `json:"member_workspace_refs"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		return "", fmt.Errorf("cmux workspace-group list: unexpected output: %q", OneLine(string(out), stderrMaxRunes))
	}
	for _, g := range l.Groups {
		for _, m := range g.Members {
			if m == wsRef {
				return g.Ref, nil
			}
		}
	}
	return "", nil
}

// GroupOf returns the group holding wsRef, "" when it is in none.
func GroupOf(wsRef string) (string, error) {
	out, err := run("workspace-group", "list", "--json")
	if err != nil {
		return "", err
	}
	return parseGroupOf(out, wsRef)
}

// SurfaceRefsIn lists the surfaces of one workspace (`cmux tree --workspace`).
func SurfaceRefsIn(wsRef string) ([]string, error) {
	out, err := run("tree", "--workspace", wsRef)
	if err != nil {
		return nil, err
	}
	return parseSurfaceRefs(string(out)), nil
}
