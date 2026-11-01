package pigtail

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSessionLaunchWireShape pins the lot 3 contract: every key is always
// present, empty strings included.
func TestSessionLaunchWireShape(t *testing.T) {
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	l := SessionLaunch{
		LaunchID:       "ln_1a2b3c4d5e6f7a8b",
		SubjectID:      "sub_x",
		State:          LaunchPending,
		Origin:         "api",
		WorkspaceID:    "6F0C",
		WorkspaceRef:   "workspace:12",
		Cell:           "mp",
		PermissionMode: "auto",
		Cwd:            "/tmp/x",
		StartedAt:      at,
		UpdatedAt:      at.Add(3 * time.Second),
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"launch_id":"ln_1a2b3c4d5e6f7a8b","subject_id":"sub_x","state":"pending","cause":"","origin":"api","workspace_id":"6F0C","workspace_ref":"workspace:12","cell":"mp","permission_mode":"auto","cwd":"/tmp/x","tab_title":"","started_at":"2026-10-25T10:00:00Z","updated_at":"2026-10-25T10:00:03Z","deduplicated":false}`
	if string(b) != want {
		t.Fatalf("wire shape:\n got %s\nwant %s", b, want)
	}
	var empty map[string]any
	b, _ = json.Marshal(SessionLaunch{})
	if err := json.Unmarshal(b, &empty); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"launch_id", "subject_id", "state", "cause", "origin", "workspace_id", "workspace_ref", "cell", "permission_mode", "cwd", "tab_title", "started_at", "updated_at", "deduplicated"} {
		if _, ok := empty[k]; !ok {
			t.Errorf("key %q missing from a zero SessionLaunch", k)
		}
	}
	if LaunchLaunched != "launched" || LaunchFailed != "failed" || LaunchPending != "pending" {
		t.Error("launch states drifted from the contract")
	}
	if EventSessionLaunchChanged != "session.launch_changed" {
		t.Errorf("event = %q", EventSessionLaunchChanged)
	}
}

func TestLaunchErrorCodes(t *testing.T) {
	for code, want := range map[ErrorCode]string{
		ErrSessionLive:        "SESSION_LIVE",
		ErrLaunchInProgress:   "LAUNCH_IN_PROGRESS",
		ErrLaunchBurst:        "LAUNCH_BURST",
		ErrNoCell:             "NO_CELL",
		ErrBypassRefused:      "BYPASS_REFUSED",
		ErrCellModeUnreadable: "CELL_MODE_UNREADABLE",
		ErrNoLaunchDir:        "NO_LAUNCH_DIR",
		ErrCmuxUnreachable:    "CMUX_UNREACHABLE",
		ErrCmuxCreateFailed:   "CMUX_CREATE_FAILED",
		ErrLiveUnknown:        "LIVE_UNKNOWN",
		ErrRegistryUnreadable: "REGISTRY_UNREADABLE",
	} {
		if string(code) != want {
			t.Errorf("%q != %q", code, want)
		}
	}
}
