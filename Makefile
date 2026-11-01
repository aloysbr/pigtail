BIN_DIR := bin
PIGTAIL  := $(BIN_DIR)/pigtail
PIGTAILD := $(BIN_DIR)/pigtaild

# Version stamped into the binaries. `git describe` is the source of truth: on
# a release tag it reads v0.2.0, off one it reads v0.2.0-3-gabc1234, so a
# binary always says where it came from. Falls back to "dev" outside a git
# checkout (a source tarball). Override with `make build VERSION=...`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Build identity, stamped into both binaries (internal/buildinfo): the branch,
# the full commit and whether tracked files were modified — the same scope as
# describe's --dirty. `pigtaild -version`, `pigtail version [--daemon]` and
# GET /version print them, so post-merge knows what it is about to replace.
# Outside git all three stay empty (read unknown), never DIRTY=true.
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
DIRTY  ?= $(shell git rev-parse --git-dir >/dev/null 2>&1 && { git diff --quiet HEAD && echo false || echo true; })

# The daemon carries its version twice: main.Version for the line it prints at
# startup, and internal/daemon.Version for what it serves on /version. The
# client compares the two (cmd/pigtail/rename.go) to warn about a daemon older
# than the CLI — stamping one and not the other would make that warning fire on
# a mismatch it invented itself. The client does not import internal/daemon, so
# it gets the shorter set rather than a -X against a symbol it lacks. The
# build identity (internal/buildinfo) goes in the client set, so both get it.
LDFLAGS_CLIENT := -X main.Version=$(VERSION) -X github.com/aloysbr/pigtail/internal/buildinfo.Branch=$(BRANCH) -X github.com/aloysbr/pigtail/internal/buildinfo.Commit=$(COMMIT) -X github.com/aloysbr/pigtail/internal/buildinfo.Dirty=$(DIRTY)
LDFLAGS_DAEMON := $(LDFLAGS_CLIENT) -X github.com/aloysbr/pigtail/internal/daemon.Version=$(VERSION)

.PHONY: all build test lint clean install uninstall app \
        check vet test-race vuln build-linux test-node sdkhost test-sdkhost test-swift test-hooks tools hooks \
        print-buildinfo web web-check test-web check-steps check-push-steps test-push check-affected

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS_CLIENT)" -o $(PIGTAIL)  ./cmd/pigtail
	go build -ldflags "$(LDFLAGS_DAEMON)" -o $(PIGTAILD) ./cmd/pigtaild

# The build identity make would stamp (TestMakefileOutsideGitLeavesFieldsUnknown).
print-buildinfo:
	@echo BRANCH=$(BRANCH) COMMIT=$(COMMIT) DIRTY=$(DIRTY)

test:
	go test ./...

lint:
	golangci-lint run ./...

# ---------------------------------------------------------------------------
# check — everything CI used to verify, on your machine.
#
# `make test` is not enough on its own: it runs without -race, and this is a
# daemon built out of goroutines, mutexes and concurrent sweeps. The race
# detector is the single most valuable check here, and it is the one a plain
# `go test` silently skips.
#
# Measured 2026-10-27: ~12-13 min end to end, test-race 87 % of it. The
# pre-push hook (`make hooks`, once per clone) therefore runs check-affected:
# the packages the push touches and their reverse dependencies, vet, lint and
# the non-Go targets touched — the whole check on any doubt. Run the full
# `make check` before a merge: green on a clean tree, it stamps its sha
# (scripts/check-stamp.sh verify <sha>). `git push --no-verify` skips the hook.
# ---------------------------------------------------------------------------

# Pinned so the linter is a fixed point rather than whatever brew last
# installed. `make tools` reports a mismatch rather than failing on it.
GOLANGCI_VERSION := v2.13.1

# Resolve go-installed tools by path, not by PATH. `go install` drops binaries
# in GOBIN (or GOPATH/bin), which is commonly absent from the PATH — measured
# here: govulncheck installed fine and stayed invisible to `command -v`, which
# would have sent `make check` into a loop telling you to run `make tools`
# again. A git hook inherits whatever PATH the caller had, so leaning on it is
# doubly fragile.
GOBIN_DIR := $(shell go env GOBIN)
ifeq ($(strip $(GOBIN_DIR)),)
GOBIN_DIR := $(shell go env GOPATH)/bin
endif
GOVULNCHECK := $(shell command -v govulncheck 2>/dev/null || echo $(GOBIN_DIR)/govulncheck)

