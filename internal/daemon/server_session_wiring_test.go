package daemon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/sysmem"
)

// fakeSensors keeps these tests off the real vm_stat and ps.
func fakeSensors(d *sessbudget.Deps) {
	d.Memory = func() (sysmem.Memory, error) { return sysmem.Memory{Available: 8 << 30}, nil }
	d.Procs = func() (map[int]sysmem.Proc, error) { return map[int]sysmem.Proc{}, nil }
}

func sampleLines(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(sessbudget.SamplesPath())
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// The session sampler starts with the daemon at the flag's default cadence,
// writes a first sample at once, and stops with the daemon's context.
func TestServerStartsSessionSamplerByDefault(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{
		SocketPath: testSocketPath(t), SessionSampleSeconds: DefaultSessionSampleSeconds,
		SessionSamplerWrap: fakeSensors,
	}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.sessionSampler == nil {
		t.Fatal("no sampler built at the default cadence")
	}
	stop := startDaemon(t, s)
	deadline := time.Now().Add(5 * time.Second)
	for sampleLines(t) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no sample written by the running daemon")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop() // Run returns only once every spawned goroutine, the sampler's included, has
	n := sampleLines(t)
	time.Sleep(200 * time.Millisecond)
	if got := sampleLines(t); got != n {
		t.Fatalf("sampler still writing after the daemon stopped: %d -> %d", n, got)
	}
}

func TestServerSessionSamplerOffAtZero(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t), SessionSampleSeconds: 0, SessionSamplerWrap: fakeSensors},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.sessionSampler != nil {
		t.Fatal("a sampler was built at 0")
	}
	stop := startDaemon(t, s)
	time.Sleep(200 * time.Millisecond)
	stop()
	if n := sampleLines(t); n != 0 {
		t.Fatalf("%d samples written with the sampler off", n)
	}
}
