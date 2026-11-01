# `pigtail.yaml` and `claude-cells.yaml` reference

This file lives at the root of the project container and should be
committed. `pigtail new` reads it to allocate ports and run hooks.

`pigtail init` writes a minimal one. The common keys are `ports`,
`servers`, `hooks`, `defaults` and `watchdog`:

```yaml
version: 1
name: myapp
backend: git              # only "git" implemented in v1
ports:
  dev:       [3000, 3099]
  storybook: [6006, 6099]

# Runaway dev-server watchdog (optional; these are the defaults).
# The daemon kills a dev server only when its process subtree sustains
# cpu_threshold %CPU across confirm_windows sampling windows; an idle
# server or a short HMR recompile is never touched. Disable globally
# with `pigtaild --watchdog=false`.
watchdog:
  enabled: true
  cpu_threshold: 400      # sustained %CPU (100 = one core)
  sample_interval: 6      # seconds per sampling window
  confirm_windows: 2      # hot windows required before a kill
  confirm_gap: 10         # seconds between confirmation windows
  sweep_interval: 30      # seconds between sweeps of all workspaces
```

The sections below document the rest.

> Earlier versions of this page documented `wt-manager.json`, the config
> format of the bash CLI. Those scripts were removed from the tree in
> July 2026 (archived at tag `legacy-bash-v0.3.0`) and nothing reads that
> format any more. `pigtail migrate` still *relocates* a leftover
> `wt-manager.json` when it converts a project to the container layout,
> but it never parses it.

## Port ranges are per-project — static servers are not

The `dev`/`storybook` port ranges are **per-project**, set in this file.
Static servers (`pigtail serve`) are not project-scoped — they allocate
from a **daemon-global** range set on `pigtaild` itself, not here:
`pigtaild --static-port-range <min>-<max>` (default `8080-8099`). Static
ports are folded into the shared used-port set, so they never collide
with any project's worktree ports.

## `hooks` — outcome, refusal, re-run

The post-create sequence runs on the daemon, bounded at 10 min for all the
hooks together, independent of the client: a `pigtail new` or `session spawn
--new-worktree` that gives up (timeout, Ctrl-C) no longer stops it. Its
outcome is recorded on the workspace and shown by `pigtail list` (`⚠hooks:<hook>`
on a failure, `…hooks` while they run) and `pigtail list --json` (`hooks`);
the output goes to the worktree's `hooks.log`. `pigtail session spawn` refuses a
worktree whose hooks failed — fix the cause, then `pigtail hooks run <id>`, or
remove it with `pigtail clean <id>` and create it again — and refuses one whose
hooks still run as retryable. `pigtail hooks run [workspace_id]` re-runs the
whole sequence through the daemon, with the same budget, and records the new
outcome.

## `profiles`

Per-Claude-account (or per-agent-identity) config for dispatched agents. Each
entry is a map keyed by a **profile id** (the key is used as the id unless the
entry sets its own `id` field).

| Field | Type | Description |
|---|---|---|
| `name` | string | Optional human-readable label (display only). |
| `agent_kind` | string | Optional agent kind override (e.g. `"claude"`). Defaults to the project default when omitted. |
| `config_dir` | string | Sets `CLAUDE_CONFIG_DIR` for the spawned agent — selects which Claude account / config directory it runs under. |
| `env_overrides` | map | Extra environment variables injected into the agent process (e.g. to pick a specific model). |

**Selection:** pass `--profile <id>` to `pigtail dispatch`, or set
`default_profile_id` in the project's `pigtail.yaml` to apply a profile
automatically when none is specified at the call site.

```yaml
profiles:
  work:
    name: Work account
    config_dir: ~/.claude-work
    env_overrides:
      ANTHROPIC_MODEL: claude-opus-4-8
  personal:
    name: Personal account
    agent_kind: claude
    config_dir: ~/.claude
```

## `claude-cells.yaml`

Unlike `pigtail.yaml`, this table is per machine: it lives in the pigtail
config dir (`~/.config/pigtail/claude-cells.yaml`, or
`$XDG_CONFIG_HOME/pigtail/`). It declares the Claude **cells** pigtail may
launch, observe and resume sessions on: one cell per Claude account (a
`CLAUDE_CONFIG_DIR`) and scope (`scopes.yaml`, built-in fallback: one scope,
`personal`). Commands that read it (`pigtail session spawn`, `respawn`,
`usage`, …) refuse with a message pointing here when it is missing or lists
no cell.

```yaml
cells:
  main:                          # cell id: [a-z]+
    config_dir: ~/.claude-pigtail  # the account's CLAUDE_CONFIG_DIR
    scope: personal              # a scope of scopes.yaml
    cwd_home: ~/code             # optional: the directories this cell's scope owns
    select_pct: 80               # optional usage thresholds (percent);
    switch_pct: 90               #   unset = pigtail's defaults
```

`cwd_home` is needed only with several scopes: `pigtail session spawn --cwd`
uses the longest `cwd_home` containing a directory to tell which scope it
belongs to.

