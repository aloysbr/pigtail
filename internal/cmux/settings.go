package cmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AgentAutoResumeKey is cmux's « Resume Agent Sessions on Reopen » setting
// (measured: sub_x000000000000037, task 1): object "terminal", key
// "autoResumeAgentSessions" in settings.json, same dotted name in
// UserDefaults. D1: off, pigtail starts the restored sessions itself.
const AgentAutoResumeKey = "terminal.autoResumeAgentSessions"

// cmuxDefaultsDomain is cmux's UserDefaults domain.
const cmuxDefaultsDomain = "com.cmuxterm.app"

// SettingsPath is cmux's user settings file (JSONC).
func SettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "cmux", "settings.json")
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

// ReadSettingsJSON reads cmux's settings file as plain JSON: whole-line //
// comments and trailing commas removed — what cmux's own template uses, not
// every JSONC form.
func ReadSettingsJSON(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kept []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(trailingCommaRe.ReplaceAllString(strings.Join(kept, "\n"), "$1")), nil
}

// ReadDefaults reads one key of cmux's UserDefaults domain: (raw value,
// found, err). found=false with a nil err is the key's absence (defaults
// exits 1 and says the pair does not exist); any other failure — timeout, no
// binary, another exit — is an err: unknown, never absence. A var so tests
// never touch the real Mac's preferences.
var ReadDefaults = func(key string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "defaults", "read", cmuxDefaultsDomain, key)
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf("defaults read %s %s: %w", cmuxDefaultsDomain, key, err)
}

// AgentAutoResume: does cmux resume the agent sessions itself on reopen?
// settings.json is read first; a key absent there falls back to UserDefaults;
// absent from both is cmux's default, on. known is false when the file
// cannot be read or parsed, a defaults read fails (timeout, no binary, an exit
// other than 1), or a defaults value is not a boolean: the caller
// keeps today's behaviour (on) and says so — never "off".
func AgentAutoResume(path string) (on, known bool) {
	b, err := ReadSettingsJSON(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// no file: the key is absent there
	case err != nil:
		return true, false
	default:
		var s struct {
			Terminal struct {
				AutoResumeAgentSessions *bool `json:"autoResumeAgentSessions"`
			} `json:"terminal"`
		}
		if json.Unmarshal(b, &s) != nil {
			return true, false
		}
		if v := s.Terminal.AutoResumeAgentSessions; v != nil {
			return *v, true
		}
	}
	raw, found, derr := ReadDefaults(AgentAutoResumeKey)
	if derr != nil {
		return true, false
	}
	if !found {
		return true, true
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes":
		return true, true
	case "0", "false", "no":
		return false, true
	}
	return true, false
}
