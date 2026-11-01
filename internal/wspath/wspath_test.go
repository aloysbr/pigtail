package wspath

import (
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestWithin(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a/b/../c", "/a/b", false},
		{"/a", "/a/b", false},
	}
	for _, c := range cases {
		if got := Within(c.path, c.root); got != c.want {
			t.Errorf("Within(%q, %q) = %v, want %v", c.path, c.root, got, c.want)
		}
	}
}

func TestDeepestPicksLongestPath(t *testing.T) {
	ws := []pigtail.Workspace{{ID: "outer", Path: "/r"}, {ID: "inner", Path: "/r/wt/x"}, {ID: "other", Path: "/o"}}
	got, ok := Deepest("/r/wt/x/sub", ws)
	if !ok || got.ID != "inner" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestDeepestNoMatch(t *testing.T) {
	ws := []pigtail.Workspace{{ID: "a", Path: "/r"}, {ID: "nopath"}}
	if _, ok := Deepest("/elsewhere", ws); ok {
		t.Fatal("no workspace should match")
	}
	if _, ok := Deepest("", ws); ok {
		t.Fatal("empty cwd matches nothing")
	}
}
