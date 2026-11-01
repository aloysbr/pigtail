package scope

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// D1: no production Go file names a scope or a vault root — the table does.
// Allowed: this package's builtin table loader and the files below, each for
// a stated reason.
var hardcodedOK = map[string]string{
	"internal/scope/table.go": "error messages cite the builtin names (none today; keep the entry if one is added)",
	"cmd/pigtail/theme.go":    "help text gives a vault root of the owner's own tables as an example vault link",
	"cmd/pigtail/subject.go":  "help text gives a vault path of the owner's own tables as an example",
}

var hardcodedRE = regexp.MustCompile(`"(talk|perso|tenantcorp)"|'(talk|perso|tenantcorp)'|CORP/|HOMED/`)

func TestNoHardcodedScopeNames(t *testing.T) {
	root := filepath.Join("..", "..")
	walked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// The root itself is "../..": only directories below it are skipped.
		if d.IsDir() && path != root && (d.Name() == "macapp" || d.Name() == "clients" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "scopetest") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if _, ok := hardcodedOK[filepath.ToSlash(rel)]; ok {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		walked++
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if hardcodedRE.MatchString(line) {
				t.Errorf("%s:%d names a scope or vault root in code: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked == 0 { // positive control: a null result must have read something
		t.Fatal("walked no Go file")
	}
}
