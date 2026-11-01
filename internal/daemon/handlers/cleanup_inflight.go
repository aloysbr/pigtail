package handlers

import "sync"

// cleanupInflight tracks the workspaces whose teardown is currently running.
//
// Why this exists: a teardown is now allowed to take minutes rather than the
// old ten seconds, which makes it easy for a user to launch a second one
// against the same workspace — Ctrl-C then retry, or the CLI and the TUI at
// the same time. Two concurrent `git worktree remove` on one path race on the
// same .git/worktrees bookkeeping and fail in ways nobody can read. Refusing
// the second call is both safer and clearer.
//
// The zero value is ready to use.
type cleanupInflight struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

// acquire claims the teardown slot for id. It returns false when another
// teardown of the same workspace is already running.
func (c *cleanupInflight) acquire(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ids == nil {
		c.ids = make(map[string]struct{})
	}
	if _, busy := c.ids[id]; busy {
		return false
	}
	c.ids[id] = struct{}{}
	return true
}

// release frees the slot. Always call it from a defer: a teardown that
// returned an error must still be retryable.
func (c *cleanupInflight) release(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.ids, id)
}
