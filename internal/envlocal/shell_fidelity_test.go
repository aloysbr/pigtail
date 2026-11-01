package envlocal

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file turns "faithful to the shell" from a claim in a doc comment into
// an assertion. Every case is written to a real .env.local, sourced by a real
// `sh` exactly the way pigtail sources it when a dev server starts
// (`set -a && . .env.local`, see internal/tui/commands.go), and the value the
// shell ends up with is compared to what Lookup reports.
//
// The table has TWO expectation columns on purpose. Most forms agree, and for
// those the test also asserts the agreement. A few forms do not, because this
// package is deliberately more permissive than the shell on a file the README
// asks users to hand-edit. Those cases must declare a `divergence` reason —
// and the test enforces the declaration both ways: an undeclared disagreement
// fails, and a declared divergence that no longer happens fails too. Silently
// dropping the awkward forms from the table would hide exactly what a reader
// of this package most needs to know.

// fidelityCase is one .env.local and the two answers it produces.
type fidelityCase struct {
	name    string
	content string
	lookup  string // variable to read; "" means PORT

	// shellValue/shellSet: what a POSIX shell sourcing the file assigns.
	shellValue string
	shellSet   bool

	// pigtailValue/pigtailFound: what Lookup reports.
	pigtailValue string
	pigtailFound bool

	// divergence explains why the two columns differ. Required when they do,
	// forbidden when they do not.
	divergence string
}

func TestFidelityToTheShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	cases := []fidelityCase{
		// --- forms where pigtail and the shell agree ---
		{
			name:       "plain assignment",
			content:    "PORT=3000\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "export prefix",
			content:    "export PORT=3000\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "indented export with a tab",
			content:    "  export\tPORT=3000\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "trailing comment",
			content:    "PORT=3000 # dev\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "double quotes",
			content:    "PORT=\"3000\"\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "single quotes",
			content:    "PORT='3000'\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "a hash with no leading space is a literal word",
			content:    "PORT=foo#bar\n",
			shellValue: "foo#bar", shellSet: true,
			pigtailValue: "foo#bar", pigtailFound: true,
		},
		{
			name:       "a hash inside quotes is data",
			content:    "PORT=\"a # b\"\n",
			shellValue: "a # b", shellSet: true,
			pigtailValue: "a # b", pigtailFound: true,
		},
		{
			name:       "empty value",
			content:    "PORT=\n",
			shellValue: "", shellSet: true,
			pigtailValue: "", pigtailFound: true,
		},
		{
			name:       "value that is only a comment",
			content:    "PORT= # nothing yet\n",
			shellValue: "", shellSet: true,
			pigtailValue: "", pigtailFound: true,
		},
		{
			// The load-bearing one for this lot: ReadPort used to take the
			// FIRST assignment. Here the shell itself says the last one wins.
			name:       "assigned twice, the last one wins",
			content:    "PORT=3000\nPORT=3042\n",
			shellValue: "3042", shellSet: true,
			pigtailValue: "3042", pigtailFound: true,
		},
		{
			name:       "commented assignment is not an assignment",
			content:    "# PORT=9999\nPORT=3000\n",
			shellValue: "3000", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
		},
		{
			name:       "absent variable",
			content:    "FOO=bar\n",
			shellValue: "", shellSet: false,
			pigtailValue: "", pigtailFound: false,
		},
		{
			// `export` is the keyword only when whitespace follows it: a
			// variable whose name merely starts with those letters is its own
			// variable, and the shell agrees.
			name:       "a variable named exported",
			content:    "exported=1\n",
			lookup:     "exported",
			shellValue: "1", shellSet: true,
			pigtailValue: "1", pigtailFound: true,
		},
		{
			name:       "a variable named export",
			content:    "export=1\n",
			lookup:     "export",
			shellValue: "1", shellSet: true,
			pigtailValue: "1", pigtailFound: true,
		},

		// --- forms where pigtail is deliberately more permissive ---
		{
			name:    "spaces around the equals",
			content: "PORT = 3000\n",
			// To the shell this is not an assignment at all: it runs a command
			// named PORT with the arguments "=" and "3000", which fails, and
			// PORT stays unset.
			shellValue: "", shellSet: false,
			pigtailValue: "3000", pigtailFound: true,
			divergence: "the README asks users to hand-edit this file, and a space " +
				"around the `=` is the most common slip. Reading it as the shell does " +
				"means silently ignoring a port or an owner the user believes they set — " +
				"the failure mode this package exists to remove. mergeEnvLocal normalises " +
				"the form back to a real assignment the next time it rewrites the line.",
		},
		{
			name:    "a spaced line overriding a valid earlier assignment",
			content: "export PORT=3000\nPORT = 3042\n",
			// The shell keeps 3000: the second line never assigns anything.
			shellValue: "3000", shellSet: true,
			pigtailValue: "3042", pigtailFound: true,
			divergence: "the cost of the tolerance above, stated plainly: where the shell " +
				"keeps the last VALID assignment, pigtail follows the user's last stated " +
				"intent. A dev server started from this file would bind 3000 while pigtail " +
				"reports 3042 — the one case where being permissive is not free.",
		},
		{
			name:    "unterminated quote",
			content: "PORT=\"3000\n",
			// The shell reports a syntax error (unexpected EOF looking for a
			// matching quote) and the assignment never completes, so PORT is
			// not set at all — the sourcing shell itself survives.
			shellValue: "", shellSet: false,
			pigtailValue: "\"3000", pigtailFound: true,
			divergence: "a malformed line must not leave the reader with nothing. Every " +
				"caller here needs an answer (an owner, a port) more than it needs a parse " +
				"error, so the quote is left in the value — where ReadPort then rejects it " +
				"loudly as a non-numeric port rather than falling back to 3000.",
		},
		{
			name:    "CRLF line ending",
			content: "PORT=3000\r\n",
			// The carriage return is an ordinary character to the shell, so it
			// lands inside the value.
			shellValue: "3000\r", shellSet: true,
			pigtailValue: "3000", pigtailFound: true,
			divergence: "a CR is an artefact of an editor or a Windows round-trip, never " +
				"part of an intended port or owner. Keeping it would make every value from " +
				"such a file unusable (Atoi fails, an owner never matches).",
		},
		{
			name:    "a word after the value",
			content: "PORT=3000 leftover\n",
			// To the shell this is a command `leftover` with PORT set only in
			// ITS environment — the sourcing shell keeps nothing.
			shellValue: "", shellSet: false,
			pigtailValue: "3000 leftover", pigtailFound: true,
			divergence: "this package reads assignments, it does not tokenise command " +
				"lines. A trailing word stays part of the value, which ReadPort then " +
				"rejects as a non-numeric port — visible, unlike the shell's silent drop.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := tc.lookup
			if name == "" {
				name = "PORT"
			}

			shellValue, shellSet, stderr := sourceWithShell(t, tc.content, name)
			if shellSet != tc.shellSet || shellValue != tc.shellValue {
				t.Fatalf("the shell assigned (%q, set=%v), the table says (%q, set=%v) — "+
					"our model of the shell is wrong, fix the table before trusting the rest\n"+
					"--- .env.local ---\n%s--- stderr ---\n%s",
					shellValue, shellSet, tc.shellValue, tc.shellSet, tc.content, stderr)
			}

			gotValue, gotFound := Lookup([]byte(tc.content), name)
			if gotFound != tc.pigtailFound || gotValue != tc.pigtailValue {
				t.Fatalf("Lookup = (%q, %v), want (%q, %v)\n--- .env.local ---\n%s",
					gotValue, gotFound, tc.pigtailValue, tc.pigtailFound, tc.content)
			}

			agree := shellSet == tc.pigtailFound && shellValue == tc.pigtailValue
			switch {
			case agree && tc.divergence != "":
				t.Fatalf("this case declares a divergence but the shell and Lookup now agree "+
					"on %q — drop the reason instead of keeping a stale one", gotValue)
			case !agree && tc.divergence == "":
				t.Fatalf("undeclared divergence: the shell assigns (%q, set=%v) but Lookup "+
					"reports (%q, %v). Either this is a bug, or it is a deliberate tolerance "+
					"that must be declared with its reason in this table\n--- .env.local ---\n%s",
					shellValue, shellSet, gotValue, gotFound, tc.content)
			}
		})
	}
}

