package heavycmd

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxDepth bounds script resolution: npm run a → run-s b c → npm run d …
const maxDepth = 4

// Note explains why a command was not routed, for the hook's log.
type Note string

const (
	NoteNone   Note = ""
	NoteOptOut Note = "optout" // PIGTAIL_ADMIT=0 on the command
	NoteNested Note = "nested" // already `pigtail admit` / `pigtail test`
	NoteSelf   Note = "self"   // the package.json script already calls pigtail
	// NoteOpaque is a command whose own argument is another command this
	// code will not read: `bash -c`, `eval`, `xargs`, `find -exec`,
	// `npx -c`. The line lexes fine; what it will actually run does not
	// show. Each carries its own Reason for the log.
	NoteOpaque Note = "opaque"
)

var (
	assignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// scriptNameRE is the fallback for a script the resolver cannot read (a
	// workspace package's own package.json): its name alone.
	scriptNameRE = regexp.MustCompile(`^(lint|eslint|type-?check|typecheck|tsc|test|tests|build-storybook|test-storybook|build)(:.*)?$`)
	// browserScriptRE names the scripts that run Storybook's vitest addon,
	// which runs nowhere but in Vitest browser mode (one live Chromium per
	// worker): a large monorepo's test-storybook and its module variants.
	browserScriptRE = regexp.MustCompile(`^test-storybook(:.*)?$`)
	longRunRE       = regexp.MustCompile(`(^|:)(watch|dev|serve|start|ui)($|:)`)
)

// resolver classifies commands against the package.json scripts reachable
// from dir.
type resolver struct {
	dir string
}

// Classify returns the class of an argv, resolving npm/yarn/pnpm scripts
// against the package.json nearest to dir. The empty Class means "not
// heavy" — or not routable, which Note then says.
func Classify(argv []string, dir string) (Class, Note) {
	c, _, note, _ := resolver{dir: dir}.classify(argv, 0)
	return c, note
}

