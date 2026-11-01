package mainfresh

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func proj(id, name string) *pigtail.Project {
	return &pigtail.Project{ID: id, Name: name, RootPath: "/tmp/" + id}
}

var t0 = time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)

// TestRegistryKeepsTheNewestVerdict: the sweep and every create both measure
// the same directory. The newest measurement is the only one that describes it
// now, so it replaces the previous one rather than accumulating beside it.
func TestRegistryKeepsTheNewestVerdict(t *testing.T) {
	r := New()
	r.Record(proj("prj_1", "globex"), backend.MainRefresh{
		Action: backend.MainRefreshSkipped, Behind: 23, Reason: "dirty",
	}, t0)
	r.Record(proj("prj_1", "globex"), backend.MainRefresh{
		Action: backend.MainRefreshUpToDate, Base: "origin/main",
	}, t0.Add(10*time.Minute))

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("two verdicts about one project produced %d entries", len(got))
	}
	if got[0].Action != backend.MainRefreshUpToDate || got[0].Behind != 0 {
		t.Fatalf("kept %+v, want the newer verdict", got[0])
	}
	if !got[0].At.Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("timestamp = %v, want the newer measurement's", got[0].At)
	}
}

// TestRegistryOrdersStably: a listing that reshuffled between two calls would
// make a diff of `pigtail servers` unreadable.
func TestRegistryOrdersStably(t *testing.T) {
	r := New()
	for _, p := range []*pigtail.Project{proj("prj_z", "tools"), proj("prj_a", "globex"), proj("prj_m", "artichaut")} {
		r.Record(p, backend.MainRefresh{Action: backend.MainRefreshUpToDate}, t0)
	}
	for i := 0; i < 5; i++ {
		got := r.Snapshot()
		if len(got) != 3 {
			t.Fatalf("snapshot %d holds %d entries, want 3", i, len(got))
		}
		if got[0].ProjectName != "artichaut" || got[2].ProjectName != "tools" {
			t.Fatalf("snapshot %d out of order: %v", i, []string{got[0].ProjectName, got[1].ProjectName, got[2].ProjectName})
		}
	}
}

// TestRegistryIgnoresAVerdictThatIsNotOne: an empty action is what a backend
// with no shared checkout produces. Recording it would publish a project with
// a blank freshness, which a display cannot render as anything but a lie.
func TestRegistryIgnoresAVerdictThatIsNotOne(t *testing.T) {
	r := New()
	r.Record(proj("prj_1", "globex"), backend.MainRefresh{}, t0)
	r.Record(nil, backend.MainRefresh{Action: backend.MainRefreshUpToDate}, t0)

	if got := r.Snapshot(); len(got) != 0 {
		t.Fatalf("registry holds %+v, want nothing", got)
	}
}

// TestNilRegistryIsANoOp: the registry is optional wiring on both the write
// and the read side; a daemon or a test built without one must not panic.
func TestNilRegistryIsANoOp(t *testing.T) {
	var r *Registry
	r.Record(proj("prj_1", "globex"), backend.MainRefresh{Action: backend.MainRefreshUpToDate}, t0)
	if got := r.Snapshot(); got != nil {
		t.Fatalf("nil registry answered %+v", got)
	}
}

// TestRegistryIsConcurrencySafe: the sweep goroutine writes while HTTP
// handlers read. Run with -race, this is the test that catches a missing lock.
func TestRegistryIsConcurrencySafe(t *testing.T) {
	r := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			r.Record(proj("prj_1", "globex"), backend.MainRefresh{
				Action: backend.MainRefreshUpToDate, Base: "origin/main",
			}, t0.Add(time.Duration(i)*time.Second))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = r.Snapshot()
	}
	<-done
}
