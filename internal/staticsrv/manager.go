// Package staticsrv runs built-in Go file servers inside the pigtail daemon.
//
// Unlike worktree dev servers (which are detached local processes), a static
// server is an http.FileServer goroutine owned by pigtaild itself, bound to
// 127.0.0.1 on the record's port. Records are durable (in the store); running
// state lives only here, in memory, and is never auto-restored on boot.
package staticsrv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runningServer is one live file server: its HTTP server, the listener it
// serves, and the port it bound (cached so List/Running need no lookup).
type runningServer struct {
	srv  *http.Server
	ln   net.Listener
	port int
}

// Manager owns the set of running static servers, keyed by static-server name
// (names are unique and user-facing). All access is guarded by mu.
type Manager struct {
	mu      sync.Mutex
	running map[string]*runningServer
}

// New constructs an empty Manager.
func New() *Manager {
	return &Manager{running: map[string]*runningServer{}}
}

// Start binds 127.0.0.1:<port> and serves http.FileServer(http.Dir(ss.Dir))
// in a goroutine. It errors if the server is already running, the directory is
// missing or not a directory, or the port cannot be bound.
func (m *Manager) Start(ss pigtail.StaticServer) error {
	if ss.Name == "" {
		return errors.New("staticsrv: name required")
	}
	if ss.Port <= 0 {
		return fmt.Errorf("staticsrv: invalid port %d", ss.Port)
	}
	info, err := os.Stat(ss.Dir)
	if err != nil {
		return fmt.Errorf("staticsrv: dir %q: %w", ss.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("staticsrv: %q is not a directory", ss.Dir)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.running[ss.Name]; ok {
		return fmt.Errorf("staticsrv: %q already running", ss.Name)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ss.Port))
	if err != nil {
		return fmt.Errorf("staticsrv: listen %d: %w", ss.Port, err)
	}

	srv := &http.Server{
		Handler:           http.FileServer(http.Dir(ss.Dir)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	rs := &runningServer{srv: srv, ln: ln, port: ss.Port}
	m.running[ss.Name] = rs

	go func() {
		// Serve returns ErrServerClosed on a clean Shutdown; that's expected.
		_ = srv.Serve(ln)
	}()
	return nil
}

// Stop gracefully shuts the named server down and removes it from the map.
// Errors if the server is not running.
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	rs, ok := m.running[name]
	if ok {
		delete(m.running, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("staticsrv: %q is not running", name)
	}
	return shutdown(rs)
}

// StopAll shuts down every running server. Used on daemon shutdown and by
// `stop --all`. Best-effort: it always drains the map.
func (m *Manager) StopAll() {
	m.mu.Lock()
	servers := m.running
	m.running = map[string]*runningServer{}
	m.mu.Unlock()
	for _, rs := range servers {
		_ = shutdown(rs)
	}
}

// Running reports the port a named server is bound to and whether it is up.
func (m *Manager) Running(name string) (port int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs, ok := m.running[name]
	if !ok {
		return 0, false
	}
	return rs.port, true
}

// List returns the names of all currently running servers.
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.running))
	for name := range m.running {
		out = append(out, name)
	}
	return out
}

// shutdown gracefully stops one running server with a short timeout.
func shutdown(rs *runningServer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := rs.srv.Shutdown(ctx)
	// Shutdown closes the listener; Close again is harmless and covers the
	// timeout path where Shutdown returned before the listener was released.
	_ = rs.ln.Close()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
