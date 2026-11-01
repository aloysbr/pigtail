package cmux

// Folder primitives (sub_x000000000000041, D2, D3): the cmux workspace groups
// and the window order that the folder arrangement writes. D2 lifts « pigtail
// never reorders » (2026-09-30) for the arrangement only, applied while the
// cmux window is not key; this file is the only one that spells a reorder or a
// group write (TestOnlyFoldersReorderOrGroup). No colour is ever set (D10).
//
// Every call, listing as write, runs with no CMUX_* variable in its
// environment: cmux reads CMUX_SURFACE_ID before an explicit target, and the
// workspace-group sub-commands have no workspace UUID to aim with, so a daemon
// started from one of the user's panes would write into their real sidebar. Every
// write carries --window <uuid>, proved honoured by each sub-command on a
// throwaway window.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Group is one entry of `cmux --json --id-format both workspace-group list`.
type Group struct {
	ID              string   // UUID, upper case
	Ref             string   // workspace_group:N, as cmux lists it ("" when absent)
	Name            string   // byte for byte as cmux lists it (P11: NFC kept)
	AnchorID        string   // upper case
	AnchorGenerated bool     // anchor_workspace_is_generated
	Members         []string // display order, upper case; the anchor always present and first (F1 or F2)
	Collapsed       bool
	Icon            string // icon_symbol, "" when none (D11)
	Pinned          bool   // `pinned` / `is_pinned`; D13: never filed, sorted or dissolved
}

// Folders is what the folder arrangement needs from cmux (D2, D3).
type Folders interface {
	ListGroups(windowID string) ([]Group, error)
	CreateGroup(windowID, name string) (Group, error)
	AddToGroup(windowID, groupID, workspaceID string) error
	RemoveFromGroup(windowID, workspaceID string) error // P2 (b) only: `workspace-group remove --workspace <ws>`; the workspace stays open
	Ungroup(windowID, groupID string, removeGeneratedAnchor bool) error
	Collapse(windowID, groupID string) error
	Expand(windowID, groupID string) error            // D12 toggle only
	SetIcon(windowID, groupID, symbol string) error   // D11; never set-color (D10)
	RenameGroup(windowID, groupID, name string) error // lot 6: `workspace-group rename <g> --name <n>`
	Reorder(windowID string, order []string) error
	// PlaceWorkspace is `reorder-workspace --workspace <ws> --after|--before
	// <ref>`: ws alone moves, right after (before) ref, a workspace of its own
	// group; block order and memberships kept.
	PlaceWorkspace(windowID, workspaceID, ref string, before bool) error
	MoveGroup(windowID, groupID, ref string, before bool) error // P4b: `workspace-group move <g> --after|--before <ref>`
	SetPinned(workspaceID string, pinned bool) error            // D3: cmux's own pin, `workspace-action --action pin|unpin`
}

// ReorderMovesGroups is the P4 verdict of the live probe: P4a,
// `reorder-workspaces` moves whole group blocks and orders the members inside
// each block (and `workspace-group move --after|--before` places a block too).
const ReorderMovesGroups = true // live probe P4: P4a

// AddMovesFromGroup is the P2 verdict of the live probe: (a),
// `workspace-group add` of a workspace already in another group moves it out
// of that group and appends it to the target.
const AddMovesFromGroup = true // live probe P2: (a)

// AddPinnedLandsAtHead is the P24 verdict of the live probe (a, b): a
// pinned workspace added to a group (free, or from another group above or
// below) lands in the group's pinned block, above every unpinned member, and
// stays pinned — no unpin/pin round after a FolderAdd. A measured fact, not a
// switch: only the test fakes read it (the simulated cmux's add, and the
// tests that prove no repin), so that they follow the probe.
const AddPinnedLandsAtHead = true // live probe P24: a, b

// ReorderLiftsPinnedGroups is the P26 verdict of the live probe (a): a
// whole reorder-workspaces puts every pinned row first (the planner reads the
// workspace's own pin, not its group's), then glues each group back around its
// members, so a group holding a pinned member rises above the blocks before it.
// A measured fact, not a switch: only the test fakes read it (the simulated
// cmux's Reorder), so that they follow the probe.
const ReorderLiftsPinnedGroups = true // live probe P26: a

// FoldersCLI implements Folders over the cmux CLI. Run/RunListing nil = the
// package runners that strip every CMUX_* variable (envWithoutCmux, wake.go):
// cmux reads CMUX_SURFACE_ID before an explicit target, and workspace-group has
// no workspace UUID to aim with. Tests inject Run for argv only.
type FoldersCLI struct {
	Run        func(args ...string) ([]byte, error)
	RunListing func(args ...string) ([]byte, error)
}

func foldersRun(args ...string) ([]byte, error) {
	return execTimeout(relayCallTimeout, envWithoutCmux(), args...)
}

func foldersRunListing(args ...string) ([]byte, error) {
	return execTimeout(listingCallTimeout, envWithoutCmux(), args...)
}

func (c FoldersCLI) exec(args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(args...)
	}
	return foldersRun(args...)
}

