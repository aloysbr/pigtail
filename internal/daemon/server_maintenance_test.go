package daemon

import "testing"

func TestDaemonMaintenanceDirRule(t *testing.T) {
	cfg := Config{SocketPath: "/s/pigtail.sock"}
	if got := daemonMaintenanceDir(cfg, func(string) string { return "" }); got != "/s" {
		t.Fatalf("default: %q", got)
	}
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": "/m"}
	if got := daemonMaintenanceDir(cfg, func(k string) string { return env[k] }); got != "/m" {
		t.Fatalf("env: %q", got)
	}
}
