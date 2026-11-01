package daemon

import (
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestStoreEnvSourcesListsLiveWorkspacesWithTheirPortVars(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	live := t.TempDir()
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_e", Name: "e", RootPath: filepath.Dir(live), DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}, "simulator": {39200, 39299}},
		PortEnvs:   map[string]string{"simulator": "PIGTAIL_SIM_PORT"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []*pigtail.Workspace{
		{ID: "ws_live", ProjectID: "prj_e", Path: live, Ports: map[string]int{"dev": 3000}, State: pigtail.StateBuilding, BackendKind: "git"},
		{ID: "ws_gone", ProjectID: "prj_e", Path: filepath.Join(live, "nope"), Ports: map[string]int{"dev": 3001}, State: pigtail.StateBuilding, BackendKind: "git"},
	} {
		if err := st.CreateWorkspace(ws); err != nil {
			t.Fatal(err)
		}
	}
	srcs, err := StoreEnvSources(st).EnvSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0].Dir != live {
		t.Fatalf("got %+v, want the live workspace only", srcs)
	}
	want := map[string]bool{"PORT": true, "PIGTAIL_SIM_PORT": true}
	if len(srcs[0].Vars) != 2 || !want[srcs[0].Vars[0]] || !want[srcs[0].Vars[1]] {
		t.Fatalf("vars %v, want PORT and PIGTAIL_SIM_PORT", srcs[0].Vars)
	}
}
