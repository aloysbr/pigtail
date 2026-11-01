package heavycmd

import (
	"regexp"
	"strings"
)

// Reason names why a line could not be routed. It is the one thing, besides
// a single command word, that may be written to a log: everything else on a
// command line — arguments, environment values, URLs — can be a secret.
type Reason string

const (
	ReasonNone                Reason = ""
	ReasonHeredoc             Reason = "heredoc"
	ReasonCommandSubstitution Reason = "command-substitution"
	ReasonBacktick            Reason = "backtick"
	ReasonProcessSubstitution Reason = "process-substitution"
	ReasonUnterminatedQuote   Reason = "unterminated-quote"
	ReasonShellC              Reason = "shell-c"     // bash -c '…'
	ReasonEval                Reason = "eval"        // eval '…'
	ReasonXargs               Reason = "xargs"       // xargs <cmd>
	ReasonFindExec            Reason = "find-exec"   // find … -exec <cmd>
	ReasonNpxCall             Reason = "npx-call"    // npx -c '…'
	ReasonVarCommand          Reason = "var-command" // $TOOL …
)

func (r Reason) String() string { return string(r) }

// unknownCommand is what stands in for a command word that cannot be known
// without running something (`$TOOL …`, “ `which tsc` … “).
const unknownCommand = "?"

// word is one shell word: its unquoted value and where it sits in the line.
type word struct {
	val        string
	start, end int
}

// segment is one simple command: the words it runs (redirections removed)
// and the operator that ends it ("" at the end of the line).
type segment struct {
	words []word
	op    string
}

// redirRE matches a redirection operator at a token start: an optional fd,
// then >, >>, >|, <, <>, <&, >& — or bash's &> / &>>.
var redirRE = regexp.MustCompile(`^(?:\d*(?:>>|>\||>&|<>|<&|>|<)|&>>|&>)`)

// herestringOrRedir returns the redirection operator at the head of s, "" if
// there is none. `<<<` is matched before redirRE would split it into `<` and
// a leftover `<<` that the heredoc rule above then mistook for a heredoc.
func herestringOrRedir(s string) string {
	if strings.HasPrefix(s, "<<<") {
		return "<<<"
	}
	return redirRE.FindString(s)
}

// lex splits a command line into simple commands.
//
// It understands exactly what routing a command needs — quotes, backslash
// escapes, the list and pipeline operators, subshell parentheses,
// redirections and comments — and declines, with a Reason, everything whose
// words it could not know without executing something: command and process
// substitution, backticks, heredocs, an unterminated quote. A declined line
// is passed through untouched and the caller logs the reason.
//
// The constructs are recognised **while scanning**, not by searching the raw
// line: `grep '$(foo)' src` is a literal and must still be parsed, and so
// must a backtick inside single quotes. Searching the raw text declined both.
//
// Offsets are byte offsets into line, so a rewrite can insert text before a
// word and leave every other byte — globs, quoting, spacing — exactly as the
// agent wrote it.
func lex(line string) (segs []segment, reason Reason) {
	var cur segment
	flush := func(op string) {
		cur.op = op
		if len(cur.words) > 0 || op != "" {
			segs = append(segs, cur)
		}
		cur = segment{}
	}
	i, n := 0, len(line)
	for i < n {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '\\' && i+1 < n && line[i+1] == '\n': // line continuation
			i += 2
			continue
		case c == '#':
			// Reached at a word boundary (spaces are skipped above), which is
			// exactly where bash starts a comment; mid-word a '#' is an
			// ordinary character and readWord keeps it (a git revision, a
			// CSS colour).
			for i < n && line[i] != '\n' {
				i++
			}
			continue
		case c == '\n' || c == ';':
			flush(string(c))
			i++
			continue
		case c == '(' || c == ')':
			flush(string(c))
			i++
			continue
		case c == '&' && i+1 < n && line[i+1] == '&':
			flush("&&")
			i += 2
			continue
		case c == '|' && i+1 < n && line[i+1] == '|':
			flush("||")
			i += 2
			continue
		case c == '|':
			flush("|")
			i++
			continue
		case (c == '<' || c == '>') && i+1 < n && line[i+1] == '(':
			return nil, ReasonProcessSubstitution
		case c == '<' && strings.HasPrefix(line[i:], "<<") && !strings.HasPrefix(line[i:], "<<<"):
			// A heredoc redefines every following line as data; a lexer that
			// kept reading would take the payload for commands. A herestring
			// (<<<) does not: its single word is data like any redirect
			// target, so it is matched first, below.
			return nil, ReasonHeredoc
		}
		if m := herestringOrRedir(line[i:]); m != "" {
			i += len(m)
			if strings.HasSuffix(m, "&") && !strings.HasPrefix(m, "&") {
				// >&1, <&-: the target is glued to the operator.
				for i < n && (line[i] >= '0' && line[i] <= '9' || line[i] == '-') {
					i++
				}
				continue
			}
			for i < n && (line[i] == ' ' || line[i] == '\t') {
				i++
			}
			_, next, wr := readWord(line, i)
			if wr != ReasonNone {
				return nil, wr
			}
			i = next
			continue
		}
		if c == '&' {
			flush("&")
			i++
			continue
		}
		val, next, wr := readWord(line, i)
		if wr != ReasonNone {
			return nil, wr
		}
		cur.words = append(cur.words, word{val: val, start: i, end: next})
		i = next
	}
	flush("")
	return segs, ReasonNone
}