func (c FoldersCLI) execListing(args ...string) ([]byte, error) {
	switch {
	case c.RunListing != nil:
		return c.RunListing(args...)
	case c.Run != nil:
		return c.Run(args...)
	}
	return foldersRunListing(args...)
}

// symbolRe is an SF Symbol name: dot-separated lower-case words (D11).
var symbolRe = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)*$`)

func checkUUIDs(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := checkUUID(pairs[i], pairs[i+1]); err != nil {
			return err
		}
	}
	return nil
}

// ListGroups lists the window's groups.
func (c FoldersCLI) ListGroups(windowID string) ([]Group, error) {
	if err := checkUUID("window id", windowID); err != nil {
		return nil, err
	}
	out, err := c.execListing("--json", "--id-format", "both", "workspace-group", "list", "--window", windowID)
	if err != nil {
		return nil, err
	}
	return ParseGroups(out)
}

// CreateGroup creates an anchor-only group (cmux generates its anchor) and
// returns it as cmux describes it (testdata/workspace-group-create.json).
func (c FoldersCLI) CreateGroup(windowID, name string) (Group, error) {
	if err := checkUUID("window id", windowID); err != nil {
		return Group{}, err
	}
	if err := checkText("group name", name); err != nil {
		return Group{}, err
	}
	out, err := c.exec("--json", "--id-format", "both", "workspace-group", "create", "--name", name, "--window", windowID)
	if err != nil {
		return Group{}, err
	}
	var created struct {
		Group *rawGroup `json:"group"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.Group == nil {
		return Group{}, fmt.Errorf("cmux workspace-group create: unexpected output: %q", OneLine(string(out), stderrMaxRunes))
	}
	return created.Group.group()
}

// AddToGroup appends a workspace to a group (P2 (a): moved out of its group).
func (c FoldersCLI) AddToGroup(windowID, groupID, workspaceID string) error {
	if err := checkUUIDs("window id", windowID, "group id", groupID, "workspace id", workspaceID); err != nil {
		return err
	}
	_, err := c.exec("workspace-group", "add", "--group", groupID, "--workspace", workspaceID, "--window", windowID)
	return err
}

// RemoveFromGroup takes a workspace out of its group; it stays open.
func (c FoldersCLI) RemoveFromGroup(windowID, workspaceID string) error {
	if err := checkUUIDs("window id", windowID, "workspace id", workspaceID); err != nil {
		return err
	}
	_, err := c.exec("workspace-group", "remove", "--workspace", workspaceID, "--window", windowID)
	return err
}

// Ungroup dissolves a group, members kept open (P7); removeGeneratedAnchor
// also closes its generated anchor, for an anchor-only group only (P6).
func (c FoldersCLI) Ungroup(windowID, groupID string, removeGeneratedAnchor bool) error {
	if err := checkUUIDs("window id", windowID, "group id", groupID); err != nil {
		return err
	}
	args := []string{"workspace-group", "ungroup", groupID}
	if removeGeneratedAnchor {
		args = append(args, "--remove-generated-anchor")
	}
	_, err := c.exec(append(args, "--window", windowID)...)
	return err
}

// Collapse folds a group (idempotent, P12).
func (c FoldersCLI) Collapse(windowID, groupID string) error {
	return c.groupVerb("collapse", windowID, groupID)
}

// Expand unfolds a group (idempotent, P12); the D12 toggle only.
func (c FoldersCLI) Expand(windowID, groupID string) error {
	return c.groupVerb("expand", windowID, groupID)
}

func (c FoldersCLI) groupVerb(verb, windowID, groupID string) error {
	if err := checkUUIDs("window id", windowID, "group id", groupID); err != nil {
		return err
	}
	_, err := c.exec("workspace-group", verb, groupID, "--window", windowID)
	return err
}

// RenameGroup renames a group in place (live probe P20a: only the name moves;
// P20b: its generated anchor's title follows — cmux's own effect; P20d: a
// taken name is accepted, so the caller checks first).
func (c FoldersCLI) RenameGroup(windowID, groupID, name string) error {
	if err := checkUUIDs("window id", windowID, "group id", groupID); err != nil {
		return err
	}
	if err := checkText("group name", name); err != nil {
		return err
	}
	_, err := c.exec("workspace-group", "rename", groupID, "--name", name, "--window", windowID)
	return err
}

// SetIcon sets a group's SF Symbol (D11). cmux accepts an unknown symbol and
// stores nothing, so the symbols come from the spaces table.
func (c FoldersCLI) SetIcon(windowID, groupID, symbol string) error {
	if err := checkUUIDs("window id", windowID, "group id", groupID); err != nil {
		return err
	}
	if !symbolRe.MatchString(symbol) {
		return invalid("icon symbol", symbol)
	}
	_, err := c.exec("workspace-group", "set-icon", groupID, "--symbol", symbol, "--window", windowID)
	return err
}

