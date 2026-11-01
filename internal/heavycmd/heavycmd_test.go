package heavycmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestLex(t *testing.T) {
	cases := []struct {
		line  string
		words [][]string
		ops   []string
	}{
		{`npx tsc --noEmit`, [][]string{{"npx", "tsc", "--noEmit"}}, []string{""}},
		{`a && b || c; d | e & f`,
			[][]string{{"a"}, {"b"}, {"c"}, {"d"}, {"e"}, {"f"}},
			[]string{"&&", "||", ";", "|", "&", ""}},
		{`eslint 'src/**/*.ts' "a b" c\ d`, [][]string{{"eslint", "src/**/*.ts", "a b", "c d"}}, []string{""}},
		{`npx tsc --noEmit 2>&1 | head -50`, [][]string{{"npx", "tsc", "--noEmit"}, {"head", "-50"}}, []string{"|", ""}},
		{`tsc > out.txt 2> err.txt`, [][]string{{"tsc"}}, []string{""}},
		{`tsc &> all.log`, [][]string{{"tsc"}}, []string{""}},
		{`(cd a && tsc)`, [][]string{{}, {"cd", "a"}, {"tsc"}}, []string{"(", "&&", ")"}},
		{"tsc # a comment && eslint", [][]string{{"tsc"}}, []string{""}},
		{`FOO="x y" tsc`, [][]string{{"FOO=x y", "tsc"}}, []string{""}},
	}
	for _, c := range cases {
		segs, reason := lex(c.line)
		if reason != ReasonNone {
			t.Errorf("%q: refused (%s)", c.line, reason)
			continue
		}
		var words [][]string
		var ops []string
		for _, s := range segs {
			ws := []string{}
			for _, w := range s.words {
				ws = append(ws, w.val)
			}
			words = append(words, ws)
			ops = append(ops, s.op)
		}
		if !reflect.DeepEqual(words, c.words) || !reflect.DeepEqual(ops, c.ops) {
			t.Errorf("%q:\n got  %q %q\n want %q %q", c.line, words, ops, c.words, c.ops)
		}
	}
}

func TestLexRefuses(t *testing.T) {
	for _, line := range []string{
		`echo $(npx tsc)`, "echo `tsc`", "cat <<EOF\ntsc\nEOF", `diff <(tsc) b`, `echo 'unterminated`, `echo "open`,
	} {
		if _, reason := lex(line); reason == ReasonNone {
			t.Errorf("%q: must be refused", line)
		}
	}
}

func TestLexOffsets(t *testing.T) {
	line := `FOO=1 npx tsc -p "a b"`
	segs, _ := lex(line)
	for _, w := range segs[0].words {
		if got := line[w.start:w.end]; strings.Trim(got, `"`) != w.val {
			t.Errorf("word %q spans %q", w.val, got)
		}
	}
}

