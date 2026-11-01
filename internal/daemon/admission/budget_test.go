package admission

import "testing"

func TestMaxJobsFor(t *testing.T) {
	cases := []struct {
		cores int
		mem   uint64
		want  int
	}{
		{6, 64 << 30, 6},  // this M5 Pro: min(6 perf cores, 6)
		{12, 64 << 30, 6}, // memory binds
		{4, 64 << 30, 4},  // cores bind
		{8, 16 << 30, 1},  // a 16 GB laptop runs them one at a time
		{8, 4 << 30, 1},   // never zero: zero is the kill switch
		{0, 64 << 30, 6},  // unreadable core count: memory alone
	}
	for _, c := range cases {
		if got := maxJobsFor(c.cores, c.mem); got != c.want {
			t.Errorf("maxJobsFor(%d, %d GiB) = %d, want %d", c.cores, c.mem>>30, got, c.want)
		}
	}
	if DefaultMaxJobs() < 1 {
		t.Fatal("DefaultMaxJobs must never be below 1")
	}
}
