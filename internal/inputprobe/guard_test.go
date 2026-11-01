package inputprobe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProbeNeverCallsCmuxSend: the probe's only write is the DSR query on a
// tty. No source on its path may reach cmux's keyboard (send, send-key, a
// paste), and the probe package does not import internal/cmux at all
// (amendment 2026-10-27: kitty turned `cmux send \e[O` into a typed "[O").
func TestProbeNeverCallsCmuxSend(t *testing.T) {
	forbidden := []string{"cmux.Send", "SendText", "SendRaw", "SendKey", `"send"`, `"send-key"`, "set-buffer", "paste-buffer"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "../daemon/inputprobe_wiring.go", "../cmuxcheck/deps.go", "../cmuxcheck/inputdead.go")
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		seen++
		src := string(b)
		for _, w := range forbidden {
			if strings.Contains(src, w) {
				t.Errorf("%s contains %q", f, w)
			}
		}
		if !strings.Contains(f, "..") && strings.Contains(src, "pigtail/internal/cmux\"") {
			t.Errorf("%s imports internal/cmux", f)
		}
	}
	if seen < 5 {
		t.Fatalf("only %d sources read: the guard reads nothing", seen)
	}
}

// TestProbeNeverSignals: nothing in the probe's sources sends a signal. A dead
// input is only observed; stopping a claude is the check's resume (with its
// own guards), never the probe's.
func TestProbeNeverSignals(t *testing.T) {
	forbidden := []string{"syscall.Kill", "unix.Kill", ".Signal(", "os.FindProcess", "SIGTERM", "SIGKILL", "SIGSTOP"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "../daemon/inputprobe_wiring.go")
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		seen++
		for _, w := range forbidden {
			if strings.Contains(string(b), w) {
				t.Errorf("%s contains %q", f, w)
			}
		}
	}
	if seen < 5 {
		t.Fatalf("only %d sources read: the guard reads nothing", seen)
	}
}

// TestWriteTTYOnlyCallers: WriteTTY is referenced by internal/inputprobe, the
// cmuxcheck seam that wires the probe and the daemon's wiring only.
func TestWriteTTYOnlyCallers(t *testing.T) {
	allowed := map[string]bool{
		"internal/inputprobe":                       true,
		"internal/cmuxcheck/deps.go":                true,
		"internal/daemon/inputprobe_wiring.go":      true,
		"internal/daemon/inputprobe_wiring_test.go": true,
	}
	root := "../.."
	seen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		seen++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), "WriteTTY") {
			return nil
		}
		if allowed[rel] || allowed[filepath.ToSlash(filepath.Dir(rel))] {
			return nil
		}
		t.Errorf("%s references WriteTTY: only the probe, its cmuxcheck seam and the daemon wiring may", rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 100 {
		t.Fatalf("only %d Go files walked: the guard reads nothing", seen)
	}
}
