package envlocal

import "testing"

// TestParseAssignment covers the shapes a hand-edited .env.local actually
// produces. The reference is the shell, not a dotenv dialect: this exact file
// is sourced with `set -a && . .env.local` when a dev server starts (see
// internal/tui/commands.go), so what the shell would assign is what every
// reader here must report. A value read differently means pigtail's picture of
// a worktree disagrees with the environment its server runs under.
func TestParseAssignment(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantName string
		wantVal  string
		wantOK   bool
	}{
		{"plain assignment", "PORT=3000", "PORT", "3000", true},
		{"export prefix", "export PORT=3000", "PORT", "3000", true},
		{"export with tabs and extra spaces", "  export\t  PORT=3000", "PORT", "3000", true},
		{"spaces around the equals", "PORT = 3000", "PORT", "3000", true},
		{"trailing comment", "PORT=3000 # dev", "PORT", "3000", true},
		{"hash inside quotes is data", `NAME="a # b"`, "NAME", "a # b", true},
		{"unspaced hash is a literal word", "NAME=foo#bar", "NAME", "foo#bar", true},
		{"single quotes", "NAME='feat-x'", "NAME", "feat-x", true},
		{"quoted value, stray text after", `NAME="feat-x" leftover`, "NAME", "feat-x", true},
		{"unterminated quote is left alone", `NAME="feat-x`, "NAME", `"feat-x`, true},
		{"empty value", "NAME=", "NAME", "", true},
		{"value that is only a comment", "NAME= # nothing yet", "NAME", "", true},
		{"indented assignment", "\tNAME=v", "NAME", "v", true},
		// `export` only counts as the keyword when whitespace follows it, so a
		// variable that merely starts with those letters keeps its full name.
		{"variable named exported", "exported=v", "exported", "v", true},
		{"variable named export", "export=v", "export", "v", true},
		{"full-line comment", "# PORT=3000", "", "", false},
		{"indented comment", "   # PORT=3000", "", "", false},
		{"blank line", "", "", "", false},
		{"whitespace-only line", "  \t ", "", "", false},
		{"not an assignment", "PORT", "", "", false},
		{"no name", "=3000", "", "", false},
		{"export with nothing after it", "export ", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, val, ok := ParseAssignment(tc.line)
			if ok != tc.wantOK || name != tc.wantName || val != tc.wantVal {
				t.Fatalf("ParseAssignment(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.line, name, val, ok, tc.wantName, tc.wantVal, tc.wantOK)
			}
		})
	}
}