// readWord reads one word starting at i, unquoting as the shell would, and
// declines the substitutions it cannot evaluate. Single quotes make them
// literal; double quotes do not.
func readWord(line string, i int) (val string, next int, reason Reason) {
	var b strings.Builder
	n := len(line)
	for i < n {
		c := line[i]
		switch c {
		case ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>':
			return b.String(), i, ReasonNone
		case '\'':
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				return "", 0, ReasonUnterminatedQuote
			}
			b.WriteString(line[i+1 : i+1+j])
			i += j + 2
		case '"':
			i++
			closed := false
			for i < n {
				if line[i] == '"' {
					closed = true
					i++
					break
				}
				if r := substitutionAt(line, i); r != ReasonNone {
					return "", 0, r
				}
				if line[i] == '\\' && i+1 < n && strings.IndexByte("\"\\$`\n", line[i+1]) >= 0 {
					b.WriteByte(line[i+1])
					i += 2
					continue
				}
				b.WriteByte(line[i])
				i++
			}
			if !closed {
				return "", 0, ReasonUnterminatedQuote
			}
		case '\\':
			if i+1 < n {
				b.WriteByte(line[i+1])
				i += 2
			} else {
				i++
			}
		default:
			if r := substitutionAt(line, i); r != ReasonNone {
				return "", 0, r
			}
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i, ReasonNone
}

// substitutionAt reports the substitution starting at i, if any. A plain
// `$VAR` is not one: its value is unknown, but the word's *position* and the
// command word around it still are, which is all the lexer needs.
func substitutionAt(line string, i int) Reason {
	switch line[i] {
	case '`':
		return ReasonBacktick
	case '$':
		if i+1 < len(line) && line[i+1] == '(' {
			return ReasonCommandSubstitution
		}
	}
	return ReasonNone
}

// firstCommandWord returns the command word of a line the lexer declined —
// the only part of it that may be logged.
//
// It re-reads the head of the line by hand, because the lexer gave up: skip
// leading assignments, then take the first word, and refuse anything that is
// not plainly a command name. A name that came from a variable or a
// substitution is reported as unknownCommand rather than guessed at.
func firstCommandWord(line string) string {
	i := 0
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\n') {
			i++
		}
		raw := rawWordAt(line, i)
		if raw == "" {
			return unknownCommand
		}
		if assignRE.MatchString(raw) {
			i += len(raw)
			continue
		}
		return safeCommandWord(raw)
	}
}

// rawWordAt returns the raw (still quoted) text of the word starting at i.
func rawWordAt(line string, i int) string {
	start := i
	for i < len(line) {
		switch line[i] {
		case ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>':
			return line[start:i]
		}
		i++
	}
	return line[start:]
}

// safeCommandWordRE is what a command word may look like to be logged: a
// plain name or path, nothing that could carry a value.
var safeCommandWordRE = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// safeCommandWord reduces a raw command word to a bare name safe to log, or
// unknownCommand. The basename, not the path: knowing it was "tsc" is what
// makes the log useful, and the directories around it are nobody's business.
func safeCommandWord(raw string) string {
	if idx := strings.LastIndexByte(raw, '/'); idx >= 0 {
		raw = raw[idx+1:]
	}
	if raw == "" || !safeCommandWordRE.MatchString(raw) {
		return unknownCommand
	}
	return raw
}