# A green `make check` on a clean tree stamps the sha it verified
# (scripts/check-stamp.sh: `verify <sha>` reads it back without re-running).
check:
	@sh scripts/check-stamp.sh run -- $(MAKE) --no-print-directory check-steps

# The steps both runs share, so the pre-push fallback cannot drift from make check.
CHECK_COMMON := lint vuln build-linux test-node test-sdkhost test-swift test-hooks web-check test-web

check-steps: vet test-race $(CHECK_COMMON)
	@echo "✓ check passed"

# The pre-push fallback (scripts/check-affected.sh) on any doubt: make check
# without -race (test-push), and it never stamps — only a full make check does.
check-push-steps: vet test-push $(CHECK_COMMON)
	@echo "✓ check-push passed"

# What the pre-push hook runs: the packages and targets the pushed files
# touch (PREPUSH_FILES lists them; scripts/check-affected.sh) without -race, or
# check-push-steps (make check minus -race and the stamp) on any doubt.
check-affected:
	@sh scripts/check-affected.sh "$(PREPUSH_FILES)"

# The pre-push hook's argv (admitted, fallback, delete-only), against fakes.
test-hooks:
	sh scripts/pre-push.test.sh
	sh scripts/check-affected.test.sh
	sh scripts/web-size.test.sh
	sh scripts/macapp-makefile.test.sh
	sh scripts/check-stamp.test.sh
	sh scripts/flake.test.sh
	bash scripts/app-install.test.sh

vet:
	go vet ./...

# Three package binaries at a time, 20 min per package instead of the 10 min default they
# sat under. -parallel 4: the isolated tests of cmuxsync, handlers and cmd/pigtail run in
# parallel (the others stay serial, see testutil.SerialOnly). Measured 2026-10-28
# (go test -race -count=1 -p 6 -json ./...): 602 s before the parallel tests, 420 s with
# -parallel 4, 426 s with -parallel 8. Measured 2026-10-30 (sha fcd9c9bd): -race -p 6 =
# 629 s at 4.7 cores on average, cmd/pigtail (605 s) the critical path; without -race
# 248 s and CPU / 4.4. Hence -p 3: -p 6 bought no wall time, only load. -race runs here
# only (the gate before a merge); the pre-push uses test-push. It needs
# clients/sdkhost's node_modules (TestFakeClaudeSeesCleanEnv skips without
# them), hence the sdkhost prerequisite.
test-race: sdkhost
	go test -race -count=1 -p 3 -parallel 4 -timeout 20m ./...

# The pre-push fallback's test run: the same without -race (2026-10-30: 248 s).
test-push:
	go test -count=1 -p 3 -parallel 4 -timeout 20m ./...

vuln:
	@test -x "$(GOVULNCHECK)" || { \
		echo "govulncheck not found at $(GOVULNCHECK) — run 'make tools'"; exit 1; }
	"$(GOVULNCHECK)" ./...

# The three *_linux.go files (rammon/mem, proccpu/sample, powersrc) are never
# compiled on a darwin machine, so nothing local notices when one of them
# breaks. Cross-compiling costs ~10s and catches exactly that. It compiles
# them; only CI could ever *run* their tests, which is the one thing dropping
# GitHub Actions genuinely gives up.
build-linux:
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=amd64 go vet ./...

