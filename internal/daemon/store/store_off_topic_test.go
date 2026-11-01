package store

import (
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestOffTopicCRUD(t *testing.T) {
	s := bindingStore(t)
	at := time.UnixMilli(10).UTC()
	o := &pigtail.WorkspaceOffTopic{CmuxWorkspaceID: wsA, Scope: "perso", SessionID: "s1", Reason: "question « rapide »", DeclaredAt: at}
	if err := s.CreateOffTopic(o); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOffTopic(o); !errors.Is(err, ErrOffTopicExists) {
		t.Fatalf("duplicate = %v", err)
	}
	got, err := s.GetOffTopic(wsA)
	if err != nil || got.Reason != "question « rapide »" || !got.DeclaredAt.Equal(at) || got.SessionID != "s1" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if err := s.SetOffTopicMissingTicks(wsA, 1); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListOffTopic()
	if err != nil || len(all) != 1 || all[0].MissingTicks != 1 {
		t.Fatalf("list = %+v %v", all, err)
	}
	if err := s.DeleteOffTopic(wsA); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOffTopic(wsA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete absent = %v", err)
	}
	if _, err := s.GetOffTopic(wsA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get absent = %v", err)
	}
}
