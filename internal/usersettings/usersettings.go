// Package usersettings reads the optional settings.yaml of the pigtail config
// directory: the human's name, the first message of a launched session, and
// the user's own commands pigtail may name or run (it ships none of them).
// Every value has a neutral default; a bad file gives the defaults and an error.
package usersettings

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// FileName is the settings file inside the config directory.
const FileName = "settings.yaml"

// DefaultHumanName names the human when settings.yaml does not.
const DefaultHumanName = "the user"

// DefaultPromptTemplate is the first message of a launched session. Placeholders: {id}, {title}.
const DefaultPromptTemplate = "Work on pigtail subject {id}: {title}. " +
	"Read it with `pigtail subject show {id}`. " +
	"Register every deliverable you produce (file, capture, report, PR) with `pigtail subject output add {id} --kind <kind> --path <path>`. " +
	"When you hand back, declare what you wait for with `pigtail session wait <type> \"<reason>\"`."

const (
	maxNameRunes    = 64
	maxGestureRunes = 80
	maxTurnless     = 32
)

type Human struct {
	Name string `yaml:"name"`
}

type Launch struct {
	PromptTemplate string `yaml:"prompt_template"`
}

// Session names the user's own commands around a session's end.
type Session struct {
	// DoneCommand is the user's slash command that closes a finished session
	// ("" = none). Set: a Done pill with no reason reads "Done · <command>",
	// the prompt <command> [args] keeps the subject's wait open, and pigtaild
	// types it into an idle session whose declared wait is done.
	DoneCommand string `yaml:"done_command"`
	// TurnlessPrompts are RE2 patterns of the prompts a user hook answers
	// without an agent turn: such a prompt leaves the session's wait standing.
	TurnlessPrompts []string `yaml:"turnless_prompts"`
}

// Mac names the user's own command that gives the Mac to a session waiting for it.
type Mac struct {
	// ReleaseCommand ("" = none) is shown on a Mac pill with no reason:
	// "Waiting for Mac · <command>".
	ReleaseCommand string `yaml:"release_command"`
}

// Maintenance names what the user's own guards also refuse during a freeze.
type Maintenance struct {
	// FreezeAlsoRefuses lists gestures the user's own guards refuse to agents
	// while a maintenance freezes; the notice names them. Empty = none.
	FreezeAlsoRefuses []string `yaml:"freeze_also_refuses"`
}

type Settings struct {
	Human       Human       `yaml:"human"`
	Launch      Launch      `yaml:"launch"`
	Session     Session     `yaml:"session"`
	Mac         Mac         `yaml:"mac"`
	Maintenance Maintenance `yaml:"maintenance"`
}

// Defaults returns the neutral settings.
func Defaults() Settings {
	return Settings{Human: Human{Name: DefaultHumanName}, Launch: Launch{PromptTemplate: DefaultPromptTemplate}}
}

// Load reads dir/settings.yaml. Absent file: defaults, nil error. Any problem:
// defaults and an error (the caller logs it once and carries on).
func Load(dir string) (Settings, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Defaults(), fmt.Errorf("settings: %w", err)
	}
	var s Settings
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return Defaults(), fmt.Errorf("settings: %s: %w", FileName, err)
	}
	s.Human.Name = strings.TrimSpace(s.Human.Name)
	if s.Human.Name == "" {
		s.Human.Name = DefaultHumanName
	} else if err := validName(s.Human.Name); err != nil {
		return Defaults(), fmt.Errorf("settings: human.name: %w", err)
	}
	if strings.TrimSpace(s.Launch.PromptTemplate) == "" {
		s.Launch.PromptTemplate = DefaultPromptTemplate
	}
	s.Session.DoneCommand = strings.TrimSpace(s.Session.DoneCommand)
	if err := validCommand(s.Session.DoneCommand); err != nil {
		return Defaults(), fmt.Errorf("settings: session.done_command: %w", err)
	}
	s.Mac.ReleaseCommand = strings.TrimSpace(s.Mac.ReleaseCommand)
	if err := validCommand(s.Mac.ReleaseCommand); err != nil {
		return Defaults(), fmt.Errorf("settings: mac.release_command: %w", err)
	}
	if len(s.Session.TurnlessPrompts) > maxTurnless {
		return Defaults(), fmt.Errorf("settings: session.turnless_prompts: more than %d patterns", maxTurnless)
	}
	for i, p := range s.Session.TurnlessPrompts {
		if strings.TrimSpace(p) == "" {
			return Defaults(), fmt.Errorf("settings: session.turnless_prompts[%d]: empty pattern", i)
		}
		if _, err := regexp.Compile(p); err != nil {
			return Defaults(), fmt.Errorf("settings: session.turnless_prompts[%d]: %w", i, err)
		}
	}
	for i, g := range s.Maintenance.FreezeAlsoRefuses {
		g = strings.TrimSpace(g)
		if err := validGesture(g); err != nil {
			return Defaults(), fmt.Errorf("settings: maintenance.freeze_also_refuses[%d]: %w", i, err)
		}
		s.Maintenance.FreezeAlsoRefuses[i] = g
	}
	return s, nil
}

func validName(n string) error {
	if utf8.RuneCountInString(n) > maxNameRunes {
		return fmt.Errorf("longer than %d characters", maxNameRunes)
	}
	for _, r := range n {
		if r == '<' || r == '>' || r == '`' || r == '\\' || unicode.IsControl(r) {
			return fmt.Errorf("character %q not allowed", r)
		}
	}
	return nil
}

// command is a slash command pigtail may type into a session: a slash, then a
// name of the characters a skill name takes. Nothing a terminal would read
// as more than one word.
var command = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$`)

func validCommand(c string) error {
	if c != "" && !command.MatchString(c) {
		return fmt.Errorf("%q is not a slash command (/name, letters, digits, : _ . -)", c)
	}
	return nil
}

func validGesture(g string) error {
	if g == "" {
		return errors.New("empty")
	}
	if utf8.RuneCountInString(g) > maxGestureRunes {
		return fmt.Errorf("longer than %d characters", maxGestureRunes)
	}
	for _, r := range g {
		if r == '`' || unicode.IsControl(r) {
			return fmt.Errorf("character %q not allowed", r)
		}
	}
	return nil
}

// LaunchPrompt renders the template for a subject.
func (s Settings) LaunchPrompt(id, title string) string {
	return strings.NewReplacer("{id}", id, "{title}", title).Replace(s.Launch.PromptTemplate)
}

// WaitCommands is what waittype needs of the settings (waittype.Configure).
// The patterns were checked by Load; one that does not compile is skipped.
func (s Settings) WaitCommands() waittype.Commands {
	c := waittype.Commands{Done: s.Session.DoneCommand, MacRelease: s.Mac.ReleaseCommand}
	for _, p := range s.Session.TurnlessPrompts {
		if re, err := regexp.Compile(p); err == nil {
			c.Turnless = append(c.Turnless, re)
		}
	}
	return c
}
