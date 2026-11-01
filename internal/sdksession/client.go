package sdksession

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"time"
)

// dialTimeout bounds one command to a host: connect, write, one reply line.
const dialTimeout = 5 * time.Second

// SockPath is the host's Unix socket of session id.
func (r *Registry) SockPath(id string) string { return filepath.Join(r.Dir, id, "host.sock") }

// HostReply is the host's one-line answer to a command.
type HostReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Dial sends one NDJSON command to the host of session id and reads its reply.
// An error means the host could not be reached or did not answer; a refusal by
// the host is HostReply{OK:false}.
func (r *Registry) Dial(id string, cmd any) (HostReply, error) {
	if !idRe.MatchString(id) {
		return HostReply{}, ErrNotFound
	}
	conn, err := net.DialTimeout("unix", r.SockPath(id), dialTimeout)
	if err != nil {
		return HostReply{}, fmt.Errorf("sdk host of %s unreachable: %w", id, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	b, err := json.Marshal(cmd)
	if err != nil {
		return HostReply{}, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return HostReply{}, fmt.Errorf("write to sdk host of %s: %w", id, err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return HostReply{}, fmt.Errorf("read from sdk host of %s: %w", id, err)
	}
	var rep HostReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return HostReply{}, fmt.Errorf("sdk host of %s: bad reply: %w", id, err)
	}
	return rep, nil
}
