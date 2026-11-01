package daemon

import (
	"errors"
	"testing"
)

func TestListenerSnapshotWith(t *testing.T) {
	nolog := func(string, ...any) {}
	got, err := listenerSnapshotWith(func() (map[int][]int, error) {
		return map[int][]int{3013: {42}, 6006: {7, 8}}, nil
	}, nolog)()
	if err != nil || !got[3013] || !got[6006] || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	var logged bool
	got, err = listenerSnapshotWith(func() (map[int][]int, error) {
		return nil, errors.New("exec: lsof not found")
	}, func(string, ...any) { logged = true })()
	if err != nil || len(got) != 0 || !logged {
		t.Fatalf("lsof unavailable: got %v, %v, logged=%v — want an empty set, no error, a log line", got, err, logged)
	}
}
