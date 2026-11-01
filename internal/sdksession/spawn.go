package sdksession

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aloysbr/pigtail/internal/usagerefresh"
)

// SpawnConfig says how the host is launched.
type SpawnConfig struct {
	Node, HostDir, Claude, SocketPath string
	// DefaultSocket: SocketPath is the daemon's default socket, so the host's
	// claude finds it without PIGTAIL_SOCKET.
	DefaultSocket bool
}

var dropPrefixes = []string{"CLAUDE", "CMUX_", "ANTHROPIC_", "PIGTAIL_"}

// Env is the environment of the host, and so of its claude (the host hands its
// own environment to query): environ without any inherited CLAUDE*, CMUX_*,
// ANTHROPIC_* or PIGTAIL_* variable nor PATH, plus a PATH cleaned of the cmux
// shim, CLAUDE_CONFIG_DIR, the marker PIGTAIL_SDK_SESSION and, off the default
// socket only, PIGTAIL_SOCKET. Unlike usagerefresh.Env it never sets
// CMUX_CLAUDE_HOOKS_DISABLED (CA10).
func Env(environ []string, cfg SpawnConfig, configDir, sessionID string) []string {
	var path, shimRoot string
	out := make([]string, 0, len(environ)+4)
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH":
			path = v
		case "CMUX_CLAUDE_WRAPPER_SHIM_ROOT":
			shimRoot = v
		}
		drop := k == "PATH"
		for _, p := range dropPrefixes {
			drop = drop || strings.HasPrefix(k, p)
		}
		if !drop {
			out = append(out, kv)
		}
	}
	out = append(out, "PATH="+usagerefresh.CleanPath(path, shimRoot), "CLAUDE_CONFIG_DIR="+configDir, "PIGTAIL_SDK_SESSION="+sessionID)
	if !cfg.DefaultSocket && cfg.SocketPath != "" {
		out = append(out, "PIGTAIL_SOCKET="+cfg.SocketPath)
	}
	return out
}

func writeAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Reservation is a session whose meta.json is written and whose host is not
// started yet. It already counts as live (a fresh meta.json is `starting`), so a
// caller holding the launch lock can reserve under it and start the host after.
type Reservation struct {
	r        *Registry
	cfg      SpawnConfig
	m        Meta
	dir      string
	metaPath string
}

// Reserve writes meta.json (version 1, host_pid 0) and returns the reservation.
func (r *Registry) Reserve(cfg SpawnConfig, m Meta) (*Reservation, error) {
	if !idRe.MatchString(m.ID) {
		return nil, fmt.Errorf("invalid sdk session id %q", m.ID)
	}
	dir := filepath.Join(r.Dir, m.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	m.Version = 1
	if m.Claude == "" {
		m.Claude = cfg.Claude
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = r.now().UTC()
	}
	m.HostPID = 0
	rv := &Reservation{r: r, cfg: cfg, m: m, dir: dir, metaPath: filepath.Join(dir, "meta.json")}
	if err := writeAtomic(rv.metaPath, m); err != nil {
		return nil, err
	}
	return rv, nil
}

// Start starts the host through start (which the caller provides so tests never
// fork a real one) and records its pid in meta.json. The host runs in its own
// session, stdin on /dev/null, stdout and stderr in host.out and host.err. A
// start failure leaves the session failed with the reason in host.err and the
// error returned.
func (rv *Reservation) Start(start func(*exec.Cmd) error) (Session, error) {
	m, cfg := rv.m, rv.cfg
	logs := [2]*os.File{}
	for i, name := range []string{"host.out", "host.err"} {
		f, err := os.OpenFile(filepath.Join(rv.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return Session{}, err
		}
		defer func() { _ = f.Close() }()
		logs[i] = f
	}
	cmd := exec.Command(cfg.Node, "--experimental-strip-types", "--no-warnings", filepath.Join(cfg.HostDir, "main.ts"), rv.dir) //nolint:gosec // paths are the daemon's own flags
	cmd.Env = Env(os.Environ(), cfg, m.ConfigDir, m.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = logs[0], logs[1]
	if err := start(cmd); err != nil {
		_, _ = fmt.Fprintf(logs[1], "pigtaild: host not started: %v\n", err)
		m.HostPID = -1
		_ = writeAtomic(rv.metaPath, m)
		s, _ := rv.r.Get(m.ID)
		return s, fmt.Errorf("start sdk host: %w", err)
	}
	if cmd.Process != nil {
		m.HostPID = cmd.Process.Pid
		// Reap the host when it exits: a zombie would still answer kill -0 and
		// keep the session looking alive.
		go func() { _ = cmd.Wait() }()
	}
	if err := writeAtomic(rv.metaPath, m); err != nil {
		return Session{}, err
	}
	return rv.r.Get(m.ID)
}

// Spawn is Reserve then Start.
func (r *Registry) Spawn(cfg SpawnConfig, m Meta, start func(*exec.Cmd) error) (Session, error) {
	rv, err := r.Reserve(cfg, m)
	if err != nil {
		return Session{}, err
	}
	return rv.Start(start)
}
