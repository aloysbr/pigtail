package cmux

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestParseWorkspaceKnown(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name    string
		in      []byte
		want    bool
		wantErr bool
	}{
		{"known", read("identify-known.json"), true, false},
		{"unknown", read("identify-unknown.json"), false, false},
		{"no caller key", []byte(`{"focused":{}}`), false, true},
		{"caller without workspace_ref", []byte(`{"caller":{"workspace_ref":null}}`), false, true},
		{"not json", []byte("not json"), false, true},
		{"empty", []byte(""), false, true},
	}
	for _, c := range cases {
		got, err := parseWorkspaceKnown(c.in)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got (%v, %v), want (%v, err=%v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}

func TestWorkspaceKnownArgs(t *testing.T) {
	unknown, err := os.ReadFile("testdata/identify-unknown.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	old := execTimeout
	execTimeout = func(_ time.Duration, _ []string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return unknown, nil
	}
	t.Cleanup(func() { execTimeout = old })

	const id = "C0FFEE00-0000-4000-8000-000000000001"
	known, err := WorkspaceKnown(id)
	if known || err != nil {
		t.Fatalf("WorkspaceKnown = (%v, %v), want (false, nil)", known, err)
	}
	want := [][]string{{"identify", "--workspace", id}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	calls = nil
	if _, err := WorkspaceKnown("--help"); err == nil {
		t.Fatal("a non-UUID id must be an error")
	}
	if len(calls) != 0 {
		t.Fatalf("a non-UUID id launched a process: %v", calls)
	}
}
