package pigtail

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func validOutput() SubjectOutput {
	return SubjectOutput{SubjectID: "sub_p", Kind: "capture", Path: "/a/c.png"}
}

func TestOutputIDStable(t *testing.T) {
	sum := sha256.Sum256([]byte("sub_p\n/a/c.png"))
	want := "out_" + hex.EncodeToString(sum[:])[:12]
	got := OutputID("sub_p", "/a/c.png")
	if got != want || len(got) != 16 {
		t.Fatalf("OutputID = %q, want %q (len 16)", got, want)
	}
}

func TestSubjectOutputNormalizePath(t *testing.T) {
	var first SubjectOutput
	for i, p := range []string{"/a/b/../c.png", "/a//c.png", "/a/c.png"} {
		o := validOutput()
		o.Path = p
		if err := o.Normalize(); err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if o.Key != "/a/c.png" {
			t.Fatalf("%q: key = %q", p, o.Key)
		}
		if i == 0 {
			first = o
		} else if o.ID != first.ID {
			t.Fatalf("%q: id %q != %q", p, o.ID, first.ID)
		}
	}
	o := validOutput()
	o.Path = "c.png"
	if err := o.Normalize(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("relative path: err = %v, want one naming path", err)
	}
}

func TestSubjectOutputNormalizeKeyPrefersPath(t *testing.T) {
	o := validOutput()
	o.URL = "https://x/y"
	if err := o.Normalize(); err != nil {
		t.Fatal(err)
	}
	if o.Key != o.Path {
		t.Fatalf("key = %q, want path %q", o.Key, o.Path)
	}
}

func TestSubjectOutputNormalizeAcceptsURLForms(t *testing.T) {
	for _, u := range []string{"https://x/y", "clip://s/1", "localhost:80"} {
		o := SubjectOutput{SubjectID: "sub_p", Kind: "link", URL: u}
		if err := o.Normalize(); err != nil {
			t.Fatalf("%q: %v", u, err)
		}
		if o.Key != u {
			t.Fatalf("%q: key = %q", u, o.Key)
		}
	}
}

func TestSubjectOutputNormalizeRejects(t *testing.T) {
	cases := []struct {
		name  string
		field string
		mut   func(*SubjectOutput)
	}{
		{"kind case", "kind", func(o *SubjectOutput) { o.Kind = "Capture" }},
		{"no path nor url", "path", func(o *SubjectOutput) { o.Path = "" }},
		{"bad scheme", "url", func(o *SubjectOutput) { o.Path = ""; o.URL = "1http://x" }},
		{"title 61 runes", "title", func(o *SubjectOutput) { o.Title = strings.Repeat("é", 61) }},
		{"title newline", "title", func(o *SubjectOutput) { o.Title = "a\nb" }},
		{"meta array", "meta", func(o *SubjectOutput) { o.Meta = []byte(`[1]`) }},
		{"meta scalar", "meta", func(o *SubjectOutput) { o.Meta = []byte(`"x"`) }},
		{"meta too big", "meta", func(o *SubjectOutput) {
			o.Meta = []byte(`{"k":"` + strings.Repeat("x", OutputMetaMax) + `"}`)
		}},
		{"step case", "step", func(o *SubjectOutput) { o.Step = "Verify" }},
		{"empty subject", "subject_id", func(o *SubjectOutput) { o.SubjectID = "" }},
		{"negative ts", "ts", func(o *SubjectOutput) { o.TS = -1 }},
		{"session space", "session_id", func(o *SubjectOutput) { o.SessionID = "a b" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := validOutput()
			c.mut(&o)
			err := o.Normalize()
			if err == nil || !strings.Contains(err.Error(), c.field) {
				t.Fatalf("err = %v, want one naming %q", err, c.field)
			}
		})
	}
}

func TestSubjectOutputNormalizeDefaults(t *testing.T) {
	o := validOutput()
	if err := o.Normalize(); err != nil {
		t.Fatal(err)
	}
	if o.Title != "c.png" || o.TS <= 0 || o.ID != OutputID("sub_p", "/a/c.png") {
		t.Fatalf("defaults not applied: %+v", o)
	}
}

func TestDefaultOutputTitle(t *testing.T) {
	if got := DefaultOutputTitle("/x/y/shot.png", ""); got != "shot.png" {
		t.Fatalf("path: %q", got)
	}
	if got := DefaultOutputTitle("", "https://github.com/a/b/pull/3"); got != "github.com/a/b/pull/3" {
		t.Fatalf("url: %q", got)
	}
	got := DefaultOutputTitle("/x/"+strings.Repeat("a", 80), "")
	if utf8.RuneCountInString(got) != 60 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long: %q (%d runes)", got, utf8.RuneCountInString(got))
	}
}

func TestStatExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.png")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		o    SubjectOutput
		want bool
	}{
		{SubjectOutput{Path: f}, true},
		{SubjectOutput{Path: filepath.Join(dir, "absent")}, false},
		{SubjectOutput{URL: "https://x/y"}, true},
	} {
		StatExists(&c.o)
		if c.o.Exists == nil || *c.o.Exists != c.want {
			t.Fatalf("%+v: exists = %v, want %v", c.o, c.o.Exists, c.want)
		}
	}
}
