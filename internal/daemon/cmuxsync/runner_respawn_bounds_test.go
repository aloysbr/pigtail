package cmuxsync

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type readCall struct {
	path string
	from int64
	next int64
}

type ioRecorder struct {
	mu    sync.Mutex
	reads []readCall
	finds []string
}

func (rec *ioRecorder) install(h *nativeHarness) {
	h.r.deps.ReadPrompts = func(path string, from int64, since time.Time) ([]claudesession.Prompt, int64, error) {
		ps, next, err := claudesession.PromptsAfter(path, from, since)
		rec.mu.Lock()
		rec.reads = append(rec.reads, readCall{path, from, next})
		rec.mu.Unlock()
		return ps, next, err
	}
	h.r.deps.FindTranscript = func(id, cfg string) (string, bool) {
		rec.mu.Lock()
		rec.finds = append(rec.finds, id)
		rec.mu.Unlock()
		tr, ok, err := claudesession.Find(id, []string{cfg})
		if err != nil || !ok {
			return "", false
		}
		return tr.JSONLPath(), true
	}
}

func (rec *ioRecorder) counts() (int, int) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.reads), len(rec.finds)
}

func (rec *ioRecorder) expect(t *testing.T, step string, reads, finds int) {
	t.Helper()
	if r, f := rec.counts(); r != reads || f != finds {
		t.Fatalf("%s: %d reads, %d lookups; want %d, %d", step, r, f, reads, finds)
	}
}

// The transcript I/O is bounded: nothing without an abandoned mark, only the
// mark's surface, appended bytes only, a missing transcript looked up once
// per slow pass.
func TestRunnerRespawnTranscriptReadsAreBounded(t *testing.T) {
	t.Parallel()
	const otherSurface = "5E5E5E5E-0000-4000-8000-0000000000A2"
	h := newNativeHarness(t, on())
	rec := &ioRecorder{}
	rec.install(h)
	h.busyFrozen(t)
	h.nativeOn("s2", wsA, otherSurface, 4343, "busy", h.now.Add(-time.Hour))
	h.transcriptAppend("s2", humanLine(h.now.Add(time.Hour), "x"))

	h.step(time.Second)
	h.step(time.Second)
	rec.expect(t, "no mark", 0, 0)

	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
	h.step(time.Second)
	rec.expect(t, "waiting", 0, 0)
	h.r.RespawnClear(wsA)

	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.transcriptAppend("s1", humanLine(h.now.Add(time.Second), "argv"))
	h.step(time.Second)
	h.step(time.Second)
	rec.expect(t, "failed", 0, 0)
	h.r.RespawnClear(wsA)

	h.r.RespawnMark(wsA, abandonedReq())
	h.step(time.Second) // a slow pass: the mark nudged
	h.step(time.Second) // a fast one
	rec.expect(t, "abandoned", 2, 1)
	for _, c := range rec.reads {
		if c.path != h.transcriptPath("s1") {
			t.Fatalf("read %s, want only the mark's surface", c.path)
		}
	}
	if rec.reads[0].from != -1 || rec.reads[1].from != rec.reads[0].next {
		t.Fatalf("offsets: %+v", rec.reads)
	}
	h.r.RespawnClear(wsA)
	h.step(time.Second)
	rec.expect(t, "cleared", 2, 1)
}

func TestRunnerRespawnMissingTranscriptIsLookedUpOncePerSlowPass(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	rec := &ioRecorder{}
	rec.install(h)
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s3", wsA, rsSurface, 4444, "busy", h.now.Add(-time.Hour))
	h.r.RespawnMark(wsA, abandonedReq())
	h.step(time.Second) // slow pass: the one lookup
	rec.expect(t, "first pass", 0, 1)
	h.step(time.Second)
	h.step(time.Second)
	rec.expect(t, "fast passes", 0, 1)
	h.step(31 * time.Second)
	rec.expect(t, "next slow pass", 0, 2)
	h.transcriptAppend("s3", humanLine(h.now.Add(-time.Hour), "start"))
	h.step(time.Second)
	rec.expect(t, "fast pass, file now there", 0, 2)
	h.step(31 * time.Second)
	if r, f := rec.counts(); f != 3 || r != 1 {
		t.Fatalf("found at the next slow pass: %d reads, %d lookups; want 1, 3", r, f)
	}
}

// A changed config dir invalidates the resolved path.
func TestRunnerRespawnConfigDirChangeResolvesAgain(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	rec := &ioRecorder{}
	rec.install(h)
	h.busyFrozen(t)
	h.r.RespawnMark(wsA, abandonedReq())
	h.step(time.Second)
	h.step(time.Second)
	rec.expect(t, "before", 2, 1)

	cfg2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg2, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	native, err := os.ReadFile(filepath.Join(h.cfg, "sessions", "4242.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := os.ReadFile(h.transcriptPath("s1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg2, "projects", "-w"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg2, "sessions", "4242.json"), native, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg2, "projects", "-w", "s1.jsonl"), tr, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: wsA, CmuxSurfaceID: rsSurface,
		PID: 4242, ConfigDir: cfg2, Status: "busy", UpdatedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.step(31 * time.Second)
	r, f := rec.counts()
	if f != 2 || r != 3 {
		t.Fatalf("after the change: %d reads, %d lookups; want 3, 2", r, f)
	}
	if last := rec.reads[len(rec.reads)-1]; last.from != -1 || last.path != filepath.Join(cfg2, "projects", "-w", "s1.jsonl") {
		t.Fatalf("last read = %+v, want a tail read of the new path", last)
	}
}

// A transcript that vanishes then reappears is read from its tail again, not
// from the offset of the file that was there before.
func TestRunnerRespawnVanishedTranscriptIsReadFromItsTailAgain(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	rec := &ioRecorder{}
	rec.install(h)
	h.busyFrozen(t)
	h.r.RespawnMark(wsA, abandonedReq())
	h.step(time.Second)
	h.step(time.Second) // second read starts at the first one's offset
	if last := rec.reads[len(rec.reads)-1]; last.from < 0 {
		t.Fatalf("second read = %+v, want an offset kept from the first", last)
	}
	if err := os.Remove(h.transcriptPath("s1")); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second) // read of the vanished file: ErrNoTranscript
	h.transcriptAppend("s1", humanLine(h.now.Add(-time.Hour), "start"))
	h.step(31 * time.Second) // slow pass: found again, read at once
	if last := rec.reads[len(rec.reads)-1]; last.from != -1 {
		t.Fatalf("read after the file came back = %+v, want a tail read (from -1)", last)
	}
}
