package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// 0019 rebuilds session_waits to accept source=held with its holder_pid, keeps
// every auto/declared row (until included), refuses an unknown source and a
// held row without holder_pid.
func TestMigration0019AcceptsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0019.db")
	db := migrateBefore(t, path, "0019")
	if _, err := db.Exec(`INSERT INTO session_waits (session_id, source, type, reason, since, until_ms) VALUES
		('s1','auto','limit','r',1000,5000), ('s1','declared','review','PR',2000,NULL)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0019): %v", err)
	}
	defer s.Close()
	ws, err := s.SessionWaits("s1")
	if err != nil || len(ws) != 2 {
		t.Fatalf("old rows = %+v %v", ws, err)
	}
	if ws[0].Source != "auto" || ws[0].Until == nil || ws[0].Until.UnixMilli() != 5000 || ws[0].Reason != "r" {
		t.Fatalf("auto row changed: %+v", ws[0])
	}
	if ws[1].Source != "declared" || ws[1].Type != "review" || ws[1].Until != nil {
		t.Fatalf("declared row changed: %+v", ws[1])
	}
	if err := s.SetSessionWait("s2", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 4242, Since: time.Now()}); err != nil {
		t.Fatalf("held refused after 0019: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO session_waits (session_id, source, type, since) VALUES ('s3','guess','x',1)`); err == nil {
		t.Fatal("source CHECK lost by the rebuild")
	}
	if _, err := s.db.Exec(`INSERT INTO session_waits (session_id, source, type, since) VALUES ('s3','held','mac',1)`); err == nil {
		t.Fatal("held without holder_pid accepted")
	}
}