// classify returns the class of a simple command, the index of the word
// admission must be inserted before (after assignments and wrappers), and a
// note when it deliberately declined.
func (r resolver) classify(words []string, depth int) (Class, int, Note, Reason) {
	if depth > maxDepth || len(words) == 0 {
		return "", 0, NoteNone, ReasonNone
	}
	i := 0
	for i < len(words) && assignRE.MatchString(words[i]) {
		if words[i] == "PIGTAIL_ADMIT=0" {
			return "", 0, NoteOptOut, ReasonNone
		}
		i++
	}
	i = skipWrappers(words, i)
	if i >= len(words) {
		return "", 0, NoteNone, ReasonNone
	}
	at := i
	if strings.HasPrefix(words[i], "$") {
		// `$TOOL --noEmit`, `${BIN} …`: the command's identity is in a
		// variable. Nothing to classify, and worth counting.
		return "", at, NoteOpaque, ReasonVarCommand
	}
	cmd := commandName(words[i])
	args := words[i+1:]

	if cmd == "node" {
		// node node_modules/.bin/tsc, node ./node_modules/typescript/bin/tsc
		j := 0
		for j < len(args) && strings.HasPrefix(args[j], "-") {
			j++
		}
		if j >= len(args) {
			return "", 0, NoteNone, ReasonNone
		}
		cmd, args = commandName(args[j]), args[j+1:]
		if !directBins[cmd] && len(args) > 0 {
			// node scripts/with-lock.js vitest --run: a wrapper script whose
			// own argv is the heavy command.
			if c, _, note, reason := r.classify(args, depth+1); c != "" || note != NoteNone {
				return c, at, note, reason
			}
			return "", 0, NoteNone, ReasonNone
		}
	}

	switch cmd {
	case "pigtail":
		if len(args) > 0 && (args[0] == "admit" || args[0] == "test") {
			return "", 0, NoteNested, ReasonNone
		}
		return "", 0, NoteNone, ReasonNone
	case "go":
		if goTestIsHeavy(args) {
			return ClassGoTest, at, NoteNone, ReasonNone
		}
		return "", 0, NoteNone, ReasonNone
	case "make":
		c, note := r.makeClass(args, depth)
		if c != "" {
			return c, at, note, ReasonNone
		}
		return "", 0, note, ReasonNone
	case "git":
		// A commit is cheap — unless the repo hangs lint-staged off its
		// pre-commit hook, in which case it costs what that lint costs. Both
		// signals must be present, and neither is executed to find out.
		if rest, cwd, isCommit := gitCommitArgs(args, r.dir); isCommit && gitCommitIsHeavy(rest, cwd) {
			return ClassPrecommit, at, NoteNone, ReasonNone
		}
		return "", 0, NoteNone, ReasonNone
	case "tsc", "vue-tsc":
		if hasAny(args, "-w", "--watch", "--showConfig", "--init", "-v", "--version", "-h", "--help") {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassTSC, at, NoteNone, ReasonNone
	case "eslint":
		if hasAny(args, "--print-config", "--init", "--env-info", "-v", "--version", "-h", "--help") {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassESLint, at, NoteNone, ReasonNone
	case "jest":
		if hasAny(args, "--watch", "--watchAll", "--listTests", "--showConfig", "--version", "--help", "-h") {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassJest, at, NoteNone, ReasonNone
	case "vitest":
		if sub := firstPositional(args); sub == "watch" || sub == "dev" || sub == "init" || sub == "list" {
			return "", 0, NoteNone, ReasonNone
		}
		if hasAny(args, "--ui", "--watch", "-w", "--version", "-v", "--help", "-h") {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassVitest, at, NoteNone, ReasonNone
	case "nx":
		if nxIsLight(args) {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassNx, at, NoteNone, ReasonNone
	case "storybook":
		if firstPositional(args) == "build" {
			return ClassStorybookBuild, at, NoteNone, ReasonNone
		}
		return "", 0, NoteNone, ReasonNone
	case "build-storybook":
		return ClassStorybookBuild, at, NoteNone, ReasonNone
	case "vite":
		if firstPositional(args) != "build" || hasAny(args, "--watch", "-w", "--help", "-h", "--version", "-v") {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassBuild, at, NoteNone, ReasonNone
	case "next":
		if firstPositional(args) != "build" || hasAny(args, "--help", "-h") {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassBuild, at, NoteNone, ReasonNone
	case "bunchee", "tsup", "rollup":
		if hasAny(args, "--watch", "-w", "--help", "-h", "--version", "-v") ||
			(cmd == "rollup" && hasShortFlag(args, 'w')) {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassBuild, at, NoteNone, ReasonNone
	case "webpack":
		if webpackIsLight(args) {
			return "", 0, NoteNone, ReasonNone
		}
		return ClassBuild, at, NoteNone, ReasonNone
	case "npx", "pnpx", "bunx":
		c, note, reason := r.launcher(args, depth)
		return c, at, note, reason
	case "npm":
		c, note := r.npm(args, depth)
		return c, at, note, ReasonNone
	case "yarn", "pnpm", "bun":
		c, note := r.yarnLike(cmd, args, depth)
		return c, at, note, ReasonNone
	case "run-s", "run-p", "npm-run-all":
		c, note := r.runAll(args, depth)
		return c, at, note, ReasonNone
	case "concurrently":
		c, note := r.concurrently(args, depth)
		return c, at, note, ReasonNone
	// The opaque family: the line parses, but what it runs is inside a
	// string this code will not read. Reported rather than passed silently,
	// so the log can say how often admission is bypassed this way.
	case "bash", "sh", "zsh", "dash", "ksh":
		if hasAny(args, "-c") {
			return "", 0, NoteOpaque, ReasonShellC
		}
		return "", 0, NoteNone, ReasonNone
	case "eval":
		return "", 0, NoteOpaque, ReasonEval
	case "xargs":
		return "", 0, NoteOpaque, ReasonXargs
	case "find":
		if hasAny(args, "-exec", "-execdir", "-ok", "-okdir") {
			return "", 0, NoteOpaque, ReasonFindExec
		}
		return "", 0, NoteNone, ReasonNone
	}
	return "", 0, NoteNone, ReasonNone
}

// directBins are the heavy binaries recognised by name.
var directBins = map[string]bool{
	"tsc": true, "vue-tsc": true, "eslint": true, "jest": true, "vitest": true,
	"nx": true, "storybook": true, "build-storybook": true,
	"vite": true, "next": true, "bunchee": true, "tsup": true, "webpack": true, "rollup": true,
}

// skipWrappers steps over commands that only wrap the real one.
func skipWrappers(words []string, i int) int {
	for i < len(words) {
		switch words[i] {
		case "time", "command", "exec", "nohup", "builtin":
			i++
			for i < len(words) && strings.HasPrefix(words[i], "-") {
				i++
			}
		case "nice":
			i++
			for i < len(words) && strings.HasPrefix(words[i], "-") {
				if words[i] == "-n" {
					i++
				}
				i++
			}
		case "env":
			i++
			for i < len(words) && (strings.HasPrefix(words[i], "-") || assignRE.MatchString(words[i])) {
				if words[i] == "-u" || words[i] == "-C" || words[i] == "-S" {
					i++
				}
				i++
			}
		case "timeout", "gtimeout":
			i++
			for i < len(words) && strings.HasPrefix(words[i], "-") {
				if words[i] == "-s" || words[i] == "-k" || words[i] == "--signal" || words[i] == "--kill-after" {
					i++
				}
				i++
			}
			i++ // the duration
		default:
			return i
		}
	}
	return i
}

// commandName is a command word's basename without a JS extension:
// node_modules/.bin/tsc → tsc, …/eslint/bin/eslint.js → eslint.
func commandName(w string) string {
	b := filepath.Base(w)
	for _, ext := range []string{".js", ".mjs", ".cjs"} {
		b = strings.TrimSuffix(b, ext)
	}
	return b
}

func hasAny(args []string, flags ...string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		name, _, _ := strings.Cut(a, "=")
		for _, f := range flags {
			if name == f {
				return true
			}
		}
	}
	return false
}

// hasShortFlag reports whether a bundled single-dash cluster (`-cw`) holds
// the given short flag, up to a `--` terminator.
func hasShortFlag(args []string, flag byte) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a, flag) > 0 {
			return true
		}
	}
	return false
}

func firstPositional(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// nxIsLight: nx subcommands that read the graph rather than run tasks, and
// task runs that start a long-lived server — those belong to rammon, not to
// admission.
func nxIsLight(args []string) bool {
	switch firstPositional(args) {
	case "", "show", "graph", "reset", "daemon", "report", "list", "init", "migrate", "add",
		"connect", "view-logs", "help", "g", "generate", "watch", "format", "format:check", "format:write":
		return true
	}
	if hasAny(args, "--version", "--help", "-h") {
		return true
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		for _, part := range strings.FieldsFunc(a, func(r rune) bool { return r == ':' || r == ',' || r == '=' }) {
			switch part {
			case "serve", "dev", "start", "watch", "storybook":
				return true
			}
		}
	}
	return false
}

// webpackIsLight: webpack-cli subcommands that serve, watch or only report,
// and the flags that do the same. Anything else is a build.
func webpackIsLight(args []string) bool {
	switch firstPositional(args) {
	case "serve", "server", "s", "watch", "w", "info", "i", "configtest", "t",
		"version", "v", "help", "h", "init", "create", "new", "c", "loader", "l", "plugin", "p", "migrate", "m":
		return true
	}
	return hasAny(args, "--watch", "-w", "--help", "-h", "--version", "-v")
}

// launcher handles npx/pnpx/bunx [flags] <cmd> [args].
func (r resolver) launcher(args []string, depth int) (Class, Note, Reason) {
	j := 0
	for j < len(args) && strings.HasPrefix(args[j], "-") {
		switch args[j] {
		case "-c", "--call":
			return "", NoteOpaque, ReasonNpxCall
		case "-p", "--package":
			j++
		}
		j++
	}
	if j < len(args) && args[j] == "--" {
		j++
	}
	c, _, note, reason := r.classify(args[j:], depth+1)
	return c, note, reason
}

// npm handles npm [--prefix d] [-w pkg] run|test|exec ….
func (r resolver) npm(args []string, depth int) (Class, Note) {
	rr, workspace := r, false
	j := 0
	for j < len(args) && strings.HasPrefix(args[j], "-") {
		name, val, hasVal := strings.Cut(args[j], "=")
		switch name {
		case "--prefix", "-C":
			if !hasVal && j+1 < len(args) {
				j++
				val = args[j]
			}
			rr.dir = joinDir(r.dir, val)
		case "-w", "--workspace":
			workspace = true
			if !hasVal {
				j++
			}
		}
		j++
	}
	if j >= len(args) {
		return "", NoteNone
	}
	sub, rest := args[j], args[j+1:]
	// npm run lint -w pkg: the workspace flag may also follow the script.
	for _, a := range rest {
		if a == "--" {
			break
		}
		if name, _, _ := strings.Cut(a, "="); name == "-w" || name == "--workspace" || name == "--workspaces" || name == "-ws" {
			workspace = true
		}
	}
	switch sub {
	case "run", "run-script", "rum", "urn":
		name := firstPositional(rest)
		if name == "" {
			return "", NoteNone
		}
		return rr.script(name, afterDashDash(rest), workspace, depth)
	case "test", "t", "tst":
		return rr.script("test", afterDashDash(rest), workspace, depth)
	case "exec", "x":
		k := 0
		for k < len(rest) && strings.HasPrefix(rest[k], "-") && rest[k] != "--" {
			k++
		}
		if k < len(rest) && rest[k] == "--" {
			k++
		}
		c, _, note, _ := rr.classify(rest[k:], depth+1)
		return c, note
	}
	return "", NoteNone
}

// yarnNonScripts are yarn/pnpm/bun subcommands that manage packages or the
// tool itself; `yarn <x>` runs a script or a binary only when x is not one.
var yarnNonScripts = map[string]bool{
	"install": true, "i": true, "add": true, "remove": true, "rm": true, "why": true, "info": true,
	"up": true, "upgrade": true, "update": true, "init": true, "create": true, "set": true, "config": true,
	"cache": true, "audit": true, "outdated": true, "publish": true, "pack": true, "link": true,
	"unlink": true, "import": true, "licenses": true, "node": true, "bin": true, "plugin": true,
	"version": true, "npm": true, "dedupe": true, "list": true, "ls": true, "store": true,
	"workspaces": true, "patch": true, "patch-commit": true, "constraints": true, "start": true,
	"dev": true, "serve": true, "help": true,
}

// yarnLike handles yarn / pnpm / bun.
func (r resolver) yarnLike(tool string, args []string, depth int) (Class, Note) {
	rr, workspace := r, false
	j := 0
	for j < len(args) && strings.HasPrefix(args[j], "-") {
		name, val, hasVal := strings.Cut(args[j], "=")
		switch name {
		case "--cwd", "-C", "--dir", "--prefix":
			if !hasVal && j+1 < len(args) {
				j++
				val = args[j]
			}
			rr.dir = joinDir(r.dir, val)
		case "--filter", "-F":
			workspace = true
			if !hasVal {
				j++
			}
		case "-r", "--recursive":
			workspace = true
		}
		j++
	}
	if j >= len(args) {
		return "", NoteNone // bare `yarn` installs
	}
	sub, rest := args[j], args[j+1:]
	switch {
	case sub == "exec" || sub == "dlx" || sub == "x":
		c, _, note, _ := rr.classify(rest, depth+1)
		return c, note
	case sub == "run":
		name := firstPositional(rest)
		if name == "" {
			return "", NoteNone
		}
		return rr.script(name, afterDashDash(rest), workspace, depth)
	case sub == "test" || sub == "t":
		return rr.script("test", rest, workspace, depth)
	case sub == "workspace" && tool == "yarn":
		// yarn workspace <pkg> <script|bin> [args]
		if len(rest) < 2 {
			return "", NoteNone
		}
		return rr.script(rest[1], rest[2:], true, depth)
	case sub == "workspaces" && tool == "yarn":
		// yarn workspaces foreach [flags] run <script>
		for k, a := range rest {
			if a == "run" && k+1 < len(rest) {
				return rr.script(rest[k+1], rest[k+2:], true, depth)
			}
		}
		return "", NoteNone
	case yarnNonScripts[sub]:
		return "", NoteNone
	}
	// yarn <script> or yarn <bin>.
	if body, ok := rr.scriptBody(sub); ok && !workspace {
		c, note := rr.classifyScript(body, rest, depth)
		return byScriptName(sub, c), note
	}
	if workspace {
		return rr.script(sub, rest, true, depth)
	}
	c, _, note, _ := rr.classify(append([]string{sub}, rest...), depth+1)
	return c, note
}

// script classifies `<runner> run <name> [-- extra]`.
func (r resolver) script(name string, extra []string, workspace bool, depth int) (Class, Note) {
	if hasAny(extra, "--watch", "--watchAll", "-w", "--ui") {
		return "", NoteNone
	}
	if !workspace {
		if body, ok := r.scriptBody(name); ok {
			c, note := r.classifyScript(body, extra, depth)
			return byScriptName(name, c), note
		}
	}
	// The script lives in a package we did not read (a workspace member):
	// judge it by its name.
	if !scriptNameRE.MatchString(name) || longRunRE.MatchString(name) {
		return "", NoteNone
	}
	switch base, _, _ := strings.Cut(name, ":"); base {
	case "lint", "eslint":
		return ClassESLint, NoteNone
	case "type-check", "typecheck", "tsc":
		return ClassTSC, NoteNone
	case "test", "tests":
		return ClassJest, NoteNone // the conservative runner: priced highest
	case "build-storybook":
		return ClassStorybookBuild, NoteNone
	case "build":
		return ClassBuild, NoteNone
	case "test-storybook":
		return ClassVitestBrowser, NoteNone
	}
	return "", NoteNone
}

// byScriptName re-classes what a script body resolved to when the script's
// name says what its argv cannot: a vitest run behind a test-storybook script
// is a browser-mode run, though its argv is a node run's. Only a vitest body
// is re-classed — a test-storybook script running something else keeps its
// own class.
func byScriptName(name string, c Class) Class {
	if c == ClassVitest && browserScriptRE.MatchString(name) {
		return ClassVitestBrowser
	}
	return c
}

// classifyScript classifies a package.json script body: the heaviest of the
// commands it runs.
func (r resolver) classifyScript(body string, extra []string, depth int) (Class, Note) {
	if strings.Contains(body, "pigtail") {
		return "", NoteSelf // the repo integrates admission itself
	}
	segs, lexReason := lex(body)
	if lexReason != ReasonNone {
		return "", NoteNone
	}
	var best Class
	for i, s := range segs {
		words := make([]string, len(s.words))
		for k, w := range s.words {
			words[k] = w.val
		}
		// Extra args after `--` go to the script's last command.
		if i == len(segs)-1 && len(extra) > 0 {
			words = append(words, extra...)
		}
		c, _, note, _ := r.classify(words, depth+1)
		if note == NoteSelf {
			return "", NoteSelf
		}
		best = Heavier(best, c)
	}
	return best, NoteNone
}

// runAll handles run-s / run-p / npm-run-all: its positional args are script
// names or globs (lint:*).
func (r resolver) runAll(args []string, depth int) (Class, Note) {
	scripts := r.scripts()
	var best Class
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		pat := strings.ReplaceAll(a, ":", "/")
		// Sorted, so a tie between two equally costly classes resolves the
		// same way on every call rather than by map order.
		names := make([]string, 0, len(scripts))
		for name := range scripts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if ok, _ := path.Match(pat, strings.ReplaceAll(name, ":", "/")); ok {
				c, note := r.classifyScript(scripts[name], nil, depth+1)
				c = byScriptName(name, c)
				if note == NoteSelf {
					return "", NoteSelf
				}
				best = Heavier(best, c)
			}
		}
	}
	return best, NoteNone
}

// concurrently handles `concurrently "cmd one" "npm:script"`.
func (r resolver) concurrently(args []string, depth int) (Class, Note) {
	var best Class
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		var c Class
		var note Note
		if name, ok := strings.CutPrefix(a, "npm:"); ok {
			c, note = r.script(name, nil, false, depth+1)
		} else {
			c, note = r.classifyScript(a, nil, depth+1)
		}
		if note == NoteSelf {
			return "", NoteSelf
		}
		best = Heavier(best, c)
	}
	return best, NoteNone
}

func afterDashDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// scriptBody returns the body of a script in the nearest package.json.
func (r resolver) scriptBody(name string) (string, bool) {
	body, ok := r.scripts()[name]
	return body, ok
}

// scripts returns the "scripts" of the package.json nearest to r.dir,
// walking up; nil when there is none or it cannot be read.
func (r resolver) scripts() map[string]string {
	if r.dir == "" {
		return nil
	}
	dir := r.dir
	for {
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal(data, &pkg) != nil {
				return nil
			}
			return pkg.Scripts
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// joinDir resolves a --cwd/--prefix/cd target against base.
func joinDir(base, target string) string {
	if strings.HasPrefix(target, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, target[2:])
		}
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(base, target)
}

// goTestValueFlags are the `go test` flags that take their value as the next
// word when written without `=`. An unknown flag consumes nothing: at worst a
// value is counted as a package pattern (over-classing, never a leak).
var goTestValueFlags = map[string]bool{
	"run": true, "count": true, "p": true, "timeout": true, "parallel": true, "tags": true,
	"bench": true, "skip": true, "cpu": true, "coverprofile": true, "coverpkg": true,
	"covermode": true, "o": true, "exec": true, "list": true, "shuffle": true, "vet": true,
	"mod": true, "modfile": true, "ldflags": true, "gcflags": true, "C": true,
	"outputdir": true, "cpuprofile": true, "memprofile": true, "blockprofile": true,
	"mutexprofile": true, "trace": true, "fuzz": true, "fuzztime": true,
	"fuzzminimizetime": true, "benchtime": true, "pkgdir": true, "toolexec": true,
	"overlay": true, "pgo": true, "asmflags": true, "buildmode": true, "compiler": true,
	"gccgoflags": true, "installsuffix": true, "memprofilerate": true,
	"blockprofilerate": true, "mutexprofilefraction": true,
}

// goTestIsHeavy reports whether `go <args>` is a heavy test suite: a `go test`
// with -race, a package pattern holding "...", or at least two patterns.
// `go test -h/-help/-list` run nothing; other subcommands are not tests.
func goTestIsHeavy(args []string) bool {
	if len(args) == 0 || args[0] != "test" {
		return false
	}
	race, dots := false, false
	patterns := 0
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "-args" || a == "--args" {
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
			switch name {
			case "h", "help", "list", "n":
				return false
			case "race":
				if !hasVal || (val != "false" && val != "0") {
					race = true
				}
			}
			if !hasVal && goTestValueFlags[name] {
				i++
			}
			continue
		}
		patterns++
		if strings.Contains(a, "...") {
			dots = true
		}
	}
	return race || dots || patterns >= 2
}
