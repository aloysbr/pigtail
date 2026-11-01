package setup

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
)

const watchdogLabel = "sh.pigtail.watchdog"

// WatchdogLabel is the LaunchAgent label of the pigtaild watchdog (not any other
// LaunchAgent of the user's).
func WatchdogLabel() string { return watchdogLabel }

// WatchdogPlistPath is where the watchdog's LaunchAgent plist lives.
func WatchdogPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", watchdogLabel+".plist")
}

// Kickstart restarts the pigtaild job (`launchctl kickstart -k`) through run.
func Kickstart(ctx context.Context, run Runner) error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	return run(ctx, "launchctl", "kickstart", "-k", domain+"/"+plistLabel)
}

// GenerateWatchdogPlist renders the LaunchAgent that runs
// `pigtail daemon watchdog` every 60 s.
func GenerateWatchdogPlist(pigtailBin, configDir string) (string, error) {
	if pigtailBin == "" || configDir == "" {
		return "", errors.New("watchdog plist: pigtail path and config dir are required")
	}
	home, _ := os.UserHomeDir()
	pathEnv := filepath.Join(home, ".local", "bin") + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	e := html.EscapeString
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>watchdog</string>
    <string>--db</string>
    <string>%s/pigtail.db</string>
  </array>
  <key>StartInterval</key>
  <integer>60</integer>
  <key>RunAtLoad</key>
  <false/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>%s/watchdog.log</string>
  <key>StandardErrorPath</key>
  <string>%s/watchdog.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
</dict>
</plist>
`, watchdogLabel, e(pigtailBin), e(configDir), e(configDir), e(configDir), e(pathEnv)), nil
}

// InstallWatchdogStep writes the watchdog plist and (re)loads its job.
func InstallWatchdogStep(run Runner, bin, configDir string) error {
	content, err := GenerateWatchdogPlist(bin, configDir)
	if err != nil {
		return err
	}
	dest := WatchdogPlistPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return err
	}
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	return loadJob(context.Background(), run, domain, watchdogLabel, dest)
}

// TeardownWatchdogStep boots the job out and removes its plist; a missing
// plist is not an error.
func TeardownWatchdogStep(run Runner) error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	berr := unloadJob(context.Background(), run, domain, watchdogLabel)
	if err := os.Remove(WatchdogPlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = berr // a job that was not loaded makes bootout fail: nothing to undo
	return nil
}

// ResolvePigtailPath is where the pigtail CLI lives for a LaunchAgent:
// PATH first, else ~/.local/bin/pigtail. Never os.Executable(), which may
// point into a worktree deleted after its merge.
func ResolvePigtailPath() string {
	if p, err := exec.LookPath("pigtail"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "pigtail")
}
