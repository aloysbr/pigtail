package main

import (
	"fmt"
	"strings"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// relaySlot decides whether a --relay spawn takes over from the caller (D3·A).
// The workspace is the calling Claude's own (its process environment: this
// pigtail's env can be set by anyone on the command line); it relays only if it
// is a live spawned workspace whose only Claude is the caller. nothing != "":
// the reason it relays nothing (the spawn goes on as a plain one).
func relaySlot(relay bool, callerEnv string, callerPID int, envWS string, pids map[string][]int, liveSpawned []spawnreg.Entry) (ws, nothing string, err error) {
	if !relay {
		return "", "", nil
	}
	ws, _ = cmux.EnvValue(callerEnv, cmux.EnvWorkspaceID)
	ws = strings.ToUpper(ws)
	if ws == "" {
		return "", "", fmt.Errorf("--relay: the calling Claude (pid %d) runs outside cmux — nothing to relay from", callerPID)
	}
	if !strings.EqualFold(ws, envWS) {
		return "", "", fmt.Errorf("--relay: $%s of this pigtail (%s) is not the calling Claude's (%s) — refusing to relay from another workspace", cmux.EnvWorkspaceID, envWS, ws)
	}
	spawned := false
	for _, e := range liveSpawned {
		spawned = spawned || e.WorkspaceID == ws
	}
	if !spawned {
		return "", fmt.Sprintf("workspace %s was not opened by pigtail session spawn", ws), nil
	}
	if p := pids[ws]; len(p) != 1 || p[0] != callerPID {
		return "", fmt.Sprintf("workspace %s runs %d Claude processes (pid %v), the relay is not the caller's alone", ws, len(p), p), nil
	}
	return ws, "", nil
}
