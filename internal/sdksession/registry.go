// Package sdksession reads the files the SDK host writes (clients/sdkhost,
// docs/wire-protocol.md § SDK sessions) and derives the state of a SDK
// session from them and from the processes alive: it survives a pigtaild
// restart because nothing but those files is stored.
package sdksession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// reservationWindow is how long a meta.json with no host.json and no live host
// pid still counts as a launch under way.
const reservationWindow = 30 * time.Second

// stderrTail bounds the part of host.err quoted in a failure reason.
const stderrTail = 600

// Meta is meta.json: written by pigtaild before the host starts (host_pid is
// added once after the spawn).
type Meta struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	SubjectID   string    `json:"subject_id"`
	Cwd         string    `json:"cwd"`
	ConfigDir   string    `json:"config_dir"`
	Claude      string    `json:"claude"`
	Mode        string    `json:"mode"`
	Prompt      string    `json:"prompt"`
	SessionUUID string    `json:"session_uuid"`
	HostPID     int       `json:"host_pid"` // 0 before the spawn, -1 when the host could not start
	CreatedAt   time.Time `json:"created_at"`
}

// Request is one pending question or permission of the host.
type Request struct {
	Rid      string          `json:"rid"`
	Kind     string          `json:"kind"` // question | permission
	Tool     string          `json:"tool"`
	Input    json.RawMessage `json:"input"`
	OpenedAt time.Time       `json:"opened_at"`
}

// HostFile is host.json: written by the host alone.
type HostFile struct {
	Version         int       `json:"version"`
	HostPID         int       `json:"host_pid"`
	ClaudePIDs      []int     `json:"claude_pids"`
	ClaudeSessionID string    `json:"claude_session_id"`
	State           string    `json:"state"`
	Turn            int       `json:"turn"`
	Requests        []Request `json:"requests"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Session is the derived view served to callers.
type Session struct {
	ID              string    `json:"id"`
	SubjectID       string    `json:"subject_id"`
	Cwd             string    `json:"cwd"`
	ConfigDir       string    `json:"config_dir"`
	ClaudeSessionID string    `json:"claude_session_id"`
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"` // why failed or lost
	ResumeCommand   string    `json:"resume_command,omitempty"`
	HostPID         int       `json:"host_pid"`
	ClaudePIDs      []int     `json:"claude_pids"`
	Turn            int       `json:"turn"`
	Pending         []Request `json:"pending"`
	// Live: a writer exists (the host, a claude, or a launch under way).
	Live      bool      `json:"live"`
	StartedAt time.Time `json:"started_at"`
}

// Registry reads <Dir>/<id>/. Every function field has a real default when nil.
type Registry struct {
	Dir    string
	Alive  func(pid int) bool
	Procs  func() ([]claudesession.Proc, error)
	Native func(cfg string, pid int) (claudesession.Native, bool)
	Kill   func(pid int) error
	Now    func() time.Time
}

// DefaultDir is $XDG_STATE_HOME/pigtail/sdk, else ~/.local/state/pigtail/sdk
// (the rule of spawnreg.DefaultPath).
func DefaultDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pigtail", "sdk")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "state", "pigtail", "sdk")
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ErrNotFound is returned for an unknown or malformed session id.
var ErrNotFound = errors.New("no such sdk session")

func (r *Registry) alive(pid int) bool {
	if r.Alive != nil {
		return r.Alive(pid)
	}
	return claudesession.PidAlive(pid)
}

func (r *Registry) procs() []claudesession.Proc {
	f := r.Procs
	if f == nil {
		f = claudesession.ClaudeProcesses
	}
	p, err := f()
	if err != nil {
		return nil
	}
	return p
}

func (r *Registry) native(cfg string, pid int) (claudesession.Native, bool) {
	if r.Native != nil {
		return r.Native(cfg, pid)
	}
	return claudesession.ReadNative(cfg, pid)
}

