package bgwork

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// freeDeps answers "nothing runs" for pid 4242 (native status idle): the
// tests turn one reading busy or unreadable.
func freeDeps() Deps {
	return Deps{
		Native: func(int) (claudesession.Native, bool) {
			return claudesession.Native{StartedAt: 1, Status: "idle"}, true
		},
		Transcript: func(string) (string, bool, error) { return "/t/s.jsonl", true, nil },
		Pending:    func(string, time.Time) ([]string, error) { return nil, nil },
		Ancestors:  func() ([]int, error) { return nil, nil },
		Shells:     func(int) ([]int, error) { return nil, nil },
		Screen:     func() (string, error) { return "", nil },
	}
}

func TestWorkSeesBackgroundAgent(t *testing.T) {
	d := freeDeps()
	d.Pending = func(string, time.Time) ([]string, error) { return []string{"agent-a1"}, nil }
	work, err := Work(d, "s1", 4242)
	if err != nil || len(work) != 1 || work[0] != "tâche agent-a1" {
		t.Fatalf("Work = %q, %v; want the background agent", work, err)
	}
}

func TestWorkSeesMonitor(t *testing.T) {
	d := freeDeps()
	d.Ancestors = func() ([]int, error) { return []int{77}, nil }
	d.Shells = func(int) ([]int, error) { return []int{77, 901}, nil }
	work, err := Work(d, "s1", 4242)
	if err != nil || len(work) != 1 || work[0] != "shell Bash 901" {
		t.Fatalf("Work = %q, %v; want the Monitor shell, ancestors excluded", work, err)
	}
}

func TestWorkSeesFooter(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-bg-agent.txt"))
	if err != nil {
		t.Fatal(err)
	}
	d := freeDeps()
	d.Screen = func() (string, error) { return string(b), nil }
	work, err := Work(d, "s1", 4242)
	if err != nil || len(work) != 1 || work[0] != "tâche de fond affichée à l'écran" {
		t.Fatalf("Work = %q, %v; want the footer row", work, err)
	}
}

func TestWorkIdleIsEmpty(t *testing.T) {
	work, err := Work(freeDeps(), "s1", 4242)
	if err != nil || len(work) != 0 {
		t.Fatalf("Work = %q, %v; want nothing", work, err)
	}
}

func TestWorkReadErrorFailsClosed(t *testing.T) {
	cases := map[string]func(d *Deps){
		"transcript": func(d *Deps) {
			d.Transcript = func(string) (string, bool, error) { return "", false, errors.New("eio") }
		},
		"no transcript": func(d *Deps) { d.Transcript = func(string) (string, bool, error) { return "", false, nil } },
		"pending":       func(d *Deps) { d.Pending = func(string, time.Time) ([]string, error) { return nil, errors.New("eio") } },
		"native": func(d *Deps) {
			d.Native = func(int) (claudesession.Native, bool) { return claudesession.Native{}, false }
		},
		"shells":    func(d *Deps) { d.Shells = func(int) ([]int, error) { return nil, errors.New("ps") } },
		"ancestors": func(d *Deps) { d.Ancestors = func() ([]int, error) { return nil, errors.New("ps") } },
		"screen":    func(d *Deps) { d.Screen = func() (string, error) { return "", errors.New("cmux") } },
	}
	for name, set := range cases {
		d := freeDeps()
		set(&d)
		if _, err := Work(d, "s1", 4242); err == nil {
			t.Errorf("%s unreadable: Work = nil error, want fail closed", name)
		}
	}
	// No pid: nothing pid-bound can be read, never "free".
	if _, err := Work(freeDeps(), "s1", 0); err == nil || !strings.Contains(err.Error(), "processus Claude") {
		t.Fatalf("pid 0: %v", err)
	}
}
