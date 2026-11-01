package claudesession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// OriginCwd returns the directory the session was started in: the first
// recorded "cwd" whose encoded form is the transcript's slug; ok=false (no
// error) when none matches. The cwd moves when a session cds, and
// `claude --resume <id>` looks under projects/<encoded cwd>.
func OriginCwd(t Transcript) (string, bool, error) {
	f, err := os.Open(t.JSONLPath())
	if err != nil {
		return "", false, err
	}
	defer f.Close() //nolint:errcheck // read-only handle
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n') // no length limit: lines reach megabytes
		if bytes.Contains(line, []byte(`"cwd":"`)) {
			var e struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(line, &e) == nil && e.Cwd != "" && projectDirName(e.Cwd) == t.Slug {
				return e.Cwd, true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
	}
}

// projectDirName: Claude Code's projects/ name for a cwd — every byte that is
// not an ASCII letter or digit becomes '-' (measured). It checks a candidate
// against the real slug, it never derives one.
func projectDirName(cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// Covers reports whether head holds everything stale holds: stale's .jsonl
// is a byte-prefix of head's (equal included), and every regular file of
// stale's sidecar exists in head's sidecar, at the same relative path, as a
// byte-prefix of it. What head has beyond that is its own. A missing sidecar
// counts as an empty one. A copy head covers has nothing head lacks: removing
// it loses nothing. Files are streamed: transcripts reach 15 MB.
func Covers(head, stale Transcript) (bool, error) {
	ok, err := isPrefixFile(stale.JSONLPath(), head.JSONLPath())
	if err != nil || !ok {
		return false, err
	}
	files, err := sidecarFiles(stale.SidecarDir())
	if err != nil {
		return false, err
	}
	for _, rel := range files {
		hp := filepath.Join(head.SidecarDir(), rel)
		fi, err := os.Lstat(hp)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !fi.Mode().IsRegular() {
			return false, nil
		}
		if ok, err := isPrefixFile(filepath.Join(stale.SidecarDir(), rel), hp); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// isPrefixFile: the bytes of p are the first bytes of q.
func isPrefixFile(p, q string) (bool, error) {
	a, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer a.Close() //nolint:errcheck // read-only handle
	b, err := os.Open(q)
	if err != nil {
		return false, err
	}
	defer b.Close() //nolint:errcheck // read-only handle
	sa, err := a.Stat()
	if err != nil {
		return false, err
	}
	sb, err := b.Stat()
	if err != nil {
		return false, err
	}
	if sa.Size() > sb.Size() {
		return false, nil
	}
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for left := sa.Size(); left > 0; {
		n := int64(len(bufA))
		if left < n {
			n = left
		}
		if _, err := io.ReadFull(a, bufA[:n]); err != nil {
			return false, err
		}
		if _, err := io.ReadFull(b, bufB[:n]); err != nil {
			return false, err
		}
		if !bytes.Equal(bufA[:n], bufB[:n]) {
			return false, nil
		}
		left -= n
	}
	return true, nil
}

// sidecarFiles lists the regular files under root, relative, in lexical
// order; an absent root is an empty list.
func sidecarFiles(root string) ([]string, error) {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// ErrSessionBusy: another pigtail process holds the session's move lock.
var ErrSessionBusy = errors.New("another pigtail is moving this session")

// LockSession takes the per-session move lock, without waiting: a
// non-blocking flock on $XDG_STATE_HOME/pigtail/session-locks/<sid>.lock
// (default ~/.local/state). ErrSessionBusy when another process holds it.
// The kernel drops the lock with the process; the empty file stays.
func LockSession(sid string) (func(), error) {
	if sid == "" || filepath.Base(sid) != sid || sid == "." || sid == ".." {
		return nil, fmt.Errorf("lock session: invalid id %q", sid)
	}
	dir := filepath.Join(lockStateRoot(), "session-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lock session %s: %w", sid, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, sid+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock session %s: %w", sid, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrSessionBusy
		}
		return nil, fmt.Errorf("lock session %s: %w", sid, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// lockStateRoot is pigtail's state directory: $XDG_STATE_HOME/pigtail, or
// ~/.local/state/pigtail (temp dir without a home), as everywhere else.
func lockStateRoot() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "pigtail")
	}
	return filepath.Join(os.TempDir(), "pigtail-state")
}
