package heavycmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// makeNoRun are the make options after which no recipe runs.
var makeNoRun = map[string]bool{
	"dry-run": true, "just-print": true, "recon": true, "question": true,
	"version": true, "help": true, "touch": true,
}

// makeLongValue are the long options whose value is the next word when
// written without `=`; the value is never a target.
var makeLongValue = map[string]bool{
	"directory": true, "file": true, "include-dir": true, "old-file": true, "what-if": true,
}

// makeSelf stands for make's own name in a recipe once the lexer has seen it.
const makeSelf = "MAKE_SELF"

// makeSelfRE matches make's reference to itself in a recipe.
var makeSelfRE = regexp.MustCompile(`[$][({]MAKE[)}]`)

// makeRule is what the minimal Makefile parse keeps of a target.
type makeRule struct {
	prereqs []string
	recipe  []string
}

// makeClass classifies `make [opts] [VAR=val…] [targets…]`: ClassGoTest when
// a recipe line reached from the requested targets (or their prerequisites,
// up to maxDepth levels) runs a heavy go test, "" otherwise. A reached recipe
// that calls `pigtail admit|test` makes the whole make "" with NoteNested:
// the make would hold the go-test slot its own admission waits for. It never
// runs make nor a recipe.
func (r resolver) makeClass(args []string, depth int) (Class, Note) {
	base := r.dir
	if base == "" {
		base = "."
	}
	var targets []string
	var file string
	var chdirs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			targets = append(targets, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			if makeNoRun[name] {
				return "", NoteNone
			}
			switch {
			case makeLongValue[name]:
				if !hasVal {
					if i+1 >= len(args) {
						continue
					}
					i++
					val = args[i]
				}
				switch name {
				case "directory":
					chdirs = append(chdirs, val)
				case "file":
					file = val
				}
			case (name == "jobs" || name == "load-average") && !hasVal:
				if i+1 < len(args) && isNumber(args[i+1]) {
					i++
				}
			}
		case len(a) > 1 && a[0] == '-':
			for k := 1; k < len(a); k++ {
				c := a[k]
				if strings.IndexByte("nqvht", c) >= 0 {
					return "", NoteNone
				}
				if c == 'O' {
					// -O[type] takes an optional value, glued only: a bare -O
					// never consumes the next word, which stays a target.
					break
				}
				if strings.IndexByte("CfIoW", c) >= 0 {
					val := a[k+1:]
					if val == "" {
						if i+1 >= len(args) {
							break
						}
						i++
						val = args[i]
					}
					switch c {
					case 'C':
						chdirs = append(chdirs, val)
					case 'f':
						file = val
					}
					break
				}
				if c == 'j' || c == 'l' {
					if a[k+1:] == "" && i+1 < len(args) && isNumber(args[i+1]) {
						i++
					}
					break
				}
			}
		case a == "-" || assignRE.MatchString(a):
			// a lone dash or a VAR=val override: never a target
		default:
			targets = append(targets, a)
		}
	}
	for _, d := range chdirs {
		base = joinDir(base, d)
	}
	rules, first, ok := readMakefile(base, file)
	if !ok {
		return "", NoteNone
	}
	if len(targets) == 0 {
		if first == "" {
			return "", NoteNone
		}
		targets = []string{first}
	}
	sub := resolver{dir: base}
	found := false
	seen := map[string]bool{}
	var nested bool
	var visit func(target string, level int)
	visit = func(target string, level int) {
		rule, known := rules[target]
		if !known || seen[target] || level > maxDepth || nested {
			return
		}
		seen[target] = true
		for _, line := range rule.recipe {
			// $(MAKE) is make's own recursive call, also behind a wrapper
			// (`sh stamp.sh run -- $(MAKE) check-steps`); the lexer would read
			// it as a command substitution.
			segs, reason := lex(makeSelfRE.ReplaceAllString(line, makeSelf))
			if reason != ReasonNone {
				continue
			}
			for _, s := range segs {
				words := make([]string, len(s.words))
				for k, w := range s.words {
					words[k] = w.val
				}
				for k, w := range words {
					if w == makeSelf {
						words = append([]string{"make"}, words[k+1:]...)
						break
					}
				}
				c, _, note, _ := sub.classify(words, depth+1)
				if note == NoteNested {
					nested = true
				}
				if c == ClassGoTest {
					found = true
				}
			}
		}
		for _, p := range rule.prereqs {
			visit(p, level+1)
		}
	}
	for _, t := range targets {
		visit(t, 0)
	}
	if nested {
		return "", NoteNested
	}
	if found {
		return ClassGoTest, NoteNone
	}
	return "", NoteNone
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// readMakefile reads and parses the Makefile make would read from base: file
// when given, else GNUmakefile, makefile, Makefile in that order.
func readMakefile(base, file string) (rules map[string]*makeRule, first string, ok bool) {
	var data []byte
	var err error
	if file != "" {
		if !filepath.IsAbs(file) {
			file = filepath.Join(base, file)
		}
		data, err = os.ReadFile(file)
	} else {
		for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
			if data, err = os.ReadFile(filepath.Join(base, name)); err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, "", false
	}
	rules, first = parseMakefile(string(data))
	return rules, first, true
}

