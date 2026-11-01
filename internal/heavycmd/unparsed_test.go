package heavycmd

import (
	"strings"
	"testing"
	"time"
)

// A line the lexer declines must say *why* and *what* was being run — and
// nothing else. The observe-mode log of 2026-09-23 had 26 unparsed lines out
// of 55 with an empty detail, which says nothing about either.
func TestRewriteReportsWhyItDeclined(t *testing.T) {
	cases := []struct {
		line    string
		reason  Reason
		command string
	}{
		{"cat <<EOF\ntsc\nEOF", ReasonHeredoc, "cat"},
		{"npx tsc --noEmit $(git rev-parse --show-toplevel)", ReasonCommandSubstitution, "npx"},
		{"echo `date`", ReasonBacktick, "echo"},
		{"diff <(tsc) b", ReasonProcessSubstitution, "diff"},
		{"eslint 'unterminated", ReasonUnterminatedQuote, "eslint"},
		{`bash -c "npx tsc"`, ReasonShellC, "bash"},
		{`sh -c 'eslint .'`, ReasonShellC, "sh"},
		{"eval npx tsc", ReasonEval, "eval"},
		{"xargs -n1 tsc < files.txt", ReasonXargs, "xargs"},
		{"find . -name '*.ts' -exec tsc {} +", ReasonFindExec, "find"},
		{"npx -c 'tsc && eslint'", ReasonNpxCall, "npx"},
		// The command word itself is unknowable: report the marker, not a guess.
		{"$TOOL --noEmit", ReasonVarCommand, unknownCommand},
		{"`which tsc` --noEmit", ReasonBacktick, unknownCommand},
	}
	for _, c := range cases {
		got := Rewrite(c.line, "", time.Minute)
		if got.Outcome != OutcomeUnparsed {
			t.Errorf("%q: outcome = %s, want unparsed", c.line, got.Outcome)
			continue
		}
		if got.Reason != c.reason || got.Command != c.command {
			t.Errorf("%q: reason=%q command=%q, want reason=%q command=%q",
				c.line, got.Reason, got.Command, c.reason, c.command)
		}
	}
}

// The non-disclosure rule: whatever is on the line, what comes back names a
// reason and at most a command word. A log that could hold a token passed as
// an argument would be a worse problem than the one it documents.
func TestRewriteNeverLeaksArguments(t *testing.T) {
	const secret = "sk-live-51H8xQe2SECRETVALUE"
	lines := []string{
		`curl -H "Authorization: Bearer ` + secret + `" $(cat url.txt)`,
		"gh auth login --with-token <<EOF\n" + secret + "\nEOF",
		"bash -c 'deploy --token=" + secret + "'",
		"eval \"export TOKEN=" + secret + "\"",
		"xargs -I{} publish --key " + secret + " < list",
		"PASSWORD=" + secret + " npx tsc `hostname`",
		"psql 'postgres://user:" + secret + "@host/db' <(echo x)",
		"find / -name '" + secret + "' -exec rm {} +",
	}
	for _, line := range lines {
		got := Rewrite(line, "", time.Minute)
		if got.Outcome != OutcomeUnparsed {
			t.Errorf("%q: outcome = %s, want unparsed", line, got.Outcome)
		}
		if strings.Contains(got.Reason.String()+"|"+got.Command, secret) {
			t.Errorf("the secret leaked into reason/command: %q %q", got.Reason, got.Command)
		}
		// Belt and braces: nothing but the reason and one word may come back.
		if strings.ContainsAny(got.Command, " \t=/'\"$`") {
			t.Errorf("%q: command word %q carries more than a name", line, got.Command)
		}
	}
}

// What the harness actually sends must not land in unparsed: these shapes are
// the bread and butter of an agent's Bash tool.
func TestCommonHarnessShapesAreParsed(t *testing.T) {
	lines := []string{
		`npx tsc --noEmit && npx eslint src`,
		`npm run lint | tail -20`,
		`FOO=1 NODE_OPTIONS=--max-old-space-size=4096 npx tsc`,
		`cd apps/web && yarn lint; cd -`,
		`npx tsc --noEmit 2>&1 | head -50`,
		`git status && npm test -- --ci`,
		`npx jest --ci || echo failed`,
		`ls -la && echo 'done: $(not a substitution)'`,
		`grep -rn '` + "`" + `backtick in single quotes` + "`" + `' src`,
		`echo "quoted && ampersand" && npx tsc`,
		`npx eslint --fix 'src/**/*.{ts,tsx}'`,
		`node --max-old-space-size=8192 node_modules/.bin/jest --runInBand`,
		`for f in a b; do echo $f; done`,
		`npx tsc <<< "ignored herestring"`,
	}
	for _, line := range lines {
		if got := Rewrite(line, "", time.Minute); got.Outcome == OutcomeUnparsed {
			t.Errorf("%q wrongly declined: reason=%q", line, got.Reason)
		}
	}
}

// A substitution inside single quotes is a literal, not a substitution: the
// first version of the lexer scanned the raw line for "$(" and declined
// perfectly ordinary greps.
func TestQuotedConstructsDoNotDecline(t *testing.T) {
	cases := []struct{ line, want string }{
		{`grep -n '$(foo)' src`, "pass"},
		{`echo 'a ` + "`" + `b` + "`" + ` c'`, "pass"},
		{`npx tsc --noEmit # $(this is a comment)`, "rewritten"},
		{`echo "$(real substitution)"`, "unparsed"}, // double quotes: it IS one
	}
	for _, c := range cases {
		if got := string(Rewrite(c.line, "", time.Minute).Outcome); got != c.want {
			t.Errorf("%q: outcome = %s, want %s", c.line, got, c.want)
		}
	}
}
