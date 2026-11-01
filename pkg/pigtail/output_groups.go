package pigtail

import (
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
)

// OutputGroup is one deliverable of a subject seen as its rounds (D2 of
// sub_x000000000000025): the `tN` folders of one parent. Answer of GET
// /subjects/{id}/outputs/groups.
type OutputGroup struct {
	Key      string       `json:"key"`  // the parent folder of the rounds; the file path for a deliverable outside a round
	Name     string       `json:"name"` // the first non-generic ancestor
	Kind     string       `json:"kind"` // mockup, report or html (of the first registration)
	FirstSeq int64        `json:"first_seq"`
	Tours    []OutputTour `json:"tours"` // N ascending: the last one is the latest version
}

// OutputTour is one round of a group.
type OutputTour struct {
	N         int    `json:"n"`     // 0 outside a round
	Label     string `json:"label"` // the round folder's name (t42-teams), the file name outside a round
	OutputID  string `json:"output_id"`
	Title     string `json:"title"`
	TS        int64  `json:"ts"` // epoch ms
	Path      string `json:"path"`
	ServedURL string `json:"served_url,omitempty"`
	Exists    bool   `json:"exists"`
}

var (
	// groupKinds are the kinds grouped into rounds; one place to widen.
	groupKinds = []string{"mockup", "report", "html"}
	tourDirRE  = regexp.MustCompile(`^t(\d+)([-_].*)?$`)
	// genericDirs are skipped when naming a group.
	genericDirs = []string{"mockup", "mockups", "report", "reports", "round", "rounds", "_outputs"}
)

// TourDir reports whether name is a round folder (`t3`, `t42-teams`,
// `t7_alt`) and its number. The outputs server opens a round folder whole and
// any other deliverable as its file alone, by the same rule.
func TourDir(name string) (int, bool) {
	m := tourDirRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false // beyond int: not a round anyone writes
	}
	return n, true
}

// groupName is the first ancestor of dir (itself included) whose name is not
// generic; the base of dir when every ancestor is.
func groupName(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		b := filepath.Base(d)
		if b == "/" || b == "." || b == "" {
			return filepath.Base(dir)
		}
		if !slices.Contains(genericDirs, b) {
			return b
		}
		if filepath.Dir(d) == d {
			return filepath.Base(dir)
		}
	}
}

// GroupOutputs groups the non-purged mockup, report and html outputs that have a
// path by round folder. It is pure: Exists and ServedURL are copied from
// each output as the caller computed them. Groups come in the order of their
// first registration (seq); tours by N ascending, two tours of the same N by
// ts (the most recent last, so the last tour is always the latest version).
// Never nil.
func GroupOutputs(outs []SubjectOutput) []OutputGroup {
	sorted := slices.Clone(outs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	groups := []OutputGroup{}
	at := map[string]int{}
	for _, o := range sorted {
		if o.Path == "" || o.PurgedAt != nil || !slices.Contains(groupKinds, o.Kind) {
			continue
		}
		p := filepath.Clean(o.Path)
		tourDir := filepath.Dir(p)
		tour := OutputTour{OutputID: o.ID, Title: o.Title, TS: o.TS, Path: o.Path, ServedURL: o.ServedURL, Exists: o.Exists != nil && *o.Exists}
		var key, name string
		if n, ok := TourDir(filepath.Base(tourDir)); ok {
			tour.N, tour.Label = n, filepath.Base(tourDir)
			key = filepath.Dir(tourDir)
			name = groupName(key)
		} else {
			tour.Label = filepath.Base(p)
			key = p
			name = groupName(tourDir)
		}
		i, ok := at[key]
		if !ok {
			i = len(groups)
			at[key] = i
			groups = append(groups, OutputGroup{Key: key, Name: name, Kind: o.Kind, FirstSeq: o.Seq})
		}
		groups[i].Tours = append(groups[i].Tours, tour)
	}
	for i := range groups {
		ts := groups[i].Tours
		sort.SliceStable(ts, func(a, b int) bool {
			if ts[a].N != ts[b].N {
				return ts[a].N < ts[b].N
			}
			return ts[a].TS < ts[b].TS
		})
	}
	return groups
}
