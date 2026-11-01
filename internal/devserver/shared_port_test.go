package devserver

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestStopServersStopsAPortOnceUnderEveryName: `dev` and `web` are one
// process. The stop must announce both names to the daemon (or the twin comes
// back as a death), clear both owner markers, kill once, and say on one line
// that the twin went down with it.
func TestStopServersStopsAPortOnceUnderEveryName(t *testing.T) {
	defer installSeams(t)()
	notified := withStopNotifier(t)
	var kills [][]int
	killPIDsFn = func(pids []int) { kills = append(kills, pids) }
	var buf bytes.Buffer
	out = &buf

	wtDir := t.TempDir()
	for _, name := range []string{"dev", "web"} {
		if err := writeOwner(wtDir, name, "ws-a"); err != nil {
			t.Fatal(err)
		}
	}
	dev := pigtail.ServerSpec{PortKind: "dev"}
	probed := []probedServer{
		{name: "dev", spec: dev, port: 3042, busy: true, pids: []int{4242}},
		{name: "storybook", spec: pigtail.ServerSpec{PortKind: "storybook"}, port: 6042},
		{name: "web", spec: dev, port: 3042, busy: true, pids: []int{4242}},
	}

	if got := stopServers(wtDir, probed); got != 1 {
		t.Errorf("stopServers = %d, want 1 (one process stopped)", got)
	}

	if want := [][2]string{{wtDir, "dev"}, {wtDir, "web"}}; !reflect.DeepEqual(*notified, want) {
		t.Errorf("notifications = %v, want %v", *notified, want)
	}
	if want := [][]int{{4242}}; !reflect.DeepEqual(kills, want) {
		t.Errorf("kills = %v, want the port's pids killed exactly once", kills)
	}
	for _, name := range []string{"dev", "web"} {
		if owner := ReadOwner(wtDir, name); owner != "" {
			t.Errorf("owner marker of %s = %q, want cleared", name, owner)
		}
	}
	report := buf.String()
	if strings.Count(report, "✓") != 1 {
		t.Errorf("want a single ✓ line for one process, got:\n%s", report)
	}
	if want := "✓ dev (:3042) — also stopped web (same port)"; !strings.Contains(report, want) {
		t.Errorf("want %q, got:\n%s", want, report)
	}
}