// sourceWithShell writes content to a real .env.local, sources it the way
// pigtail does, and reports what the shell ended up with: the value and
// whether the variable is set at all.
//
// Which `sh` this is matters, and the two that matter disagree: CI runs Linux,
// where /bin/sh is dash, while a dev machine runs macOS, where it is bash in
// POSIX mode. On a syntax error inside the sourced file bash reports it and
// carries on, whereas dash exits the whole shell — so the probe gets no output
// at all. Both agree on what a caller can observe (the variable never becomes
// available), which is what this reports. PIGTAIL_TEST_SH overrides the
// binary, so the table can be run against either shell from one machine.
//
// The variable name is interpolated into the script rather than passed as an
// argument to keep the probe free of `eval`; every name in the table is a
// literal written here.
func sourceWithShell(t *testing.T, content, name string) (value string, set bool, stderr string) {
	t.Helper()
	sh := os.Getenv("PIGTAIL_TEST_SH")
	if sh == "" {
		sh = "sh"
	}
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// \037 (unit separator) cannot appear in any value the table uses, so it
	// safely delimits "is it set" from the value itself — which may be empty,
	// or may contain a trailing CR we must not trim away.
	script := fmt.Sprintf(`set -a; . "$1"; set +a; printf '%%s\037%%s' "${%s+set}" "${%s-}"`, name, name)
	cmd := exec.Command(sh, "-c", script, "sh", path) //nolint:gosec // fixed script, test-local path
	var stdout, errBuf bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &errBuf
	runErr := cmd.Run() // a failing command inside the sourced file is data, not an error here

	out := stdout.String()
	sep := strings.IndexByte(out, '\037')
	if sep < 0 {
		// No output: the shell died before its printf. That is a real answer
		// only when it died complaining about the file (dash aborts on a
		// syntax error where bash carries on) — the variable never became
		// available either way. Anything else is a broken probe.
		if runErr != nil && errBuf.Len() > 0 {
			return "", false, errBuf.String()
		}
		t.Fatalf("shell probe produced no readable output for %q (err %v, stdout %q, stderr %q)",
			content, runErr, out, errBuf.String())
	}
	return out[sep+1:], out[:sep] == "set", errBuf.String()
}
