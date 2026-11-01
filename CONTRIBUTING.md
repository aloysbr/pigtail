# Contributing to pigtail

Thanks for considering a contribution. pigtail is an early-alpha OSS Go
project — expect rough edges, moving APIs, and the occasional rewrite.
Small commits, a hardening reflex and abstraction discipline apply to
*every* change.

## Conventional Commits

Every commit message **must** follow [Conventional Commits](https://www.conventionalcommits.org/).
The `release-please` workflow parses commit subjects on `main` to compute
the next semver bump and generate `CHANGELOG.md` entries — non-conforming
commits are silently ignored.

Format:

```
<type>(<scope>): <subject>

<body, optional, wrapped at 72 cols>
```

Allowed **types** (the only ones release-please considers):

| Type | When to use | Bumps |
|---|---|---|
| `feat` | New user-visible capability | minor |
| `fix` | Bug fix in shipped behavior | patch |
| `refactor` | Internal change, no behavior delta | patch |
| `docs` | Docs-only change | none (hidden) |
| `chore` | Maintenance, deps, no behavior delta | none (hidden) |
| `test` | Test-only addition or change | none (hidden) |
| `ci` | CI / workflow / build tooling | none (hidden) |

Use `feat!:` or include a `BREAKING CHANGE:` footer to force a major bump
(while we're still pre-1.0, breaking changes still bump the *minor*).

**Scope** is optional but encouraged for any non-trivial surface: `cli`,
`daemon`, `tui`, `backend`, `delivery`, `wire`, `config`, etc. Use the
filename or package when nothing better fits.

Examples:

```
feat(reply): pigtail reply — forward input to an agent without tmux attach
fix(daemon): release port on workspace cleanup failure
docs(readme): rewrite README for pigtail (was wt-manager)
ci(release): bootstrap release-please for automated CHANGELOG
```

## Code style

Go code:

- `gofmt` / `goimports` clean.
- `go vet ./...` and `golangci-lint run ./...` clean (run both before pushing).
- Public types and functions documented with full sentences.
- Honor the architecture rules in `docs/architecture.md` §8 — no direct
  `exec.Command("git", …)` outside `internal/backend/git`, no implicit
  `BackendData["branch"]` access, no "PR" in user-facing strings.

There is no public CI yet: run `make check` locally before pushing. The core of it:

```bash
go vet ./...
go test -race -count=1 ./...
golangci-lint run ./...
```

## Pull requests

- One concern per PR. Split tooling, refactors, and features into
  separate commits (or PRs), so a revert undoes one layer only.
- Use the PR template. There is no public CI yet: run `make check` before
  asking for a merge and tick its box.
- **Do not** edit `CHANGELOG.md` by hand — `release-please` regenerates
  it from commit messages on every merge to `main`.

## Tests

Tests live alongside the code (`*_test.go`). Run with
`go test -race -count=1 ./...`. Each new exported function or wire
endpoint needs at least one test exercising the happy path.

## Checks before you push

Everything the project verifies runs locally, in one target:

```bash
make tools    # once: installs govulncheck (golangci-lint via brew)
make hooks    # once per clone: runs `make check-affected` (no -race) on every push, two at a time on the machine (pigtail admit)
make check    # the full gate; expect several minutes
```

`make check` is `go vet`, `go test -race`, `golangci-lint`, `govulncheck`,
and a Linux cross-build. Three of those deserve a word:

- **`-race` is not optional here.** `make test` runs without it, and this is
  a daemon made of goroutines, mutexes and concurrent sweeps — the race
  detector is the check most likely to catch a real defect, and a plain
  `go test` skips it silently.
- **The Linux cross-build** compiles `rammon/mem_linux.go`,
  `proccpu/sample_linux.go` and `powersrc/powersrc_linux.go`, which a macOS
  machine never builds. It compiles them; running their tests needs an actual
  Linux box.
- **`govulncheck` reports on the toolchain you build with**, not on a
  pristine CI image. If it flags standard-library advisories, your local Go
  is behind — upgrade it rather than skipping the check. That is the finding
  a CI runner on `go-version: stable` structurally cannot give you.

**If `lint` fails inside the Go standard library, update golangci-lint.** The
linter type-checks with the Go version it was itself built against, so a
toolchain upgrade breaks an older one — and the failure names *your* toolchain,
not the linter, which reads like the upgrade broke the language:

```
/opt/homebrew/Cellar/go/1.27.0/libexec/src/.../randutil.go:21:5: undefined: rand (typecheck)
```

Nothing is wrong with the code. `make tools` prints the installed version
against `GOLANGCI_VERSION` for exactly this reason — note it when a Go upgrade
and a lint failure arrive together. Homebrew may decline a plain `brew upgrade`
here; `brew reinstall golangci-lint` takes it.

`git push --no-verify` skips the hook when you need it to.
