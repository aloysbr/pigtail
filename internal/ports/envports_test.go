package ports

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type fakeEnv []EnvSource

func (f fakeEnv) EnvSources() ([]EnvSource, error) { return f, nil }

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The incident: ws_efb2e9 had dev=3000 in the store but PORT=3013 in its
// .env.local, and a server on 3013. 3013 must not be handed out.
func TestAllocateSkipsPortNamedInLiveEnvLocal(t *testing.T) {
	other := writeEnv(t, "PORT=38013\nSTORYBOOK_PORT=38014\n")
	a := New(fakeUsed{m: map[int]bool{}},
		WithBindProbe(func(int) bool { return true }),
		WithEnvSources(fakeEnv{{Dir: other, Vars: []string{"PORT", "STORYBOOK_PORT"}}}))
	p, err := a.Allocate(map[string]pigtail.PortRange{"dev": {38013, 38015}}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if p != 38015 {
		t.Fatalf("got %d, want 38015 (38013 and 38014 are named in a live .env.local)", p)
	}
}

func TestEnvPortsSkipsMalformed(t *testing.T) {
	bad := writeEnv(t, "PORT=abc\nSTORYBOOK_PORT=\n")
	last := writeEnv(t, "PORT=38020\nPORT=38021\n")
	got := envPorts([]EnvSource{
		{Dir: bad, Vars: []string{"PORT", "STORYBOOK_PORT"}},
		{Dir: last, Vars: []string{"PORT"}},
		{Dir: filepath.Join(t.TempDir(), "gone"), Vars: []string{"PORT"}},
	})
	if len(got) != 1 || !got[38021] {
		t.Fatalf("got %v, want {38021} (last assignment wins, junk ignored)", got)
	}
}
