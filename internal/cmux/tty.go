package cmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// treeAllArgs is the read-only, fixed listing of every surface of every
// window with its tty: no caller value reaches cmux's argv.
func treeAllArgs() []string { return []string{"tree", "--all", "--json", "--id-format", "both"} }

// SurfaceTTYs reads `cmux tree --all --json --id-format both` and returns
// surface id → the tty cmux names for it ("" when it names none), ids upper
// case. Bounded by listingCallTimeout. cmux reuses and duplicates ttys
// across a relaunch (F9): a caller trusts a tty only when no other surface
// claims it (D22, cmuxcheck shellOfSurface).
func SurfaceTTYs() (map[string]string, error) {
	out, err := runListing(treeAllArgs()...)
	if err != nil {
		return nil, err
	}
	return ParseSurfaceTTYs(out)
}

// ParseSurfaceTTYs decodes that listing. A missing windows array, or a
// workspace without panes, is an error, never an empty map.
func ParseSurfaceTTYs(b []byte) (map[string]string, error) {
	var raw struct {
		Windows *[]struct {
			Workspaces []struct {
				ID    string `json:"id"`
				Panes *[]struct {
					Surfaces []struct {
						ID  string  `json:"id"`
						TTY *string `json:"tty"`
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
	out := map[string]string{}
	for _, win := range *raw.Windows {
		for _, w := range win.Workspaces {
			if w.Panes == nil {
				return nil, fmt.Errorf("cmux tree: workspace %q without panes", w.ID)
			}
			for _, p := range *w.Panes {
				for _, s := range p.Surfaces {
					if s.ID == "" {
						continue
					}
					tty := ""
					if s.TTY != nil {
						tty = strings.TrimPrefix(strings.TrimSpace(*s.TTY), "/dev/")
					}
					out[strings.ToUpper(s.ID)] = tty
				}
			}
		}
	}
	return out, nil
}

// SurfaceNeverRendered reads the same listing and reports whether cmux never
// rendered a surface: its tty is null and its render_health is
// "not_started" (D22). Such a pane has no shell to prove. An
// absent surface, or any other combination, is false: a doubt is rendered.
func SurfaceNeverRendered(surfaceID string) (bool, error) {
	out, err := runListing(treeAllArgs()...)
	if err != nil {
		return false, err
	}
	m, err := ParseNeverRendered(out)
	if err != nil {
		return false, err
	}
	return m[strings.ToUpper(surfaceID)], nil
}

// ParseNeverRendered decodes that listing: surface id (upper case) → never
// rendered. Only an explicit JSON null tty together with render_health
// "not_started" is true; a missing tty, an empty one or a missing health is
// false. Malformed as ParseSurfaceTTYs: an error, never an empty map.
func ParseNeverRendered(b []byte) (map[string]bool, error) {
	var raw struct {
		Windows *[]struct {
			Workspaces []struct {
				ID    string `json:"id"`
				Panes *[]struct {
					Surfaces []struct {
						ID           string          `json:"id"`
						TTY          json.RawMessage `json:"tty"` // nil absent, "null" null
						RenderHealth *string         `json:"render_health"`
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
	out := map[string]bool{}
	for _, win := range *raw.Windows {
		for _, w := range win.Workspaces {
			if w.Panes == nil {
				return nil, fmt.Errorf("cmux tree: workspace %q without panes", w.ID)
			}
			for _, p := range *w.Panes {
				for _, s := range p.Surfaces {
					if s.ID == "" {
						continue
					}
					out[strings.ToUpper(s.ID)] = string(s.TTY) == "null" &&
						s.RenderHealth != nil && *s.RenderHealth == "not_started"
				}
			}
		}
	}
	return out, nil
}

// SurfaceRefsByID reads the same listing as SurfaceTTYs and returns surface
// id (upper case) → its surface:N ref. Bounded by listingCallTimeout.
func SurfaceRefsByID() (map[string]string, error) {
	out, err := runListing(treeAllArgs()...)
	if err != nil {
		return nil, err
	}
	return ParseSurfaceRefsByID(out)
}

// ParseSurfaceRefsByID decodes that listing; malformed as ParseSurfaceTTYs:
// an error, never an empty map.
func ParseSurfaceRefsByID(b []byte) (map[string]string, error) {
	var raw struct {
		Windows *[]struct {
			Workspaces []struct {
				ID    string `json:"id"`
				Panes *[]struct {
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
	out := map[string]string{}
	for _, win := range *raw.Windows {
		for _, w := range win.Workspaces {
			if w.Panes == nil {
				return nil, fmt.Errorf("cmux tree: workspace %q without panes", w.ID)
			}
			for _, p := range *w.Panes {
				for _, s := range p.Surfaces {
					if s.ID != "" && s.Ref != "" {
						out[strings.ToUpper(s.ID)] = s.Ref
					}
				}
			}
		}
	}
	return out, nil
}
