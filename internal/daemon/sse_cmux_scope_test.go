package daemon

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// cmux.sidebar_changed carries a subject id: a work subscriber gets it only
// when tagged work; untagged, it fails closed like message.*.
func TestSSECmuxSidebarEventsFollowTheirScope(t *testing.T) {
	ev := func(sc string) pigtail.Event {
		return pigtail.Event{Type: pigtail.EventCmuxSidebarChanged, Scope: sc, Data: map[string]interface{}{"subject_id": "sub_x"}}
	}
	cases := []struct {
		caller scope.Scope
		event  pigtail.Event
		want   bool
	}{
		{scope.Scope("globex"), ev("gx"), true},
		{scope.Scope("globex"), ev("perso"), false},
		{scope.Scope("globex"), ev(""), false},
		{scope.Scope("perso"), ev("perso"), true},
		{scope.Scope("perso"), ev(""), true},
	}
	for _, c := range cases {
		if got := eventVisible(c.caller, c.event); got != c.want {
			t.Errorf("%s sees %q-tagged event: %v, want %v", c.caller, c.event.Scope, got, c.want)
		}
	}
}