func TestClassify(t *testing.T) {
	app := fixture(t, "app")
	lib := fixture(t, "lib")
	gm := fixture(t, "gomake")
	gnu := fixture(t, "gomake-gnu")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		argv string
		dir  string
		want Class
		note Note
	}{
		// direct binaries
		{"tsc --noEmit", "", ClassTSC, ""},
		{"node_modules/.bin/tsc -p tsconfig.json", "", ClassTSC, ""},
		{"vue-tsc --noEmit", "", ClassTSC, ""},
		{"eslint src", "", ClassESLint, ""},
		{"jest --ci", "", ClassJest, ""},
		{"vitest run", "", ClassVitest, ""},
		{"vitest", "", ClassVitest, ""},
		{"nx run-many -t lint test", "", ClassNx, ""},
		{"nx affected -t test", "", ClassNx, ""},
		{"storybook build", "", ClassStorybookBuild, ""},
		{"build-storybook", "", ClassStorybookBuild, ""},
		// JS builds (2026-10-28): the bundlers by name; never go build / make build
		{"vite build", "", ClassBuild, ""},
		{"vite build --watch", "", "", ""},
		{"vite", "", "", ""},
		{"vite dev", "", "", ""},
		{"next build", "", ClassBuild, ""},
		{"next dev", "", "", ""},
		{"next start", "", "", ""},
		{"bunchee", "", ClassBuild, ""},
		{"bunchee --watch", "", "", ""},
		{"tsup src/index.ts --dts", "", ClassBuild, ""},
		{"tsup --watch", "", "", ""},
		{"webpack --mode production", "", ClassBuild, ""},
		{"webpack serve", "", "", ""},
		{"webpack --watch", "", "", ""},
		{"rollup -c", "", ClassBuild, ""},
		{"rollup -c -w", "", "", ""},
		{"rollup -cw", "", "", ""},
		{"rollup -wc", "", "", ""},
		{"rollup --version", "", "", ""},
		{"npx vite build", "", ClassBuild, ""},
		{"pnpm exec tsup", "", ClassBuild, ""},
		{"node node_modules/.bin/next build", "", ClassBuild, ""},
		{"go build ./...", "", "", ""},
		{"make build", "", "", ""},
		{"node ./node_modules/typescript/bin/tsc", "", ClassTSC, ""},
		{"node --max-old-space-size=4096 node_modules/eslint/bin/eslint.js .", "", ClassESLint, ""},
		{"node scripts/with-test-lock.js vitest --run", "", ClassVitest, ""},
		// long-running or informational: not admitted
		{"tsc --watch", "", "", ""},
		{"tsc -w", "", "", ""},
		{"tsc --version", "", "", ""},
		{"eslint --print-config x.ts", "", "", ""},
		{"jest --watchAll", "", "", ""},
		{"jest --listTests", "", "", ""},
		{"vitest watch", "", "", ""},
		{"vitest --ui", "", "", ""},
		{"nx graph", "", "", ""},
		{"nx serve web", "", "", ""},
		{"nx run web:serve", "", "", ""},
		{"nx run web:storybook", "", "", ""},
		{"nx show projects", "", "", ""},
		{"storybook dev -p 6006", "", "", ""},
		{"node scripts/seed.js", "", "", ""},
		{"grep tsc src", "", "", ""},
		{"cat jest.config.js", "", "", ""},
		{"ls node_modules/.bin", "", "", ""},
		{"biome check .", "", "", ""},
		// wrappers and assignments
		{"FOO=1 BAR=2 tsc", "", ClassTSC, ""},
		{"time npx tsc", "", ClassTSC, ""},
		{"nice -n 10 eslint .", "", ClassESLint, ""},
		{"env NODE_OPTIONS=--x -u HOME jest", "", ClassJest, ""},
		{"timeout -s KILL 300 tsc", "", ClassTSC, ""},
		// launchers
		{"npx tsc --noEmit", "", ClassTSC, ""},
		{"npx -y -p typescript tsc", "", ClassTSC, ""},
		{"npx --no-install eslint .", "", ClassESLint, ""},
		{"pnpx vitest run", "", ClassVitest, ""},
		{"bunx jest", "", ClassJest, ""},
		{"yarn exec tsc", "", ClassTSC, ""},
		{"pnpm exec eslint .", "", ClassESLint, ""},
		{"npm exec -- tsc", "", ClassTSC, ""},
		{"npx -c 'tsc && eslint'", "", "", NoteOpaque},
		// scripts resolved from package.json
		{"npm run type-check", app, ClassTSC, ""},
		{"npm run lint", app, ClassESLint, ""},
		{"npm test", app, ClassJest, ""},
		{"npm t", app, ClassJest, ""},
		{"npm run check", app, ClassJest, ""}, // biome + tsc + jest: the heaviest wins
		{"npm run test-storybook", app, ClassVitestBrowser, ""},
		{"npm run test-storybook -- --run", app, ClassVitestBrowser, ""},
		{"npm run test-storybook:run", app, ClassVitestBrowser, ""},
		{"npm run test-storybook:structure", app, ClassVitestBrowser, ""},
		{"yarn test-storybook", app, ClassVitestBrowser, ""},
		{"npm run test-storybook:watch", app, "", ""},
		{"npm run test-storybook -- --watch", app, "", ""},
		{"npm run test-storybook:runner", app, "", ""}, // not vitest: the name alone does not re-class it
		{"npm run test-storybook:ui", app, "", ""},
		{"npm run test:watch", app, "", ""},
		{"npm test -- --watch", app, "", ""},
		{"npm run build-storybook", app, ClassStorybookBuild, ""},
		{"npm run storybook", app, "", ""},
		{"npm run dev", app, "", ""},
		{"npm run all", app, ClassESLint, ""}, // run-s lint:* type-check: eslint and tsc tie, the first is kept
		{"npm run par", app, ClassTSC, ""},
		{"npm run sb-all", app, ClassVitestBrowser, ""},
		{"npm run guarded", app, "", NoteSelf},
		{"npm run build", app, ClassBuild, ""}, // tsc then vite build: the build wins
		{"npm run build:lib", app, ClassBuild, ""},
		{"npm run build:next", app, ClassBuild, ""},
		{"npm run build:watch", app, "", ""},
		{"npm run build:go", app, "", ""},
		{"yarn build", app, ClassBuild, ""},
		{"yarn lint", app, ClassESLint, ""},
		{"yarn run type-check", app, ClassTSC, ""},
		{"yarn test", app, ClassJest, ""},
		{"pnpm type-check", app, ClassTSC, ""},
		{"yarn tsc --noEmit", app, ClassTSC, ""}, // no such script: yarn runs the bin
		{"npm --prefix " + app + " run lint", "/", ClassESLint, ""},
		{"yarn --cwd " + app + " type-check", "/", ClassTSC, ""},
		{"npm run lint", filepath.Join(app, "src", "deep"), ClassESLint, ""}, // walks up
		{"npm install", app, "", ""},
		{"yarn add -D typescript", app, "", ""},
		{"yarn", app, "", ""},
		{"npm run nope", app, "", ""},
		// workspaces: the member's scripts are not read, the name decides
		{"yarn workspace ui test", lib, ClassJest, ""},
		{"npm run lint -w ui", lib, ClassESLint, ""},
		{"pnpm --filter ui test", lib, ClassJest, ""},
		{"yarn workspace ui dev", lib, "", ""},
		{"yarn workspace ui test:watch", lib, "", ""},
		{"npm run test-storybook -w ui", lib, ClassVitestBrowser, ""},
		{"npm run test-storybook:ui -w ui", lib, "", ""},
		{"npm run graph", lib, "", ""},
		{"npm run build -w ui", lib, ClassBuild, ""},
		{"npm run build -w @globex/ds-ui", lib, ClassBuild, ""},
		{"npm run build:lib --workspace ui", lib, ClassBuild, ""},
		{"npm run build:dev --workspace ui", lib, "", ""}, // longRunRE: dev is a long run
		{"yarn workspace ui build", lib, ClassBuild, ""},
		{"pnpm --filter ui build", lib, ClassBuild, ""},
		{"npm run build:watch -w ui", lib, "", ""},
		{"yarn workspace ui build:dev:watch", lib, "", ""},
		// go test (2026-10-29): heavy = -race, a "..." pattern, or two packages
		{"go test ./internal/scope/... ./internal/space/... ./internal/daemon/...", "", ClassGoTest, ""},
		{"go test -race ./cmd/pigtail/ ./internal/daemon/admission/...", "", ClassGoTest, ""},
		{"go test -race -parallel 4 ./... -count=1", "", ClassGoTest, ""},
		{"go test -race ./internal/foo/", "", ClassGoTest, ""},
		{"go test --race ./internal/foo/", "", ClassGoTest, ""},
		{"go test -race=true ./internal/foo/", "", ClassGoTest, ""},
		{"go test -race=1 ./internal/foo/", "", ClassGoTest, ""},
		{"go test -race=false ./internal/foo/", "", "", ""},
		{"go test -race=0 ./internal/foo/", "", "", ""},
		{"go test ./internal/foo/", "", "", ""},
		{"go test -run TestA/sub -count 1 ./internal/foo/", "", "", ""},
		{"go test -count=1 ./a/ ./b/", "", ClassGoTest, ""},
		{"go test", "", "", ""},
		{"go vet ./...", "", "", ""},
		{"go test ./internal/foo/ -args ./x/... ./y", "", "", ""},
		{"go test -list . ./...", "", "", ""},
		{"go test -h", "", "", ""},
		// make: a target whose recipe runs a heavy go test (fixtures in testdata/gomake)
		{"make check", gm, ClassGoTest, ""},
		{"make test-race", gm, ClassGoTest, ""},
		{"make test", gm, ClassGoTest, ""},
		{"make -j8 check", gm, ClassGoTest, ""},
		{"make -j 8 check", gm, ClassGoTest, ""},
		{"make -j check", gm, ClassGoTest, ""},
		{"make nope check", gm, ClassGoTest, ""},
		{"make split", gm, ClassGoTest, ""},
		{"make vet unit test", gm, ClassGoTest, ""},
		{"make", gm, ClassGoTest, ""},
		{"make stamped", gm, ClassGoTest, ""},
		{"make stampedvet", gm, "", ""},
		{"make unit", gm, "", ""},
		{"make vet", gm, "", ""},
		{"make web", gm, "", ""},
		{"make vuln", gm, "", ""},
		{"make loop", gm, "", ""},
		{"make build", gm, "", ""},
		{"make nope", gm, "", ""},
		{"make HELP", gm, "", ""},
		{"make x", gm, "", ""},
		{"make .PHONY", gm, "", ""},
		{"make -n check", gm, "", ""},
		{"make --dry-run check", gm, "", ""},
		{"make -q check", gm, "", ""},
		{"make -nj4 check", gm, "", ""},
		{"make --help", gm, "", ""},
		{"make -t check", gm, "", ""},
		{"make --touch check", gm, "", ""},
		{"make wrapped", gm, "", NoteNested},
		{"make -W check unit", gm, "", ""},
		{"make -l 8 unit", gm, "", ""},
		{"make -j 8 unit", gm, "", ""},
		{"make -j8 unit", gm, "", ""},
		{"make 8", gm, ClassGoTest, ""},
		{"make cond", gm, ClassGoTest, ""},
		{"make -O unit", gm, "", ""},
		{"make -O check", gm, ClassGoTest, ""},
		{"make -Onone check", gm, ClassGoTest, ""},
		{"make -Otarget check", gm, ClassGoTest, ""},
		{"make -f" + gm + "/Makefile test", "", ClassGoTest, ""},
		{"make inlinenote", gm, ClassGoTest, ""},
		{"make inlinevet", gm, "", ""},
		{"make inlinehash", gm, ClassGoTest, ""},
		{"make -C " + gm + " check", "", ClassGoTest, ""},
		{"make -C" + gm + " check", "", ClassGoTest, ""},
		{"make --directory=" + gm + " check", "", ClassGoTest, ""},
		{"make -f " + gm + "/Makefile test", "", ClassGoTest, ""},
		{"make --file=" + gm + "/Makefile test", "", ClassGoTest, ""},
		{"make check", lib, "", ""},
		{"make t", gnu, ClassGoTest, ""},
		{"make check", root, ClassGoTest, ""},
		{"go test -n ./...", "", "", ""},
		// already routed, opted out
		{"pigtail admit -- tsc", "", "", NoteNested},
		{"pigtail test -- --run", "", "", NoteNested},
		{"PIGTAIL_ADMIT=0 tsc", "", "", NoteOptOut},
	}
	for _, c := range cases {
		segs, reason := lex(c.argv)
		if reason != ReasonNone || len(segs) != 1 {
			t.Fatalf("%q: fixture does not lex to one command", c.argv)
		}
		var argv []string
		for _, w := range segs[0].words {
			argv = append(argv, w.val)
		}
		got, note := Classify(argv, c.dir)
		if got != c.want || note != c.note {
			t.Errorf("Classify(%q, %s) = %q/%q, want %q/%q", c.argv, filepath.Base(c.dir), got, note, c.want, c.note)
		}
	}
}

