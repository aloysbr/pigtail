package cmux

import (
	"os"
	"strings"
	"testing"
)

func TestPingBoundedWithoutCmuxEnv(t *testing.T) {
	log := recordingCLI(t, "0", "PONG")
	t.Setenv("CMUX_SURFACE_ID", "X")
	if err := PingBounded(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "CMUX_SURFACE_ID") || !strings.Contains(string(b), "ARGS ping") {
		t.Fatalf("calls %q", b)
	}
}
