package main

import "testing"

func TestBoardURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"127.0.0.1:7800": "http://127.0.0.1:7800/board?scope=globex",
		"localhost:9000": "http://localhost:9000/board?scope=globex",
		"":               "http://127.0.0.1:7800/board?scope=globex", // empty falls back to default
	}
	for in, want := range cases {
		if got := boardURL(in, "globex"); got != want {
			t.Errorf("boardURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := boardURL("", "all"); got != "http://127.0.0.1:7800/board?scope=all" {
		t.Errorf("boardURL(all) = %q", got)
	}
}
