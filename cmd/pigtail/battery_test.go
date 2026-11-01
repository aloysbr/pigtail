package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestPrintBattery(t *testing.T) {
	t.Parallel()
	resp := pigtail.BatteryResponse{
		OnBattery:    true,
		Enabled:      true,
		GraceSeconds: 600,
		Servers: []pigtail.BatteryServerView{
			{WorkspaceID: "ws1", Server: "dev", Port: 3030, IdleSeconds: 1800, EvictInSeconds: 0, Spared: false},
			{WorkspaceID: "ws2", Server: "dev", Port: 3040, IdleSeconds: 60, EvictInSeconds: 540, Spared: true},
		},
	}
	var buf bytes.Buffer
	printBattery(&buf, resp)
	out := buf.String()
	for _, want := range []string{"battery", "ws1:dev", "ws2:dev", "spared"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