// Reorder sets the window's whole workspace order (P4a: blocks and members follow).
// cmux puts the pinned rows first and glues each group around them, so a group
// holding a pinned member rises: with the sort off, pigtail
// moves one tab at a time with PlaceWorkspace instead (D39).
func (c FoldersCLI) Reorder(windowID string, order []string) error {
	if err := checkUUID("window id", windowID); err != nil {
		return err
	}
	if len(order) == 0 {
		return invalid("order", "")
	}
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if err := checkUUID("workspace id", id); err != nil {
			return err
		}
		if seen[strings.ToUpper(id)] {
			return invalid("order (duplicate)", id)
		}
		seen[strings.ToUpper(id)] = true
	}
	_, err := c.exec("reorder-workspaces", "--window", windowID, "--order", strings.Join(order, ","))
	return err
}

// PlaceWorkspace moves one workspace right after (before) ref, by UUID only
// (never a workspace:N ref nor an index): cmux keeps the block order and the
// memberships, where a whole reorder-workspaces lifts a group
// holding a pinned member (P26 a). D39: the only move without the sort.
func (c FoldersCLI) PlaceWorkspace(windowID, workspaceID, ref string, before bool) error {
	if err := checkUUIDs("window id", windowID, "workspace id", workspaceID, "reference workspace id", ref); err != nil {
		return err
	}
	if strings.EqualFold(workspaceID, ref) {
		return invalid("reference workspace id (same workspace)", ref)
	}
	where := "--after"
	if before {
		where = "--before"
	}
	_, err := c.exec("reorder-workspace", "--workspace", workspaceID, where, ref, "--window", windowID)
	return err
}

// MoveGroup places a group block right after (before) another group, never by
// index: the sense of --to-index was not proved.
func (c FoldersCLI) MoveGroup(windowID, groupID, ref string, before bool) error {
	if err := checkUUIDs("window id", windowID, "group id", groupID, "reference group id", ref); err != nil {
		return err
	}
	if strings.EqualFold(groupID, ref) {
		return invalid("reference group id (same group)", ref)
	}
	where := "--after"
	if before {
		where = "--before"
	}
	_, err := c.exec("workspace-group", "move", groupID, where, ref, "--window", windowID)
	return err
}

type rawGroup struct {
	ID              string   `json:"id"`
	Ref             string   `json:"ref"`
	Name            string   `json:"name"`
	AnchorID        string   `json:"anchor_workspace_id"`
	AnchorGenerated bool     `json:"anchor_workspace_is_generated"`
	Members         []string `json:"member_workspace_ids"`
	Collapsed       bool     `json:"is_collapsed"`
	Icon            *string  `json:"icon_symbol"`
	Pinned          *bool    `json:"pinned"`
	IsPinned        *bool    `json:"is_pinned"`
}

// group normalizes one listed group: ids upper case, the anchor first in
// Members whether cmux lists it (F1) or leaves it out (F2), no duplicate.
// is_empty and member_count are never read.
func (r rawGroup) group() (Group, error) {
	if r.ID == "" || r.AnchorID == "" {
		return Group{}, fmt.Errorf("cmux workspace-group: group %q without id or anchor_workspace_id", r.Name)
	}
	g := Group{
		ID:              strings.ToUpper(r.ID),
		Ref:             r.Ref,
		Name:            r.Name,
		AnchorID:        strings.ToUpper(r.AnchorID),
		AnchorGenerated: r.AnchorGenerated,
		Collapsed:       r.Collapsed,
		Pinned:          (r.Pinned != nil && *r.Pinned) || (r.IsPinned != nil && *r.IsPinned),
	}
	if r.Icon != nil {
		g.Icon = *r.Icon
	}
	g.Members = []string{g.AnchorID}
	for _, m := range r.Members {
		if m = strings.ToUpper(m); m != "" && !slices.Contains(g.Members, m) {
			g.Members = append(g.Members, m)
		}
	}
	return g, nil
}

// ParseGroups reads `workspace-group list --json`. An output without a groups
// array, or a group without anchor, is an error, never an empty list.
func ParseGroups(out []byte) ([]Group, error) {
	var raw struct {
		Groups *[]rawGroup `json:"groups"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || raw.Groups == nil {
		return nil, fmt.Errorf("cmux workspace-group list: unexpected output: %q", OneLine(string(out), stderrMaxRunes))
	}
	gs := make([]Group, 0, len(*raw.Groups))
	for _, r := range *raw.Groups {
		g, err := r.group()
		if err != nil {
			return nil, err
		}
		gs = append(gs, g)
	}
	return gs, nil
}

// SetPinned writes cmux's own pin on a workspace (D3):
// `workspace-action --action pin|unpin --workspace <UUID>`. The only place
// pigtail writes a pin (TestOnlyFoldersPin); the target is always a UUID,
// never the caller's CMUX_* (the runners strip them).
func (c FoldersCLI) SetPinned(workspaceID string, pinned bool) error {
	action := "unpin"
	if pinned {
		action = "pin"
	}
	args, err := workspaceActionArgs(workspaceID, action)
	if err != nil {
		return err
	}
	_, err = c.exec(args...)
	return err
}
