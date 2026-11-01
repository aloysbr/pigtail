package space

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D9 (sub_x000000000000007) then lot 6 (sub_x000000000000010): the folder is
// « Inbox ». No production file, help text or doc names it by a former label. History keeps it: old plans,
// recorded cmux captures, applied migrations, CHANGELOG.md (release-please
// writes it from PR titles). Tests are free to name it (the
// convergence test builds a former « Sans sujet » folder).
// Former labels of the no-subject folder: « Sans sujet » (D9), « Other » (lot 6).
// « Other » is an English word: only its shapes as a folder name are refused.
var formerLabels = []string{"Sans sujet", "« Other", "`Other`", `"Other"`, "else Other", "folder Other", "in Other", "(Other", "|Other"}

var formerLabelOK = []string{"docs/superpowers/", "internal/cmux/testdata/", "internal/daemon/store/migrations/", "CHANGELOG.md"}

func TestNoStaleNoSubjectLabel(t *testing.T) {
	root := filepath.Join("..", "..")
	walked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "macapp" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" && ext != ".yaml" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, p := range formerLabelOK {
			if strings.HasPrefix(rel, p) {
				return nil
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		walked++
		for i, line := range strings.Split(string(b), "\n") {
			// a label split across two comment lines ends one with « Sans (space.go:129-130 on the base)
			if strings.Contains(line, "FormerNoSubject") {
				continue
			}
			for _, l := range formerLabels {
				if strings.Contains(line, l) || strings.HasSuffix(strings.TrimSpace(line), "« Sans") {
					t.Errorf("%s:%d names a former folder label (%s): %s", rel, i+1, l, strings.TrimSpace(line))
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked == 0 { // positive control: a null result must have read something
		t.Fatal("walked no file")
	}
}