// makeDirectives are the lines that open with one of these words and are not
// rules. (The conditionals are masked before, by makeConditionals.)
var makeDirectives = map[string]bool{
	"include": true, "-include": true, "sinclude": true, "undefine": true, "vpath": true,
	"export": true, "unexport": true, "override": true,
}

// makeConditionals are the directives that leave a rule's recipe open.
// Conditionals are not evaluated: both branches are read.
var makeConditionals = map[string]bool{
	"ifeq": true, "ifneq": true, "ifdef": true, "ifndef": true, "else": true, "endif": true,
}

// parseMakefile is a deliberately minimal reading: rules `targets: prereqs`,
// tab-indented recipe lines, `\` continuations, and nothing else — no
// variables, no pattern rules, no includes. A target it cannot name is not
// followed.
func parseMakefile(src string) (rules map[string]*makeRule, first string) {
	rules = map[string]*makeRule{}
	// Join `\` continuations first; the next line's leading whitespace goes.
	var lines []string
	var cur string
	cont := false
	for _, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		if cont {
			raw = " " + strings.TrimLeft(raw, " \t")
		}
		if strings.HasSuffix(raw, "\\") {
			cur += strings.TrimSuffix(raw, "\\")
			cont = true
			continue
		}
		lines = append(lines, cur+raw)
		cur, cont = "", false
	}
	if cont {
		lines = append(lines, cur)
	}

	inDefine := false
	var current []*makeRule
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inDefine {
			if strings.HasPrefix(trimmed, "endef") {
				inDefine = false
			}
			continue
		}
		if strings.HasPrefix(line, "\t") {
			if len(current) > 0 {
				if body := recipeLine(line); body != "" {
					for _, rl := range current {
						rl.recipe = append(rl.recipe, body)
					}
				}
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A comment ends the line, unless a `;` came first: what follows it is
		// an inline recipe, passed whole to the shell (lex reads its `#`).
		pre := trimmed
		if i := strings.IndexByte(pre, ';'); i >= 0 {
			pre = pre[:i]
		}
		if i := strings.IndexByte(pre, '#'); i >= 0 {
			trimmed = strings.TrimSpace(pre[:i])
		}
		head, _, _ := strings.Cut(trimmed, " ")
		if head == "define" || ((head == "override" || head == "export") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, head)), "define")) {
			inDefine = true
			current = nil
			continue
		}
		if makeConditionals[head] {
			continue // inside a rule, a conditional does not end its recipe
		}
		if makeDirectives[head] {
			current = nil
			continue
		}
		colon := strings.IndexByte(trimmed, ':')
		eq := strings.IndexByte(trimmed, '=')
		if colon < 0 || (eq >= 0 && eq < colon) {
			current = nil // an assignment, or a line that is no rule
			continue
		}
		rest := trimmed[colon+1:]
		rest = strings.TrimPrefix(rest, ":") // `::`
		if strings.HasPrefix(rest, "=") {
			current = nil // `:=` / `::=`
			continue
		}
		var inline string
		if i := strings.IndexByte(rest, ';'); i >= 0 {
			rest, inline = rest[:i], strings.TrimSpace(rest[i+1:])
		}
		current = nil
		var prereqs []string
		for _, p := range strings.Fields(rest) {
			if !strings.ContainsAny(p, "$%") {
				prereqs = append(prereqs, p)
			}
		}
		for _, t := range strings.Fields(trimmed[:colon]) {
			if strings.HasPrefix(t, ".") || strings.ContainsAny(t, "$%") {
				continue
			}
			rl := rules[t]
			if rl == nil {
				rl = &makeRule{}
				rules[t] = rl
				if first == "" {
					first = t
				}
			}
			rl.prereqs = append(rl.prereqs, prereqs...)
			if inline != "" {
				rl.recipe = append(rl.recipe, recipeLine("\t"+inline))
			}
			current = append(current, rl)
		}
	}
	return rules, first
}

// recipeLine strips a recipe line to the command the shell will see: the
// leading tab, the `@ - +` prefixes, and `$$` → `$`.
func recipeLine(line string) string {
	s := strings.TrimSpace(line)
	s = strings.TrimLeft(s, "@-+ \t")
	return strings.ReplaceAll(s, "$$", "$")
}
