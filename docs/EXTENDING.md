# Extending pigtail

pigtail works with no private methodology. Two things let you plug in your own: a small
settings file, and a default Claude Code skill.

## `settings.yaml`

Optional file in pigtail's config directory (`~/.config/pigtail/settings.yaml`, or
`$XDG_CONFIG_HOME/pigtail/settings.yaml`):

```yaml
human:
  name: Ada            # default "the user"
launch:
  prompt_template: "/example-skill resume {id}"   # placeholders {id}, {title}
```

- `human.name` is how agents see you: the answer you give to a wait reaches the session as
  "Answer from Ada ...", and the subject log traces it as `Ada -> <session> [answer ...]`.
  One line, at most 64 characters, none of `<`, `>`, `\`, a backtick or a control character.
- `launch.prompt_template` is the first message of every session pigtail launches (from the
  web app or the API). `{id}` is the subject id, `{title}` its title.
- A missing file gives the defaults silently. An invalid file, an unknown key or a bad name
  gives the defaults and one line in the daemon log; the daemon still starts. An empty value
  means its default.
- The file is read when the daemon starts: after a change, restart it.
- Your own slash commands (turnless prompts, the done and Mac-release commands) are declared there too: see [CONFIG.md, `settings.yaml` — your own commands](CONFIG.md#settingsyaml--your-own-commands).

## The default prompt

With no template, a launched session starts with:

```
Work on pigtail subject {id}: {title}. Read it with `pigtail subject show {id}`.
Register every deliverable you produce (file, capture, report, PR) with
`pigtail subject output add {id} --kind <kind> --path <path>`. When you hand back,
declare what you wait for with `pigtail session wait <type> "<reason>"`.
```

(One line in practice.) A test checks that every command and flag it cites exists.

## Plugging in a method

Point `launch.prompt_template` at your own slash command or skill, for example
`/example-skill resume {id}`. pigtail only supplies the subject; what the session does with it is
your method's business. Your method can still use the same three gestures: read the subject,
register outputs, declare waits.

## The default skill

`skills/pigtail/SKILL.md` is a Claude Code skill that teaches an agent those three gestures
and how to read peer messages. It is embedded in the binary:

```
pigtail skills show             # print it
pigtail skills install          # write ~/.claude/skills/pigtail/SKILL.md ($CLAUDE_CONFIG_DIR respected)
pigtail skills install --force  # overwrite a different installed copy
```

`install` writes the file when absent, prints "already up to date" when it is identical, and
refuses (exit 1) to overwrite a different file unless you pass `--force`.
