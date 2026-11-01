package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrAnotherInstance is returned by AcquireInstanceLock when another pigtaild
// already serves the same database.
var ErrAnotherInstance = errors.New("another pigtaild already serves this database")

type anotherInstanceError struct {
	pid    int
	dbPath string
}

func (e *anotherInstanceError) Error() string {
	who := "unknown pid"
	if e.pid > 0 {
		who = "pid " + strconv.Itoa(e.pid)
	}
	return fmt.Sprintf("another pigtaild (%s) already serves %s — stop it, or start this one with -db <other path>", who, e.dbPath)
}

func (e *anotherInstanceError) Is(target error) bool { return target == ErrAnotherInstance }

// AcquireInstanceLock takes an exclusive, non-blocking flock on <dbPath>.lock
// and writes this process's pid into it. The kernel drops the lock when the
// process dies, so a crash never leaves a stale lock. Call it before opening
// the store and before binding any socket.
func AcquireInstanceLock(dbPath string) (release func(), err error) {
	lockPath := dbPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer func() { _ = f.Close() }()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid := 0
			if b, rerr := os.ReadFile(lockPath); rerr == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			return nil, &anotherInstanceError{pid: pid, dbPath: dbPath}
		}
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// listenTCP binds the HTTP address; a failure names the usual cause.
func listenTCP(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen tcp: %w — port in use: is another pigtaild running? pass --http 127.0.0.1:<port>", err)
	}
	return ln, nil
}
