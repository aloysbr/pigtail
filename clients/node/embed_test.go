package nodeclient

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInstallWritesEveryFile(t *testing.T) {
	dir := t.TempDir()
	setup, err := Install(dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want := filepath.Join(dir, "slots.mjs"); setup.Vitest != want {
		t.Fatalf("vitest setup = %q, want %q", setup.Vitest, want)
	}
	if want := filepath.Join(dir, "jest-slots.cjs"); setup.Jest != want {
		t.Fatalf("jest setup = %q, want %q", setup.Jest, want)
	}
	for _, name := range installed {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

// Both setups must reach the transport by a relative path: all four files land
// in one directory, and a bare specifier would be resolved against the target
// repo's node_modules, where nothing of pigtail's exists.
func TestSetupsImportTheTransportRelatively(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, tc := range []struct{ file, want string }{
		{"slots.mjs", "./client.mjs"},
		{"client.mjs", "./client.cjs"},
		{"jest-slots.cjs", "./client.cjs"},
	} {
		b, err := os.ReadFile(filepath.Join(dir, tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if !strings.Contains(string(b), tc.want) {
			t.Fatalf("%s does not reference %s", tc.file, tc.want)
		}
	}
}

// The jest setup is required by jest itself from under node_modules, where
// transformIgnorePatterns excludes it from every transform: it has to be plain
// CommonJS, so an `import`/`export` keyword slipping in would break every jest
// run rather than fail a test here.
func TestJestSetupIsPlainCommonJS(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, name := range []string{"jest-slots.cjs", "client.cjs"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "export ") {
				t.Fatalf("%s has an ESM statement jest cannot load: %q", name, trimmed)
			}
		}
	}
}

// Install runs before every `pigtail test`: a second call must overwrite, not fail.
func TestInstallIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if _, err := Install(dir); err != nil {
		t.Fatalf("second Install: %v", err)
	}
}

// TestAcquireTimeoutsAgree guards a duplication the browser fix forced.
//
// slots.mjs sizes its hook timeout from ACQUIRE_TIMEOUT_MS, but it can no
// longer import the transport to read it: a static import of client.cjs kills
// the file in a browser-mode run (see slots-loading.test.mjs), so the constant
// is mirrored. Drift would be silent and nasty — a hook timeout below the
// acquire timeout makes vitest fail files the arbiter was merely making wait.
func TestAcquireTimeoutsAgree(t *testing.T) {
	const pattern = `ACQUIRE_TIMEOUT_MS = ([0-9_]+)`
	re := regexp.MustCompile(pattern)

	value := func(name string) string {
		t.Helper()
		b, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		m := re.FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: no %s", name, pattern)
		}
		return strings.ReplaceAll(string(m[1]), "_", "")
	}

	if got, want := value("slots.mjs"), value("client.cjs"); got != want {
		t.Fatalf("slots.mjs mirrors ACQUIRE_TIMEOUT_MS = %s, client.cjs has %s — they must match", got, want)
	}
}
