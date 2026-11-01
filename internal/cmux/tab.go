package cmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Tabs of the dead-input resume (sub_x000000000000024, D3): `pigtail cmux
// check --fix --surface` opens a new tab in the dead tab's workspace, without
// focus (G3), resumes the session there, then closes the dead tab. Surfaces
// and workspaces are named by UUID only.

// ErrLastSurface: cmux refuses to close the last surface of a workspace
// (`invalid_state: Cannot close the last surface`).
var ErrLastSurface = errors.New("cmux: cannot close the last surface")

// surfaceUUIDRe reads a surface UUID printed beside its ref (`surface:70
// 3A23D5DD-…`, `surface:70 (3a23…)`, `surface:70=…`) or as a JSON
// "surface_id"; never a bare UUID elsewhere in the line (a pane's or a
// workspace's).
var (
	surfaceUUIDRe  = regexp.MustCompile(`surface:\d+[\s(=/]*([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})`)
	surfaceIDKeyRe = regexp.MustCompile(`"surface_id"\s*:\s*"([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})"`)
)

// newTabArgs: `cmux --id-format both new-surface --workspace W
// --working-directory CWD --focus false` — always without focus.
func newTabArgs(workspace, cwd string) ([]string, error) {
	if err := checkUUID("workspace id", workspace); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cwd) == "" {
		return nil, invalid("working directory", cwd)
	}
	return append([]string{"--id-format", "both"}, newSurfaceArgs(workspace, cwd)...), nil
}

// NewTab opens a terminal tab in workspace, in cwd, without focus, and
// returns its surface UUID (upper case): read in cmux's output when it
// prints one beside the ref, else the `surface:N` ref resolved in the tree.
func NewTab(workspace, cwd string) (string, error) {
	args, err := newTabArgs(workspace, cwd)
	if err != nil {
		return "", err
	}
	out, err := runTimeout(spawnCallTimeout, args...)
	if err != nil {
		return "", err
	}
	return newTabUUID(string(out), workspace, func() ([]byte, error) { return runListing(treeAllArgs()...) })
}

// newTabUUID: the UUID of the surface new-surface just created. Never a ref:
// every later call (send, seen, close) compares UUIDs.
func newTabUUID(out, workspace string, tree func() ([]byte, error)) (string, error) {
	if m := surfaceUUIDRe.FindStringSubmatch(out); m != nil {
		return strings.ToUpper(m[1]), nil
	}
	if m := surfaceIDKeyRe.FindStringSubmatch(out); m != nil {
		return strings.ToUpper(m[1]), nil
	}
	ref, err := parseSurfaceRef(out)
	if err != nil {
		return "", fmt.Errorf("cmux new-surface: %w", err)
	}
	b, err := tree()
	if err != nil {
		return "", fmt.Errorf("cmux new-surface: resolve %s: %w", ref, err)
	}
	surfaces, err := parseTreeSurfaces(b)
	if err != nil {
		return "", err
	}
	for _, s := range surfaces {
		if s.Ref == ref && strings.EqualFold(s.WorkspaceID, workspace) {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("cmux new-surface: %s not found in workspace %s", ref, workspace)
}

// closeSurfaceArgs: both by UUID — a `surface:N` ref went unresolved once
// (task 0 C of the plan).
func closeSurfaceArgs(surface, workspace string) ([]string, error) {
	if err := checkUUID("surface id", surface); err != nil {
		return nil, err
	}
	if err := checkUUID("workspace id", workspace); err != nil {
		return nil, err
	}
	return []string{"close-surface", "--surface", surface, "--workspace", workspace}, nil
}

// CloseSurface closes surface (UUID) of workspace (UUID), never with
// --force: a surface still running a process stays open. The last surface of
// a workspace is ErrLastSurface.
func CloseSurface(surface, workspace string) error {
	args, err := closeSurfaceArgs(surface, workspace)
	if err != nil {
		return err
	}
	out, err := run(args...)
	return closeSurfaceErr(out, err)
}

func closeSurfaceErr(out []byte, err error) error {
	if err == nil && !strings.Contains(string(out), "Cannot close the last surface") {
		return nil
	}
	msg := string(out)
	if err != nil {
		msg += " " + err.Error()
	}
	if strings.Contains(msg, "Cannot close the last surface") {
		return fmt.Errorf("%w: %s", ErrLastSurface, OneLine(msg, stderrMaxRunes))
	}
	return err
}

// WorkspaceOfSurface returns the UUID (upper case) of the workspace holding
// surfaceID, read in `cmux tree --all --json --id-format both`. A surface the
// tree does not hold is an error, never "".
func WorkspaceOfSurface(surfaceID string) (string, error) {
	if err := checkUUID("surface id", surfaceID); err != nil {
		return "", err
	}
	out, err := runListing(treeAllArgs()...)
	if err != nil {
		return "", err
	}
	return workspaceOfSurface(out, surfaceID)
}

func workspaceOfSurface(tree []byte, surfaceID string) (string, error) {
	surfaces, err := parseTreeSurfaces(tree)
	if err != nil {
		return "", err
	}
	for _, s := range surfaces {
		if strings.EqualFold(s.ID, surfaceID) {
			return s.WorkspaceID, nil
		}
	}
	return "", fmt.Errorf("cmux tree: surface %s not found", surfaceID)
}

// treeSurface is one surface of the tree with its workspace.
type treeSurface struct {
	ID, Ref, WorkspaceID string
}

// parseTreeSurfaces decodes the tree into its surfaces, ids upper case. A
// missing windows array is an error, never an empty list.
func parseTreeSurfaces(b []byte) ([]treeSurface, error) {
	var raw struct {
		Windows *[]struct {
			Workspaces []struct {
				ID    string `json:"id"`
				Panes []struct {
					Surfaces []struct {
						ID  string `json:"id"`
						Ref string `json:"ref"`
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
	var out []treeSurface
	for _, win := range *raw.Windows {
		for _, w := range win.Workspaces {
			for _, p := range w.Panes {
				for _, s := range p.Surfaces {
					if s.ID == "" {
						continue
					}
					out = append(out, treeSurface{ID: strings.ToUpper(s.ID), Ref: s.Ref, WorkspaceID: strings.ToUpper(w.ID)})
				}
			}
		}
	}
	return out, nil
}
