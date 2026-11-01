package claudeusage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No production Go file names an account letter or a per-account threshold:
// the cell table (claude-cells.yaml, written by the launcher tooling from
// the launcher config) carries them. Same walk as scope.TestNoHardcodedScopeNames.
var (
	letterCmpRE    = regexp.MustCompile(`\b(Account|Label|account|label|acct|letter)\b[^"\n]*(==|!=)\s*"[a-z][a-z0-9]*"`)
	accountSwitch  = regexp.MustCompile(`^\s*switch\b.*\b(Account|Label|account|label|acct|letter)\b`)
	letterCaseRE   = regexp.MustCompile(`^\s*case\s+"[a-z][a-z0-9]*"`)
	thresholdLitRE = regexp.MustCompile(`\b(SelectPct|SwitchPct)\s*:\s*[0-9]`)
)

// scanAccountLetters returns one finding per offending line of src.
func scanAccountLetters(src string) []string {
	var out []string
	lines := strings.Split(src, "\n")
	inSwitch, switchIndent := false, ""
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "//") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if inSwitch && trim == "}" && indent == switchIndent {
			inSwitch = false
			continue
		}
		if accountSwitch.MatchString(line) {
			inSwitch, switchIndent = true, indent
		}
		switch {
		case letterCmpRE.MatchString(line):
			out = append(out, fmt.Sprintf("%d: account letter compared: %s", i+1, trim))
		case inSwitch && letterCaseRE.MatchString(line):
			out = append(out, fmt.Sprintf("%d: account letter case: %s", i+1, trim))
		case thresholdLitRE.MatchString(line):
			out = append(out, fmt.Sprintf("%d: threshold in code: %s", i+1, trim))
		}
	}
	return out
}

func TestNoAccountLetterInCode(t *testing.T) {
	// Each rule pins its witnesses on in-memory text, through the same scan.
	for _, bad := range []string{
		"\tif c.Account == \"m\" {\n\t}\n",
		"\tif ch.Label == \"m\" {\n\t}\n",
		"\tswitch c.Account {\n\tcase \"m\":\n\t}\n",
		"\tth := Thresholds{SelectPct: 70}\n",
	} {
		if len(scanAccountLetters(bad)) == 0 {
			t.Errorf("witness not caught: %q", bad)
		}
	}
	if got := scanAccountLetters("\tswitch key {\n\tcase \"o\":\n\t}\n"); len(got) != 0 {
		t.Errorf("TUI key switch flagged: %v", got)
	}

	root := filepath.Join("..", "..")
	walked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "macapp" || d.Name() == "clients" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		walked++
		rel, _ := filepath.Rel(root, path)
		for _, f := range scanAccountLetters(string(b)) {
			t.Errorf("%s:%s", rel, f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked == 0 {
		t.Fatal("walked no Go file")
	}
}