# The Node client has no npm dependency and no test runner: `node --test` is
# the runner. A bare directory argument does not recurse on Node 22.16 here
# (only the no-args cwd scan does), so this globs explicitly. Skipped rather
# than failed when node is absent — a Go-only machine can still verify Go.
# One recipe line on purpose: each Makefile line is its own shell (no
# .ONESHELL here), so a skip-and-exit-0 on its own line only ends that
# line's shell and the next line still runs unconditionally regardless.
# slots.mjs is syntax-checked too: it is the one file consumers actually
# load, and nothing else here parses it (the glob below only matches
# *.test.mjs, and the Go test only substring-checks the embedded bytes).
test-node:
	@command -v node >/dev/null || { echo "node not found — skipping clients/node tests"; exit 0; }; node --check clients/node/slots.mjs && node --test clients/node/*.test.mjs

# The SDK session host (clients/sdkhost, TypeScript on Node). `make sdkhost`
# installs its pinned dependencies; pigtaild's -sdk-host points at this
# directory, so run it at post-merge, before restarting the daemon. One recipe
# line each, like test-node (a skip-and-exit-0 must not protect the next line).
sdkhost:
	@command -v node >/dev/null || { echo "node not found — skipping the sdkhost install"; exit 0; }; cd clients/sdkhost && npm ci

# Runs npm ci itself: a fresh checkout has no node_modules.
test-sdkhost:
	@command -v node >/dev/null || { echo "node not found — skipping clients/sdkhost tests"; exit 0; }; cd clients/sdkhost && npm ci --silent && node --test --experimental-strip-types *.test.ts

# The web front (web/, Vite). Its build is committed in
# internal/webui/dist (go:embed cannot reach web/), stamped with the hash of
# the sources it came from (webui.SourcesHash, the one implementation).
# `make web` skips without node or without web/ (a Go-only machine, or
# before the front exists); one recipe line each, like test-node.
# It ends on the bundle budget (scripts/web-size.sh, <= 500 KB gzip).
web:
	@command -v node >/dev/null || { echo "node not found — skipping the web front build"; exit 0; }; test -f web/package.json || { echo "no web/package.json — nothing to build"; exit 0; }; (cd web && npm ci && npm run build) && rm -rf internal/webui/dist && mkdir -p internal/webui/dist && cp -R web/dist/. internal/webui/dist/ && go run ./internal/webui/cmd/webhash -write internal/webui/dist/SOURCES.sha256 && sh scripts/web-size.sh

# Needs no node: compares the committed stamp with the sources' hash.
web-check:
	@go run ./internal/webui/cmd/webhash -check internal/webui/dist/SOURCES.sha256 || { echo "web-check: run \`make web\` then commit internal/webui/dist"; exit 1; }

test-web:
	@command -v node >/dev/null || { echo "node not found — skipping web tests"; exit 0; }; test -f web/package.json || { echo "no web/package.json — skipping web tests"; exit 0; }; cd web && npm ci && npm test

# Guards the Swift package under macapp/ — the Pigtail app — with the one
# gate it actually has. The Go workflow runs on ubuntu-latest and never
# touches macapp/, so `swift test` has never run in CI. It is not an oversight
# left to fix later: this repo has no branch protection, so a red
# CI job would only ever be informative, never a merge gate. The gate that
# actually blocks a bad push is `.githooks/pre-push` (wired to `make check`
# via `make hooks`), so that is where this has to live.
#
# Measured, not estimated: 110 tests, 0 failures (2026-09-11's original
# baseline was 58, before this slice's read path and this task's retry
# button both added their own suites — recount before trusting this number
# stale a second time). 17s cold (first `swift build` of the package,
# before its build cache exists); ~1s warm — incremental, the same order of
# magnitude as `make vet` above. Every push that touches only Go still pays
# that ~1s; that is not a real cost.
#
# What this does NOT catch: `git push --no-verify` skips the hook (and this
# target) entirely, and a machine with no Swift toolchain — or not darwin —
# skips silently below rather than failing. Same guarantee already accepted
# for this directory: local-only gates, nothing more.
#
# One recipe line on purpose, same reason as test-node above: each Makefile
# line is its own shell, so a skip-and-exit-0 on its own line would not
# protect the line after it.
test-swift:
	@{ [ "$$(uname -s)" = "Darwin" ] && command -v swift >/dev/null 2>&1; } || { echo "not darwin or swift toolchain not found — skipping macapp Swift tests"; exit 0; }; swift test --package-path macapp

tools:
	go install golang.org/x/vuln/cmd/govulncheck@latest
	@test -x "$(GOVULNCHECK)" || { \
		echo "installed govulncheck but it is not at $(GOVULNCHECK)"; exit 1; }
	@command -v golangci-lint >/dev/null || { \
		echo "golangci-lint not found — install it (brew install golangci-lint)"; exit 1; }
	@have=$$(golangci-lint version --short 2>/dev/null || echo unknown); \
	 want=$$(echo $(GOLANGCI_VERSION) | sed 's/^v//'); \
	 [ "$$have" = "$$want" ] || echo "note: golangci-lint $$have installed, CI pinned $$want"
	@echo "✓ tools ready"

# core.hooksPath is repo-wide, so every worktree of this clone gets the hook.
# .githooks/ is versioned, unlike .git/hooks — that is the whole point.
hooks:
	git config core.hooksPath .githooks
	@echo "✓ hooks enabled (git config core.hooksPath .githooks)"

clean:
	rm -rf $(BIN_DIR)
	rm -rf macapp/.build macapp/Pigtail.app

PREFIX ?= $(HOME)/.local

# Code-signing identity for everything `install` and `app` deploy.
#
# Why not ad-hoc (`codesign -s -`): an ad-hoc signature's designated
# requirement is the binary's cdhash, so every rebuild is a new program to
# macOS. TCC then forgets what the user granted — pigtaild reaches a backup host
# over SSH for the offsite backup, which needs Local Network access, and the
# "Allow pigtaild to find devices on local networks?" prompt came back after
# every `make install`. Signed with a stable certificate and a fixed
# identifier, the requirement becomes `identifier "…" and certificate leaf =
# H"…"`, which survives rebuilds: one last Allow, then none.
#
# SIGN_CERT names a code-signing identity in the login keychain (a
# self-signed one is enough). Where it is missing — another machine, a
# fresh keychain — the build falls back to ad-hoc rather than failing.
# Override either: `make install SIGN_CERT=other` or `SIGN_IDENTITY=-`.
# Empty by default (ad-hoc signing); set your own in Makefile.local
# (gitignored), e.g. `SIGN_CERT = my-signing-cert` and
# `SIGN_ID_PREFIX = com.example`, so every `make install` keeps them.
-include Makefile.local
SIGN_CERT ?=
SIGN_IDENTITY ?= $(if $(SIGN_CERT),$(shell security find-identity -v -p codesigning 2>/dev/null | grep -qF '"$(SIGN_CERT)"' && echo '$(SIGN_CERT)' || echo -),-)
SIGN_ID_PREFIX ?= sh.pigtail

install: build
	install -d $(PREFIX)/bin
	@# Atomic install: stage as <name>.new, sign the staged file, then rename
	@# it over the final name. Never write over the installed binary in place.
	@#
	@# Why: macOS caches a binary's code signature per vnode (inode) the first
	@# time it is executed. `install`/`cp` onto an existing path truncates and
	@# rewrites the SAME inode, so the kernel keeps validating the new pages
	@# against the old cached signature: dyld hits an "Invalid Page" and the
	@# process is SIGKILLed with "Code Signature Invalid" (exit 137, crash
	@# report namespace CODESIGNING). Seen on pigtaild in July (crash reports
	@# pigtaild-2026-07-{22,23}: CODESIGNING SIGKILL at launch — launchd
	@# KeepAlive masked it, a shell-spawned pigtaild died outright) and on
	@# `pigtail new` in September. Re-signing the destination in place, the
	@# former workaround, only narrowed the window: the inode stayed the same.
	@# `mv` within one directory is a rename(2): the final name points at a
	@# fresh inode with no stale cache, and a running process keeps its old one.
	@#
	@# codesign re-signs through the system tool with SIGN_IDENTITY (see
	@# SIGN_CERT above), replacing the Go linker's `linker-signed` signature.
	@# Skipped where codesign does not exist (Linux).
	@echo "signing with: $(SIGN_IDENTITY)"
	@set -e; for name in pigtail pigtaild; do \
	  src=$(BIN_DIR)/$$name; dst=$(PREFIX)/bin/$$name; \
	  rm -f "$$dst.new"; \
	  install -m 755 "$$src" "$$dst.new"; \
	  if command -v codesign >/dev/null 2>&1; then codesign -s "$(SIGN_IDENTITY)" -i "$(SIGN_ID_PREFIX).$$name" --force "$$dst.new"; fi; \
	  mv -f "$$dst.new" "$$dst"; \
	  echo "installed $$dst"; \
	done

uninstall:
	rm -f $(PREFIX)/bin/pigtail $(PREFIX)/bin/pigtaild \
	      $(PREFIX)/bin/pigtail.new $(PREFIX)/bin/pigtaild.new

MACAPP_DIR := macapp
MACAPP_BUILD := $(MACAPP_DIR)/.build/release/Pigtail
MACAPP_APP := $(MACAPP_DIR)/Pigtail.app
# Emplacement stable de déploiement. SMAppService (launch-at-login) indexe
# l'enregistrement sur le chemin du bundle : /Applications reste fixe entre
# deux rebuilds, contrairement à un bundle sous macapp/ (ré-approbation à
# chaque build). Surchargeable : `make app APP_DEST=...`.
APP_DEST ?= /Applications/Pigtail.app
# A bundle id other than sh.pigtail.app is a verify build: it never asks for
# notifications, never terminates the retired apps and keeps its own defaults.
APP_BUNDLE_ID ?= sh.pigtail.app
# Where scripts/app-install.sh looks for the two retired apps, and where it
# puts them. Passed explicitly on the script's command line, never left to
# make's implicit export.
PIGTAIL_APPS_DIR ?= /Applications
PIGTAIL_TRASH_DIR ?= $(HOME)/.Trash

# Guard of the app target: it copies exactly the binary, Info.plist and its
# icons (versioned in macapp/Resources/, copied here, never by SwiftPM) into
# the .app. There is no SwiftPM Resources/ step. A SwiftPM resource would
# therefore be built into .build/release/<Package>_<Target>.bundle and never
# copied, Bundle.module (the generated accessor) would fatalError at its first
# access -- a crash at launch, with no window to show it when Pigtail starts
# as a menu bar app. Fail here instead, before anything is deployed. Matches
# .bundle *directories* at the top of release/ only: stale dependency files
# from older builds litter .build with names a looser match would pick up.
# $(1): the calling target, $(2): its .app, $(3): its install variable.
define swiftpm_bundle_guard
bundles=$$(find -L "$(MACAPP_DIR)/.build/release" -maxdepth 1 -type d -name '*.bundle' 2>/dev/null); \
if [ -n "$$bundles" ]; then \
  echo "error: SwiftPM built a resource bundle that nothing copies into the .app:"; \
  echo "$$bundles" | sed 's/^/  /'; \
  echo "The $(1) target copies only the binary, Info.plist and its icons, so"; \
  echo "Bundle.module would fatalError on first access, a crash at launch."; \
  echo "Fix it one of two ways:"; \
  echo "  - copy the bundle into $(2)/Contents/Resources/ here, and"; \
  echo "    into \$$$(3) with it; or"; \
  echo "  - drop the SwiftPM resource and build the asset in code"; \
  echo "    (see Sources/MenuBarCore/StatusItemImage.swift)."; \
  exit 1; \
fi
endef

# Pigtail: one app, a main window and a menu bar. Its two icons are required
# prerequisites: AppIcon.icns (Océan) is the bundle's icon (Finder,
# notifications, Login Items), AppIconTerra.icns rides along and the app shows
# it in the Dock while the web front is in Terra.
APP_ICON ?= $(MACAPP_DIR)/Resources/AppIcon.icns
APP_TERRA_ICON ?= $(MACAPP_DIR)/Resources/AppIconTerra.icns

app: $(APP_ICON) $(APP_TERRA_ICON)
	swift build --package-path $(MACAPP_DIR) -c release --product Pigtail
	@$(call swiftpm_bundle_guard,app,$(MACAPP_APP),APP_DEST)
	@# Former build outputs (regenerable, ignored by git): LaunchServices would keep two more copies.
	rm -rf "$(MACAPP_DIR)/PigtailBar.app" "$(MACAPP_DIR)/Pigtail Cockpit.app"
	rm -rf "$(MACAPP_APP)"
	mkdir -p "$(MACAPP_APP)/Contents/MacOS" "$(MACAPP_APP)/Contents/Resources"
	cp "$(MACAPP_BUILD)" "$(MACAPP_APP)/Contents/MacOS/Pigtail"
	cp "$(MACAPP_DIR)/Info.plist" "$(MACAPP_APP)/Contents/Info.plist"
	cp "$(APP_ICON)" "$(MACAPP_APP)/Contents/Resources/AppIcon.icns"
	cp "$(APP_TERRA_ICON)" "$(MACAPP_APP)/Contents/Resources/AppIconTerra.icns"
	$(if $(filter-out sh.pigtail.app,$(APP_BUNDLE_ID)),/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier $(APP_BUNDLE_ID)" "$(MACAPP_APP)/Contents/Info.plist")
	codesign --force --sign "$(SIGN_IDENTITY)" "$(MACAPP_APP)"
	@echo "built $(MACAPP_APP)"
	@# Staged install: copy and sign beside the destination (a .new left by a
	@# failed run would install a stale build), then app-install.sh swaps it in.
	@# APP_DEST's parent may not exist yet (never /Applications, which always does).
	[ -d "$(dir $(APP_DEST))" ] || mkdir -p "$(dir $(APP_DEST))"
	rm -rf "$(APP_DEST).new"
	cp -R "$(MACAPP_APP)" "$(APP_DEST).new"
	codesign --force --sign "$(SIGN_IDENTITY)" "$(APP_DEST).new"
	PIGTAIL_APPS_DIR="$(PIGTAIL_APPS_DIR)" PIGTAIL_TRASH_DIR="$(PIGTAIL_TRASH_DIR)" scripts/app-install.sh "$(APP_DEST).new" "$(APP_DEST)"
