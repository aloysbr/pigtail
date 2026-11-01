package cmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// « Aller à la session »: bring the cmux tab
// of a session to the front, on the user's click in the reader. The fifth place
// pigtaild executes cmux (CLAUDE.md § cmux sidebar); the only daemon code
// that references Reveal is internal/daemon/handlers/reveal.go
// (TestRevealOnlyInRevealHandler).
//
// Probe of 2026-10-29 (cmux NIGHTLY 0.65.x, Finder frontmost): `select-workspace --workspace <uuid>
// --window <uuid>` alone brought cmux to the front and changed the selected
// workspace; `focus-window` does neither (0.64.24). The window is
// mandatory: every window-scoped cmux write acts on the key (or calling)
// window without it, and no window is key while another app is frontmost.
// `open -b com.cmuxterm.app` follows as a safety net only — harmless (it
// activates an app already running), not measured on its own, and it covers
// a cmux build where select-workspace would select without activating.

// revealTimeout bounds the whole reveal (the listing, the select, the open).
var revealTimeout = 3 * time.Second

// cmuxBundleID is the app `open -b` activates.
const cmuxBundleID = "com.cmuxterm.app"

// ErrRevealNotOpen: cmux has no tab with that workspace id (closed since the
// session registered). The listing answered, so cmux runs.
var ErrRevealNotOpen = errors.New("cmux: workspace not open")

// Revealer brings a workspace to the front. Its fields are the executor
// seams; nil means the real cmux CLI (run with no CMUX_* variable but the
// aimed socket, envWithoutCmux) and the real `open`.
type Revealer struct {
	Run  func(ctx context.Context, args ...string) ([]byte, error)
	Open func(ctx context.Context, bundleID string) error
}

// Reveal brings the cmux workspace (UUID) to the front through the real CLI.
func Reveal(workspaceID string) error { return Revealer{}.Reveal(workspaceID) }

// revealArgs is the select call; both targets by UUID.
func revealArgs(workspaceID, windowID string) []string {
	return []string{"select-workspace", "--workspace", workspaceID, "--window", windowID}
}

// Reveal finds the window holding the workspace (`tree --all --json`, one
// listing), selects it there, then activates the app. Every call shares one
// 3 s deadline.
func (r Revealer) Reveal(workspaceID string) error {
	if err := checkUUID("workspace id", workspaceID); err != nil {
		return err
	}
	run, open := r.Run, r.Open
	if run == nil {
		run = runRevealCLI
	}
	if open == nil {
		open = openApp
	}
	ctx, cancel := context.WithTimeout(context.Background(), revealTimeout)
	defer cancel()
	out, err := run(ctx, treeAllArgs()...)
	if err != nil {
		return err
	}
	windowID, err := windowOfWorkspace(out, workspaceID)
	if err != nil {
		return err
	}
	if err := checkUUID("window id", windowID); err != nil {
		return err
	}
	if _, err := run(ctx, revealArgs(workspaceID, windowID)...); err != nil {
		return err
	}
	return open(ctx, cmuxBundleID)
}

// windowOfWorkspace returns the id of the window whose workspaces include id
// (case-insensitive: cmux answers upper case, the registry stores upper case).
func windowOfWorkspace(b []byte, id string) (string, error) {
	var raw struct {
		Windows *[]struct {
			ID         string `json:"id"`
			Workspaces []struct {
				ID string `json:"id"`
			} `json:"workspaces"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return "", fmt.Errorf("cmux tree: unexpected output: %w", err)
	}
	if raw.Windows == nil {
		return "", errors.New("cmux tree: no windows array in output")
	}
	for _, win := range *raw.Windows {
		for _, w := range win.Workspaces {
			if strings.EqualFold(w.ID, id) {
				return win.ID, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s", ErrRevealNotOpen, id)
}

func runRevealCLI(ctx context.Context, args ...string) ([]byte, error) {
	cli, err := cliPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, cli, args...) //nolint:gosec // cli is the cmux binary; args are built and validated here
	cmd.Env = envWithoutCmux()
	out, err := cmd.Output()
	if err != nil {
		return out, runError(args[0], err)
	}
	return out, nil
}

func openApp(ctx context.Context, bundleID string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/open", "-b", bundleID)
	cmd.Env = envWithoutCmux()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open -b %s: %w: %s", bundleID, err, OneLine(string(out), stderrMaxRunes))
	}
	return nil
}