func TestRewrite(t *testing.T) {
	app := fixture(t, "app")
	lib := fixture(t, "lib")
	const w = 8 * time.Minute
	p := func(c Class) string { return Prefix(c, w) }
	cases := []struct {
		line, cwd string
		want      string
		outcome   Outcome
	}{
		{`npx tsc --noEmit`, "", p(ClassTSC) + `npx tsc --noEmit`, OutcomeRewritten},
		{`npx tsc --noEmit 2>&1 | head -50`, "", p(ClassTSC) + `npx tsc --noEmit 2>&1 | head -50`, OutcomeRewritten},
		{`FOO=1 npx eslint 'src/**/*.ts'`, "", `FOO=1 ` + p(ClassESLint) + `npx eslint 'src/**/*.ts'`, OutcomeRewritten},
		{`yarn lint && yarn type-check`, app,
			p(ClassESLint) + `yarn lint && ` + p(ClassTSC) + `yarn type-check`, OutcomeRewritten},
		{`tsc & eslint . & wait`, "", p(ClassTSC) + `tsc & ` + p(ClassESLint) + `eslint . & wait`, OutcomeRewritten},
		{`cd ` + app + ` && npm run lint`, "/", `cd ` + app + ` && ` + p(ClassESLint) + `npm run lint`, OutcomeRewritten},
		{`cd testdata/app && npm run lint`, filepath.Dir(filepath.Dir(app)), `cd testdata/app && ` + p(ClassESLint) + `npm run lint`, OutcomeRewritten},
		{`time npx tsc`, "", `time ` + p(ClassTSC) + `npx tsc`, OutcomeRewritten},
		{`git status && ls`, "", `git status && ls`, OutcomePass},
		{`grep -rn tsc src`, "", `grep -rn tsc src`, OutcomePass},
		{`echo $(npx tsc)`, "", `echo $(npx tsc)`, OutcomeUnparsed},
		{`bash -c 'tsc'`, "", `bash -c 'tsc'`, OutcomeUnparsed},
		{`pigtail admit -- npx tsc`, "", `pigtail admit -- npx tsc`, OutcomeNested},
		{`PIGTAIL_ADMIT=0 npx tsc`, "", `PIGTAIL_ADMIT=0 npx tsc`, OutcomeOptOut},
		{`npm run build -w @globex/ds-ui`, lib, p(ClassBuild) + `npm run build -w @globex/ds-ui`, OutcomeRewritten},
		{`go test -race ./...`, "", p(ClassGoTest) + `go test -race ./...`, OutcomeRewritten},
		{`go test ./internal/foo/`, "", `go test ./internal/foo/`, OutcomePass},
		{`make check`, fixture(t, "gomake"), p(ClassGoTest) + `make check`, OutcomeRewritten},
	}
	for _, c := range cases {
		got := Rewrite(c.line, c.cwd, w)
		if got.Line != c.want || got.Outcome != c.outcome {
			t.Errorf("Rewrite(%q):\n got  %q (%s)\n want %q (%s)", c.line, got.Line, got.Outcome, c.want, c.outcome)
		}
	}
}

