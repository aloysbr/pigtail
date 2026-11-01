package powersrc

import (
	"errors"
	"testing"
)

// fakeSource lets tests drive OnBattery and its error independently.
type fakeSource struct {
	onBattery bool
	err       error
}

func (f fakeSource) OnBattery() (bool, error) { return f.onBattery, f.err }

func TestSampleOnBattery(t *testing.T) {
	cases := []struct {
		name string
		src  fakeSource
		want bool
	}{
		{"on battery, no error", fakeSource{onBattery: true}, true},
		{"on mains, no error", fakeSource{onBattery: false}, false},
		{"error reports mains (fail-safe)", fakeSource{onBattery: true, err: errors.New("boom")}, false},
		{"error on mains stays mains", fakeSource{onBattery: false, err: errors.New("boom")}, false},
	}
	for _, c := range cases {
		if got := SampleOnBattery(c.src); got != c.want {
			t.Errorf("%s: SampleOnBattery = %v, want %v", c.name, got, c.want)
		}
	}
}
