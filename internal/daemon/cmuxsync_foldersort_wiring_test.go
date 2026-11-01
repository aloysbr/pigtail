package daemon

import (
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

// sub_x000000000000030 (D5): --cmux-folder-sort=false reaches the loop.
func TestServerWiresCmuxFolderSort(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		st, _ := store.Open(":memory:")
		dbDir := t.TempDir()
		s, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(dbDir, "pigtail.db"), CmuxFolders: true, CmuxFolderSortDisabled: disabled},
			st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.cmuxSyncConfig().FolderNoSort; got != disabled {
			t.Errorf("CmuxFolderSortDisabled=%v: FolderNoSort=%v", disabled, got)
		}
		st.Close()
	}
}