func TestRewriteIsIdempotent(t *testing.T) {
	once := Rewrite(`npx tsc && eslint .`, "", time.Minute)
	twice := Rewrite(once.Line, "", time.Minute)
	if twice.Line != once.Line || twice.Outcome != OutcomeNested {
		t.Fatalf("a rewritten line must not be rewritten again: %q → %q (%s)", once.Line, twice.Line, twice.Outcome)
	}
}

func TestPrefixWithoutWait(t *testing.T) {
	if got := Prefix(ClassJest, 0); got != "pigtail admit --class=jest -- " {
		t.Fatalf("got %q", got)
	}
}

func TestHeavier(t *testing.T) {
	if Heavier("", ClassTSC) != ClassTSC || Heavier(ClassTSC, "") != ClassTSC {
		t.Fatal("the empty class must lose")
	}
	if Heavier(ClassTSC, ClassJest) != ClassJest || Heavier(ClassJest, ClassTSC) != ClassJest {
		t.Fatal("the costlier class must win")
	}
	if Prior("unknown") != Prior(ClassGeneric) || Known("unknown") || !Known(ClassNx) {
		t.Fatal("unknown classes cost what generic does")
	}
}

func TestBrowserRunCostScalesWithWorkers(t *testing.T) {
	if got, want := BrowserRunCost(0), Prior(ClassVitestBrowser); got != want {
		t.Fatalf("an unknown worker count must fall back to the class prior: got %d want %d", got, want)
	}
	two, eight := BrowserRunCost(2), BrowserRunCost(8)
	if two >= eight {
		t.Fatalf("cost must grow with workers: 2 → %d, 8 → %d", two, eight)
	}
	if two < Prior(ClassVitest) {
		t.Fatalf("a browser run must never be reserved less than a node one: %d", two)
	}
}