// TestValue exercises the right-hand side on its own, since ParseAssignment
// delegates to it and the quoting/comment rules are where a naive reader gets
// it wrong.
func TestValue(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"3000", "3000"},
		{"  3000", "3000"},
		{"3000   ", "3000"},
		{"3000 # dev", "3000"},
		{"3000\t# dev", "3000"},
		{"foo#bar", "foo#bar"},
		{`"a # b"`, "a # b"},
		{`'a # b'`, "a # b"},
		{`"a" # b`, "a"},
		{`"unterminated`, `"unterminated`},
		{"", ""},
		{"# only a comment", ""},
		{`""`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := Value(tc.raw); got != tc.want {
				t.Fatalf("Value(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestLookup pins the last-assignment-wins semantics: a shell sources the file
// top to bottom, so a variable assigned twice carries the value of the LAST
// assignment. A worktree that inherited a parent's .env.local before
// mergeEnvLocal rewrote it is the concrete case.
func TestLookup(t *testing.T) {
	cases := []struct {
		name    string
		content string
		lookup  string
		want    string
		wantOK  bool
	}{
		{
			name:    "single assignment",
			content: "PORT=3000\n",
			lookup:  "PORT", want: "3000", wantOK: true,
		},
		{
			name:    "last assignment wins",
			content: "PORT=3000\nPORT=3001\n",
			lookup:  "PORT", want: "3001", wantOK: true,
		},
		{
			name:    "last assignment wins across forms",
			content: "export PORT=3000\nPORT = 3001 # moved\n",
			lookup:  "PORT", want: "3001", wantOK: true,
		},
		{
			name:    "commented assignments are ignored",
			content: "# PORT=9999\nPORT=3000\n#PORT=8888\n",
			lookup:  "PORT", want: "3000", wantOK: true,
		},
		{
			name:    "absent variable",
			content: "PORT=3000\n",
			lookup:  "STORYBOOK_PORT", want: "", wantOK: false,
		},
		{
			// An empty value is an assignment, not an absence: the caller must
			// be able to tell `PORT=` (present but unusable — an error worth
			// reporting) from a file that never mentions PORT.
			name:    "empty value is still an assignment",
			content: "PORT=\n",
			lookup:  "PORT", want: "", wantOK: true,
		},
		{
			name:    "empty file",
			content: "",
			lookup:  "PORT", want: "", wantOK: false,
		},
		{
			name:    "no trailing newline",
			content: "PORT=3000",
			lookup:  "PORT", want: "3000", wantOK: true,
		},
		{
			name:    "CRLF line endings",
			content: "PORT=3000\r\nPIGTAIL_OWNER=feat-x\r\n",
			lookup:  "PORT", want: "3000", wantOK: true,
		},
		{
			name:    "blank and junk lines are skipped",
			content: "\n\nnot an assignment\n\nPORT=3000\n",
			lookup:  "PORT", want: "3000", wantOK: true,
		},
		{
			name:    "a prefix of the name is not a match",
			content: "PORT_EXTRA=1\nPORT=3000\n",
			lookup:  "PORT", want: "3000", wantOK: true,
		},
		{
			name:    "exact name only",
			content: "STORYBOOK_PORT=6006\n",
			lookup:  "PORT", want: "", wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Lookup([]byte(tc.content), tc.lookup)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("Lookup(%q, %q) = (%q, %v), want (%q, %v)",
					tc.content, tc.lookup, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestLookupN exposes what Lookup resolves silently: how many times the file
// assigns the variable. Files assigning a port twice exist on disk — the
// pre-fix mergeEnvLocal produced them by appending instead of rewriting — and
// a caller that wants to warn about one must not have to parse the file again
// to find out.
func TestLookupN(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		wantN   int
	}{
		{"absent", "FOO=bar\n", "", 0},
		{"assigned once", "PORT=3000\n", "3000", 1},
		{"assigned twice, last wins", "PORT=3000\nPORT=3042\n", "3042", 2},
		{"three times across forms", "PORT=1\nexport PORT=2\nPORT = 3\n", "3", 3},
		{"commented assignments do not count", "# PORT=9999\nPORT=3000\n", "3000", 1},
		{"an empty assignment still counts", "PORT=3000\nPORT=\n", "", 2},
		{
			// The exact shape the old mergeEnvLocal left behind: the parent's
			// exported line, plus an appended one for the child.
			name:    "the duplicate the old merge produced",
			content: "export PORT=3000\nPIGTAIL_OWNER=main\nPORT=38800\n",
			want:    "38800", wantN: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := LookupN([]byte(tc.content), "PORT")
			if got != tc.want || n != tc.wantN {
				t.Fatalf("LookupN = (%q, %d), want (%q, %d)", got, n, tc.want, tc.wantN)
			}
			// Lookup must stay exactly LookupN's "did we see one at all".
			val, ok := Lookup([]byte(tc.content), "PORT")
			if val != got || ok != (n > 0) {
				t.Fatalf("Lookup = (%q, %v) disagrees with LookupN = (%q, %d)", val, ok, got, n)
			}
		})
	}
}

// TestAssignPrefix covers the other half of reading this file: rewriting a
// line without reshaping it. The prefix is what precedes the variable name —
// indentation and an `export` keyword — so a caller replacing a value leaves a
// hand-maintained file looking the way its author left it.
func TestAssignPrefix(t *testing.T) {
	cases := []struct {
		name string
		line string
		vari string
		want string
	}{
		{"plain assignment has no prefix", "PORT=3000", "PORT", ""},
		{"export keyword is kept", "export PORT=3000", "PORT", "export "},
		{"indentation is kept", "\tPORT=3000", "PORT", "\t"},
		{"indented export with a tab", "  export\tPORT=3000", "PORT", "  export\t"},
		// Spaces around the `=` are the one thing NOT preserved: the input is
		// not an assignment to a shell, and echoing it back would keep it that
		// way. Rewriting normalises it into a line that actually assigns.
		{"spaces around the equals are dropped", "PORT = 3000", "PORT", ""},
		{"exported variable with spacing", "export PORT = 3000", "PORT", "export "},
		{"not an assignment", "PORT", "PORT", ""},
		{"a comment is not an assignment", "# export PORT=3000", "PORT", ""},
		{"another variable's line", "export PORT=3000", "FOO", ""},
		{"a variable named exported", "exported=1", "exported", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AssignPrefix(tc.line, tc.vari); got != tc.want {
				t.Fatalf("AssignPrefix(%q, %q) = %q, want %q", tc.line, tc.vari, got, tc.want)
			}
		})
	}
}

// TestOwnerIsTheWireName guards the one string three packages agree on. It is
// written into .env.local by `pigtail new` (internal/backend/git) and read back
// by OwnerID (internal/devserver); a rename on one side only would silently
// unclaim every worktree.
func TestOwnerIsTheWireName(t *testing.T) {
	if Owner != "PIGTAIL_OWNER" {
		t.Fatalf("Owner = %q, want PIGTAIL_OWNER", Owner)
	}
}
