package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// 0018 rebuilds messages to accept kind=notice, keeps every row and every
// index, and still refuses an unknown kind.
func TestMigration0018AcceptsNotice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0018.db")
	db := migrateBefore(t, path, "0018")
	if _, err := db.Exec(`INSERT INTO messages (id, thread_id, from_session, to_session, kind, body, created_at, expires_at, state)
		VALUES ('msg_old','thr_old','a','b','request','kept',1,2,'pending')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0018): %v", err)
	}
	defer s.Close()
	old, err := s.GetMessage("msg_old")
	if err != nil || old.Kind != pigtail.MessageKindRequest || old.Body != "kept" {
		t.Fatalf("old row = %+v %v", old, err)
	}
	now := time.Now()
	if err := s.CreateMessage(&pigtail.Message{ID: "msg_n", ThreadID: "thr_n", FromSession: "a", ToSession: "b",
		Kind: pigtail.MessageKindNotice, Body: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("notice refused after 0018: %v", err)
	}
	if err := s.CreateMessage(&pigtail.Message{ID: "msg_bad", ThreadID: "t", FromSession: "a", ToSession: "b",
		Kind: "shout", Body: "x", CreatedAt: now, ExpiresAt: now}); err == nil {
		t.Fatal("kind CHECK lost by the rebuild")
	}
	for _, idx := range []string{"idx_messages_to_state", "idx_messages_thread", "idx_messages_from"} {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after 0018 (%d, %v)", idx, n, err)
		}
	}
}
