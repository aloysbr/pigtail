package usersettings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
)

func write(t *testing.T, body string) string {
	t.Helper()
	dir := testutil.MkdirTemp(t, "us-")
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadAbsentGivesDefaults(t *testing.T) {
	s, err := Load(testutil.MkdirTemp(t, "us-"))
	if err != nil || !reflect.DeepEqual(s, Defaults()) {
		t.Fatalf("got %+v, %v", s, err)
	}
	if s.Human.Name != DefaultHumanName || s.Launch.PromptTemplate != DefaultPromptTemplate {
		t.Fatalf("defaults wrong: %+v", s)
	}
}

func TestLoadInvalidGivesDefaultsAndError(t *testing.T) {
	for _, body := range []string{"human: [", "human: 3\n", "bogus: 1\n", "human:\n  name: \"a<b\"\n", "human:\n  name: \"a`b\"\n", "human:\n  name: 'a\\b'\n",
		"human:\n  name: \"" + strings.Repeat("x", 65) + "\"\n", "human:\n  name: \"a\\nb\"\n",
		"session:\n  done_command: finish\n", "session:\n  done_command: \"/finish now\"\n", "session:\n  done_command: \"/a;b\"\n",
		"mac:\n  release_command: lend\n", "session:\n  turnless_prompts: [\"(\"]\n", "session:\n  turnless_prompts: [\"  \"]\n",
		"maintenance:\n  freeze_also_refuses: [\"a`b\"]\n", "maintenance:\n  freeze_also_refuses: [\"\"]\n",
		"maintenance:\n  freeze_also_refuses: [\"" + strings.Repeat("x", 81) + "\"]\n"} {
		s, err := Load(write(t, body))
		if err == nil {
			t.Fatalf("%q: want error", body)
		}
		if !reflect.DeepEqual(s, Defaults()) {
			t.Fatalf("%q: want defaults, got %+v", body, s)
		}
	}
}

func TestLoadEmptyValuesGiveDefaults(t *testing.T) {
	s, err := Load(write(t, "human:\n  name: \"\"\nlaunch:\n  prompt_template: \"\"\n"))
	if err != nil || !reflect.DeepEqual(s, Defaults()) {
		t.Fatalf("got %+v, %v", s, err)
	}
	s, err = Load(write(t, "human:\n  name: Ada\nlaunch:\n  prompt_template: \"/m {id}\"\n"))
	if err != nil || s.Human.Name != "Ada" || s.Launch.PromptTemplate != "/m {id}" {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestLaunchPromptTemplate(t *testing.T) {
	s := Defaults()
	s.Launch.PromptTemplate = "/x {id} — {title}"
	if got := s.LaunchPrompt("sub_1", "T"); got != "/x sub_1 — T" {
		t.Fatalf("got %q", got)
	}
	if got := Defaults().LaunchPrompt("sub_1", "T"); !strings.HasPrefix(got, "Work on pigtail subject sub_1: T.") {
		t.Fatalf("got %q", got)
	}
}

// The user's own commands: unset gives none, set gives them back to waittype.
func TestCommandsUnsetAndSet(t *testing.T) {
	s, err := Load(write(t, "human:\n  name: Ada\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := s.WaitCommands()
	if s.Session.DoneCommand != "" || s.Mac.ReleaseCommand != "" || len(s.Maintenance.FreezeAlsoRefuses) != 0 || c.Done != "" || c.MacRelease != "" || len(c.Turnless) != 0 {
		t.Fatalf("unset: %+v, %+v", s, c)
	}
	body := "session:\n  done_command: \" /finish \"\n  turnless_prompts:\n    - '(?s)^/(?:lend|reclaim)(?:\\s+.*?)?\\s*$'\n" +
		"mac:\n  release_command: /lend\n" +
		"maintenance:\n  freeze_also_refuses: [\" deploy --prod \"]\n"
	s, err = Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	c = s.WaitCommands()
	if c.Done != "/finish" || c.MacRelease != "/lend" || len(c.Turnless) != 1 || !c.Turnless[0].MatchString("/reclaim now") || c.Turnless[0].MatchString("/lender") {
		t.Fatalf("set: %+v", c)
	}
	if !reflect.DeepEqual(s.Maintenance.FreezeAlsoRefuses, []string{"deploy --prod"}) {
		t.Fatalf("freeze_also_refuses = %q", s.Maintenance.FreezeAlsoRefuses)
	}
}
