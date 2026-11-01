package waittype

import "testing"

// The five gravities are the only colours a pigtail pill may wear (D1, D7).
func TestGravityColours(t *testing.T) {
	want := map[Gravity]string{Red: "#FF3B30", Orange: "#FF9500", Green: "#34C759", Blue: "#0A84FF", Grey: "#8E8E93"}
	for g, c := range want {
		if g.Color() != c {
			t.Errorf("gravity %d = %s, want %s", g, g.Color(), c)
		}
	}
}

// Every state takes its colour from its gravity: no hex outside the table.
func TestEveryStateColourComesFromItsGravity(t *testing.T) {
	for _, ty := range Order {
		if ty.Color() != ty.Gravity().Color() {
			t.Errorf("%q: colour %s, gravity colour %s", ty, ty.Color(), ty.Gravity().Color())
		}
	}
}
