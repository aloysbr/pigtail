package launch

import (
	"fmt"
	"strings"
	"sync"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// fakeCmux is a simulated cmux: windows, their workspaces and groups, and a
// count of every call. No test ever runs the real cmux.
type fakeCmux struct {
	mu         sync.Mutex
	windows    []string
	workspaces map[string][]cmux.WorkspaceInfo
	groups     map[string][]cmux.Group

	windowsErr error
	listErr    error // ListWorkspaces
	createErr  error // NewWorkspace fails
	createNone bool  // NewWorkspace "succeeds" without a tab

	nWindows, nWorkspaces, nGroups, nCreate int
	specs                                   []cmux.WorkspaceSpec
	next                                    int
}

func newFakeCmux(windows ...string) *fakeCmux {
	f := &fakeCmux{windows: windows, workspaces: map[string][]cmux.WorkspaceInfo{}, groups: map[string][]cmux.Group{}, next: 10}
	return f
}

func (f *fakeCmux) Windows() ([]cmux.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nWindows++
	if f.windowsErr != nil {
		return nil, f.windowsErr
	}
	var out []cmux.Window
	for i, w := range f.windows {
		out = append(out, cmux.Window{ID: w, Index: i})
	}
	return out, nil
}

func (f *fakeCmux) Workspaces(w string) ([]cmux.WorkspaceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nWorkspaces++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]cmux.WorkspaceInfo(nil), f.workspaces[w]...), nil
}

func (f *fakeCmux) Groups(w string) ([]cmux.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nGroups++
	return f.groups[w], nil
}

func (f *fakeCmux) NewWorkspace(s cmux.WorkspaceSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nCreate++
	f.specs = append(f.specs, s)
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.createNone {
		return "workspace:99", nil
	}
	f.next++
	ref := fmt.Sprintf("workspace:%d", f.next)
	w := s.WindowID
	if w == "" {
		w = f.windows[0]
	}
	f.workspaces[w] = append(f.workspaces[w], cmux.WorkspaceInfo{
		ID: strings.ToLower(fmt.Sprintf("00000000-0000-4000-8000-%012d", f.next)), Ref: ref, Title: s.Name})
	return ref, nil
}

func (f *fakeCmux) creates() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nCreate
}

func (f *fakeCmux) lastSpec() cmux.WorkspaceSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[len(f.specs)-1]
}

func (f *fakeCmux) wire(d *Deps) {
	d.Windows, d.Workspaces, d.Groups, d.NewWorkspace = f.Windows, f.Workspaces, f.Groups, f.NewWorkspace
}

// createdID is the UUID (upper case) of the n-th tab NewWorkspace made, "" before.
func (f *fakeCmux) createdID(n int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, w := range f.windows {
		for _, x := range f.workspaces[w] {
			ids = append(ids, x.ID)
		}
	}
	if n >= len(ids) {
		return ""
	}
	return strings.ToUpper(ids[n])
}