### One account (recommended for the alpha)

1. Give the account its own config dir and log in once:
   `CLAUDE_CONFIG_DIR=~/.claude-pigtail claude`, then `/login`.
2. Declare it: `cells: { main: { config_dir: ~/.claude-pigtail, scope: personal } }`
   (`personal` is the built-in master scope; use yours if you wrote a `scopes.yaml`).
3. Start the Claude sessions you drive pigtail from on that dir too — e.g.
   `export CLAUDE_CONFIG_DIR=~/.claude-pigtail` in your `~/.zshrc`. A command run from
   a session's Bash tool (`spawn`, `respawn`, `attach`, …) finds its cell by the calling
   Claude's `CLAUDE_CONFIG_DIR`; a `claude` started without it belongs to no cell and is
   refused.

Why not `~/.claude`: with `CLAUDE_CONFIG_DIR=~/.claude`, Claude Code reads its
state from `~/.claude/.claude.json` instead of `~/.claude.json`, so the login
of a default install is not found and you must log in again. A dedicated dir
avoids touching the default install.

**Several accounts:** one cell per account, each with its own `config_dir`.
Do **not** export `CLAUDE_CONFIG_DIR` in your shell rc then: pigtail starts each
new session's tab with the cell's `CLAUDE_CONFIG_DIR`, and an export in the rc
overrides it — the spawn then sees a Claude on the wrong account and fails
loudly (`has CLAUDE_CONFIG_DIR=…, not …`).

### Without the `c` launcher

pigtail works with a plain shell. When the tab's shell has no `c` launcher
(`C_LAUNCH_PROTO` unset or below 1), `pigtail session spawn`, pigtaild's web
launch and `pigtail session respawn` cd into the session's directory and run
`claude` themselves — cmux's `cmux-claude-wrapper` (next to
`$CMUX_BUNDLED_CLI_PATH`) when the shell is in cmux, so the session stays bound
to its tab, else the `claude` on `PATH`. A missing directory prints one line
and launches nothing. The resume hint of a failed respawn reads
`CLAUDE_CONFIG_DIR=<dir> claude --resume <session id>`.

## `settings.yaml` — your own commands

pigtail ships no slash command of its own and, by default, never names or
types one. If your Claude setup has commands of its own around a session's
end, declare them in `settings.yaml` (pigtail's config dir, next to
`claude-cells.yaml`; the file's other keys, `human.name` and
`launch.prompt_template`, are in [EXTENDING.md](EXTENDING.md#settingsyaml)).
Every key is optional; restart pigtaild after a change.

```yaml
session:
  done_command: /finish          # your command that closes a finished session
  turnless_prompts:              # RE2 patterns of prompts a hook answers without a turn
    - '^/clip(?:\s+\d+)?\s*$'
mac:
  release_command: /lend         # your command that releases the Mac to a waiting agent
maintenance:
  freeze_also_refuses:           # extra gestures your own hooks refuse during a freeze
    - deploy --prod
```

| Key | Unset (default) | Set |
|---|---|---|
| `session.done_command` | A `Done` pill with no reason reads `Done`; no prompt is special; pigtaild never types anything into an idle Done session; the idle pause reclaims it like any other idle session. | The pill reads `Done · <command>`; the prompt `<command>` or `<command> <args>` clears the turn's waits but keeps the subject's wait open for its next session; pigtaild types `<command>` once per process into a session idle past `-idle-session-pause-seconds` whose declared wait is `done`; `pigtail session spawn`'s relay line and the refusal of an answer to a done wait name it. A slash command: `/` then letters, digits, `:_.-`, 64 characters at most. |
| `session.turnless_prompts` | Every typed prompt is a turn: it clears the session's waits and Standby. | A prompt matching one pattern is answered by your hook without an agent turn: it clears no wait, does not end Standby and does not clear a respawn's abandoned mark. At most 32 patterns, each must compile. |
| `mac.release_command` | A Mac wait with no reason reads `Waiting for Mac`. | It reads `Waiting for Mac · <command>`, and `pigtail session wait mac` prints the same label. Same form as `done_command`. |
| `maintenance.freeze_also_refuses` | The freeze (`pigtail maintenance on --freeze`) lists what pigtail itself refuses: subject writes, `pigtail new`, `pigtail dispatch`, `pigtail session spawn`. | Each gesture is added to the freeze line and to the reopen notice as refused to agents — pigtail only says it; your own hook must refuse it. Each is 80 characters at most, no backtick or control character. |

A bad file (unknown key, invalid value) is refused whole: pigtail runs on the
defaults and logs the error.

The menu-bar app's « Reprendre » button (maintenance chip) runs your own
take-back command, set in the app's defaults — the program's path first (a
leading `~` is your home), then its arguments:

```bash
defaults write sh.pigtail.app maintenanceTakeBackCommand -array '~/bin/take-back' --back
```

Unset, the button runs nothing and the chip says no command is configured.
