// Package setup implements the pigtail setup/teardown lifecycle.
package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type PrereqResult struct {
	Name    string
	Found   bool
	Path    string
	HelpMsg string
}

func CheckPrereqs() []PrereqResult {
	prereqs := []struct {
		name    string
		helpMac string
	}{
		{"git", "Install Xcode CLT: xcode-select --install"},
		{"tmux", "Install via Homebrew: brew install tmux"},
	}
	results := make([]PrereqResult, len(prereqs))
	for i, p := range prereqs {
		path, err := exec.LookPath(p.name)
		results[i] = PrereqResult{
			Name:    p.name,
			Found:   err == nil,
			Path:    path,
			HelpMsg: p.helpMac,
		}
	}
	return results
}

func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pigtail")
}

func EnsureConfigDir() (string, error) {
	dir := ConfigDir()
	return dir, os.MkdirAll(dir, 0o700)
}

func WaitForDaemon(socketPath string, maxRetries int, interval time.Duration) error {
	c := pigtail.NewSocketClient(socketPath)
	var lastErr error
	for i := range maxRetries {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := c.Workspaces(ctx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if i < maxRetries-1 {
			time.Sleep(interval)
		}
	}
	return fmt.Errorf("daemon not responding after %d attempts: %w", maxRetries, lastErr)
}

func ResolvePigtaildPath() string {
	if p, err := exec.LookPath("pigtaild"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "pigtaild")
}
