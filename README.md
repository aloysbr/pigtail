# pigtail

pigtail is a control centre for Claude Code sessions on macOS. It tracks your work as subjects, gives each one its own git worktree, runs the sessions in cmux, and arbitrates what they share (dev-server ports, RAM, CPU slots). You drive it from a CLI, a local web UI and a menu-bar app, all talking to one background daemon.

## Status: alpha

`v0.5.0-alpha.1`. Expect breakage and renamed commands between alphas. For this alpha, the web UI and the messages pigtail writes to agents (hand-offs, notices, relay prompts) are in French only.

## Prerequisites

- macOS 14 or later
- Claude Code, logged in
- cmux
- git
- tmux (optional)

To build from source you also need:

- Go 1.25 or later
- Node 22.13 or later (only to rebuild the web UI)
- Xcode command-line tools (for the macOS app)

## Build from source

```sh
git clone https://github.com/aloysbr/pigtail.git
cd pigtail
make build      # bin/pigtail and bin/pigtaild
make install    # copies them to ~/.local/bin (PREFIX=... to change)
make app        # builds and installs /Applications/Pigtail.app
pigtail setup   # installs and starts the daemon
```

Make sure `~/.local/bin` is on your `PATH`. `make web` rebuilds the web UI; you only need it if you change `web/`, because the built bundle is committed.

## Extension point

The prompt that a launched session receives is a template, not code. It lives in `~/.config/pigtail/settings.yaml`:

```yaml
human:
  name: the user          # how agents refer to you
launch:
  prompt_template: "..."  # placeholders: {id} (the subject id) and {title}
```

The daemon reads this file at start, so restart it after a change. A default Claude Code skill, `pigtail`, teaches an agent to read its subject, record its outputs and declare what it is waiting for. Install it with `pigtail skills install` (add `--force` to overwrite), into `${CLAUDE_CONFIG_DIR:-~/.claude}/skills/pigtail/`. To plug in your own workflow, change the template and the skill.

Details: [docs/EXTENDING.md](docs/EXTENDING.md).

## Themes and the vault

Subjects are grouped in themes (`--theme <id>` on `pigtail capture` and `pigtail subject`). A theme may link to a folder of notes, its *vault* (an Obsidian vault or any folder of markdown): the link is the theme's `vault_link`, and the root it sits under decides the theme's scope (`scopes.yaml`). Without a vault the theme still works, it just has no notes to open.

## Documentation

- [docs/CONFIG.md](docs/CONFIG.md): `pigtail.yaml`, hooks, profiles and `claude-cells.yaml`.
- [docs/EXTENDING.md](docs/EXTENDING.md): the launch prompt and the default skill.
- [docs/architecture.md](docs/architecture.md): how the daemon, the CLI and the backends fit together.
- [docs/wire-protocol.md](docs/wire-protocol.md): the CLI-to-daemon REST and SSE contract.

## The macOS app is not notarised

The app is not signed with an Apple Developer ID, so Gatekeeper blocks the first launch:

- macOS 15 or later: open System Settings, Privacy & Security, and click "Open Anyway" next to Pigtail. Right-click, Open no longer bypasses the alert since Sequoia.
- macOS 14: right-click the app, then Open.
- Any version: `xattr -dr com.apple.quarantine /Applications/Pigtail.app`

## License

MIT — see [LICENSE](LICENSE). The name and logo are covered by the [trademark policy](TRADEMARK.md).

## Author

Aloys Belpaire