func (r *Registry) kill(pid int) error {
	if r.Kill != nil {
		return r.Kill(pid)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func terminal(state string) bool { return state == "ended" || state == "failed" }

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (r *Registry) readMeta(id string) (Meta, error) {
	if !idRe.MatchString(id) {
		return Meta{}, ErrNotFound
	}
	var m Meta
	if err := readJSON(filepath.Join(r.Dir, id, "meta.json"), &m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Meta{}, ErrNotFound
		}
		return Meta{}, fmt.Errorf("sdk session %s: %w", id, err)
	}
	if m.ID == "" {
		m.ID = id
	}
	return m, nil
}

// view derives a Session without side effect; orphans lists the claude pids
// whose identity is proven and that Settle may kill.
func (r *Registry) view(m Meta) (s Session, orphans []int) {
	s = Session{ID: m.ID, SubjectID: m.SubjectID, Cwd: m.Cwd, ConfigDir: m.ConfigDir,
		HostPID: m.HostPID, StartedAt: m.CreatedAt, Pending: []Request{}, ClaudePIDs: []int{}}
	dir := filepath.Join(r.Dir, m.ID)
	var h HostFile
	hErr := readJSON(filepath.Join(dir, "host.json"), &h)
	hasHost := hErr == nil
	hostPID := m.HostPID
	if hasHost && h.HostPID > 0 {
		hostPID = h.HostPID
	}
	s.HostPID = hostPID
	hostAlive := hostPID > 0 && r.alive(hostPID)

	if !hasHost {
		switch {
		case m.HostPID < 0: // Spawn marks a host that could not be started
			s.State = "failed"
			s.Reason = "host not started"
			if tail := fileTail(filepath.Join(dir, "host.err")); tail != "" {
				s.Reason += ": " + tail
			}
		case hostAlive || r.now().Sub(m.CreatedAt) < reservationWindow:
			s.State, s.Live = "starting", true
		default:
			s.State = "failed"
			s.Reason = "host exited before it wrote its state"
			if tail := fileTail(filepath.Join(dir, "host.err")); tail != "" {
				s.Reason += ": " + tail
			}
		}
		return s, nil
	}

	s.ClaudeSessionID, s.Turn, s.State = h.ClaudeSessionID, h.Turn, h.State
	if h.ClaudePIDs != nil {
		s.ClaudePIDs = h.ClaudePIDs
	}
	if hostAlive {
		s.Live = !terminal(h.State)
		if s.Live && h.Requests != nil {
			s.Pending = h.Requests
		}
		return s, nil
	}

	// The host is dead: only a claude it left behind can still write.
	if !terminal(h.State) {
		s.State = "lost"
		s.Reason = "host exited while the session was " + h.State
	}
	procs := r.procs()
	var unproven []int
	for _, pid := range h.ClaudePIDs {
		writer, proven := r.claudeWriter(m, h, pid, procs)
		switch {
		case proven:
			orphans = append(orphans, pid)
		case writer:
			unproven = append(unproven, pid)
		}
	}
	if len(orphans) > 0 || len(unproven) > 0 {
		s.Live = true
	}
	if len(unproven) > 0 {
		s.State = "lost"
		s.Reason = fmt.Sprintf("host is gone and claude pid %v is still running, its identity is not proven", unproven)
	}
	if !s.Live {
		s.ResumeCommand = resumeCommand(m, h.ClaudeSessionID)
	}
	return s, orphans
}

// Get returns one session as it reads now, no process touched.
func (r *Registry) Get(id string) (Session, error) {
	m, err := r.readMeta(id)
	if err != nil {
		return Session{}, err
	}
	s, _ := r.view(m)
	return s, nil
}

// Settle is Get after the orphans are dealt with: a claude left behind by a dead
// host whose identity is proven is killed, then the resume command is offered.
// A claude that cannot be proven is never killed and stays a live writer.
func (r *Registry) Settle(id string) (Session, error) {
	m, err := r.readMeta(id)
	if err != nil {
		return Session{}, err
	}
	s, orphans := r.view(m)
	if len(orphans) == 0 {
		return s, nil
	}
	for _, pid := range orphans {
		if err := r.kill(pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			return s, fmt.Errorf("kill orphan claude %d: %w", pid, err)
		}
	}
	// SIGTERM is asked, not obeyed: a claude still alive keeps the session
	// live and no resume command is offered until it is gone (CA7).
	s2, _ := r.view(m)
	return s2, nil
}

// List returns the sessions of one subject, or all of them with "".
func (r *Registry) List(subjectID string) ([]Session, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Session{}, nil
		}
		return nil, err
	}
	out := []Session{}
	var unreadable []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := r.readMeta(e.Name())
		if err != nil {
			// A directory with no meta.json is no session; one whose meta
			// cannot be read or parsed has an unknown subject and state:
			// reported, never skipped (CA3 fail-safe).
			if !errors.Is(err, ErrNotFound) {
				unreadable = append(unreadable, err.Error())
			}
			continue
		}
		if subjectID != "" && m.SubjectID != subjectID {
			continue
		}
		s, _ := r.view(m)
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(unreadable) > 0 {
		return out, fmt.Errorf("unreadable sdk session metadata: %s", strings.Join(unreadable, "; "))
	}
	return out, nil
}

// LiveForSubject reports a session of the subject that still has a writer.
// A registry that cannot be read reads as live (fail-safe, CA3): the id then
// says why ("unreadable registry: …").
func (r *Registry) LiveForSubject(subjectID string) (string, bool) {
	list, err := r.List(subjectID)
	if err != nil {
		return "unreadable registry: " + err.Error(), true
	}
	for _, s := range list {
		if s.Live {
			return s.ID, true
		}
	}
	return "", false
}

// SettleSubject settles every non-terminal session of the subject.
func (r *Registry) SettleSubject(subjectID string) error {
	list, err := r.List(subjectID)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range list {
		if terminal(s.State) {
			continue
		}
		if _, err := r.Settle(s.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Events returns the journal lines whose seq is above after. A torn last line
// (the host is mid-write) is ignored.
func (r *Registry) Events(id string, after int) ([]json.RawMessage, error) {
	if _, err := r.readMeta(id); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(r.Dir, id, "events.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []json.RawMessage{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := []json.RawMessage{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		var head struct {
			Seq int `json:"seq"`
		}
		if len(line) == 0 || json.Unmarshal(line, &head) != nil {
			continue
		}
		if head.Seq > after {
			out = append(out, json.RawMessage(append([]byte(nil), line...)))
		}
	}
	return out, sc.Err()
}

// fileTail is the last stderrTail bytes of a file, trimmed; "" when unreadable.
func fileTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > stderrTail {
		b = b[len(b)-stderrTail:]
	}
	return strings.TrimSpace(string(b))
}
