package pigtail

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Version keeps its three-value signature (cmd/pigtail/rename.go calls it);
// VersionInfo adds the build identity.
func TestVersionClientKeepsLegacySignature(t *testing.T) {
	c := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"daemon_version": "v1.2.3",
			"daemon_branch":  "main",
			"daemon_commit":  "abc",
			"daemon_dirty":   "false",
			"supported_apis": []string{"v1"},
		}})
	}))
	ver, apis, err := c.Version(context.Background())
	if err != nil || ver != "v1.2.3" || len(apis) != 1 || apis[0] != "v1" {
		t.Fatalf("Version = (%q, %v, %v)", ver, apis, err)
	}
	vi, err := c.VersionInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := VersionInfo{DaemonVersion: "v1.2.3", DaemonBranch: "main", DaemonCommit: "abc", DaemonDirty: "false", SupportedAPIs: []string{"v1"}}
	if vi.DaemonVersion != want.DaemonVersion || vi.DaemonBranch != want.DaemonBranch ||
		vi.DaemonCommit != want.DaemonCommit || vi.DaemonDirty != want.DaemonDirty ||
		len(vi.SupportedAPIs) != 1 || vi.SupportedAPIs[0] != "v1" {
		t.Fatalf("VersionInfo = %+v, want %+v", vi, want)
	}
}