func TestGoTestClass(t *testing.T) {
	if !Known(ClassGoTest) || ClassGoTest != "go-test" {
		t.Fatalf("go-test must be a known class, got %q known=%v", ClassGoTest, Known(ClassGoTest))
	}
	if got := Prior(ClassGoTest); got != 3*gib {
		t.Fatalf("Prior(go-test) = %d, want 3 GiB", got)
	}
	// Inferred for a heavy suite (-race, "...", several packages); a targeted
	// single-package run stays unclassified.
	if c, _ := Classify([]string{"go", "test", "-race", "./..."}, t.TempDir()); c != ClassGoTest {
		t.Fatalf("go test -race ./... must be go-test, got %q", c)
	}
}

func TestBuildPrior(t *testing.T) {
	if !Known(ClassBuild) || Prior(ClassBuild) != 8*gib {
		t.Fatalf("build prior = %d, want 8 GiB", Prior(ClassBuild))
	}
	if Heavier(ClassTSC, ClassBuild) != ClassBuild {
		t.Fatal("a script that type-checks then builds is a build")
	}
}

func TestCalmClasses(t *testing.T) {
	if got, want := CalmClasses(), []Class{ClassBuild, ClassVitestBrowser}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CalmClasses() = %v, want %v — a prior crossing 4 GiB moves a class in or out of the calm rule", got, want)
	}
}
