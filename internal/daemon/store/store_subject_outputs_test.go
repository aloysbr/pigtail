package store

import (
	"context"
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func newOutput(t *testing.T, subjectID, kind, path, session string, ts int64) pigtail.SubjectOutput {
	t.Helper()
	o := pigtail.SubjectOutput{SubjectID: subjectID, Kind: kind, Path: path, SessionID: session, TS: ts}
	if err := o.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return o
}

func TestAddSubjectOutputCreatesThenNoop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	o := newOutput(t, "sub_p", "capture", "/a/c.png", "s1", 1000)
	got, created, err := s.AddSubjectOutput(ctx, o)
	if err != nil || !created || got.Seq <= 0 {
		t.Fatalf("first add = %+v, %v, %v; want created with seq > 0", got, created, err)
	}
	again, created, err := s.AddSubjectOutput(ctx, o)
	if err != nil || created || again.Seq != got.Seq {
		t.Fatalf("second add = seq %d, %v, %v; want seq %d, created=false", again.Seq, created, err, got.Seq)
	}
}

func TestAddSubjectOutputFirstWriterWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first := newOutput(t, "sub_p", "capture", "/a/c.png", "s1", 1000)
	first.Title = "first"
	if _, _, err := s.AddSubjectOutput(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := newOutput(t, "sub_p", "capture", "/a/c.png", "s2", 2000)
	second.Title = "second"
	got, created, err := s.AddSubjectOutput(ctx, second)
	if err != nil || created {
		t.Fatalf("second add: created=%v err=%v", created, err)
	}
	check := func(where string, o pigtail.SubjectOutput) {
		if o.SessionID != "s1" || o.Title != "first" || o.TS != 1000 {
			t.Errorf("%s = session %q title %q ts %d; want s1/first/1000", where, o.SessionID, o.Title, o.TS)
		}
	}
	check("returned", got)
	list, err := s.ListSubjectOutputs(ctx, "sub_p", OutputFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	check("stored", list[0])
}

func TestListSubjectOutputsFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var seqs []int64
	for _, o := range []pigtail.SubjectOutput{
		newOutput(t, "sub_p", "capture", "/a/1.png", "s1", 1),
		newOutput(t, "sub_p", "mockup", "/a/2.html", "s2", 2),
		newOutput(t, "sub_p", "capture", "/a/3.png", "s2", 3),
		newOutput(t, "sub_t", "capture", "/a/4.png", "s1", 4),
	} {
		got, _, err := s.AddSubjectOutput(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, got.Seq)
	}
	all, err := s.ListSubjectOutputs(ctx, "sub_p", OutputFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d, %v; want 3", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Errorf("not seq ASC: %d after %d", all[i].Seq, all[i-1].Seq)
		}
	}
	for name, tc := range map[string]struct {
		f    OutputFilter
		want int
	}{
		"since":   {OutputFilter{SinceSeq: seqs[0]}, 2},
		"kind":    {OutputFilter{Kind: "mockup"}, 1},
		"session": {OutputFilter{SessionID: "s2"}, 2},
		"limit":   {OutputFilter{Limit: 1}, 1},
	} {
		got, err := s.ListSubjectOutputs(ctx, "sub_p", tc.f)
		if err != nil || len(got) != tc.want {
			t.Errorf("%s: %d rows, %v; want %d", name, len(got), err, tc.want)
		}
	}
	empty, err := s.ListSubjectOutputs(ctx, "sub_none", OutputFilter{})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty subject = %#v, %v; want non-nil []", empty, err)
	}
}

func TestPurgeSubjectOutput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	o := newOutput(t, "sub_p", "capture", "/a/c.png", "", 1)
	if _, _, err := s.AddSubjectOutput(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, err := s.PurgeSubjectOutput(ctx, "sub_p", o.Key, 5000)
	if err != nil || got.PurgedAt == nil || *got.PurgedAt != 5000 {
		t.Fatalf("purge = %+v, %v; want purged_at 5000", got, err)
	}
	again, err := s.PurgeSubjectOutput(ctx, "sub_p", o.Key, 9000)
	if err != nil || again.PurgedAt == nil || *again.PurgedAt != 5000 {
		t.Fatalf("second purge = %+v, %v; want purged_at kept at 5000", again, err)
	}
	list, err := s.ListSubjectOutputs(ctx, "sub_p", OutputFilter{})
	if err != nil || len(list) != 1 || list[0].PurgedAt == nil {
		t.Fatalf("list after purge = %+v, %v; want the purged row", list, err)
	}
	if _, err := s.PurgeSubjectOutput(ctx, "sub_p", "/nope", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key err = %v, want ErrNotFound", err)
	}
}

func TestSubjectOutputStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	kept := newOutput(t, "sub_p", "capture", "/a/1.png", "", 1000)
	gone := newOutput(t, "sub_p", "capture", "/a/2.png", "", 9000)
	for _, o := range []pigtail.SubjectOutput{kept, gone} {
		if _, _, err := s.AddSubjectOutput(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PurgeSubjectOutput(ctx, "sub_p", gone.Key, 1); err != nil {
		t.Fatal(err)
	}
	n, last, err := s.SubjectOutputStats(ctx, "sub_p")
	if err != nil || n != 1 || last != 1000 {
		t.Errorf("stats = %d, %d, %v; want 1, 1000", n, last, err)
	}
	n, last, err = s.SubjectOutputStats(ctx, "sub_none")
	if err != nil || n != 0 || last != 0 {
		t.Errorf("empty stats = %d, %d, %v; want 0, 0", n, last, err)
	}
	// Sub-second: same second, inserted in reverse order.
	for _, o := range []pigtail.SubjectOutput{
		newOutput(t, "sub_ms", "file", "/b/late", "", 1700000000500),
		newOutput(t, "sub_ms", "file", "/b/early", "", 1700000000000),
	} {
		if _, _, err := s.AddSubjectOutput(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if _, last, err := s.SubjectOutputStats(ctx, "sub_ms"); err != nil || last != 1700000000500 {
		t.Errorf("sub-second lastMs = %d, %v; want 1700000000500", last, err)
	}
}
