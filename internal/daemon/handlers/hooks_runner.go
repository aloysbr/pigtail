package handlers

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// HookRunner runs post-create hooks on the daemon's own lifetime instead of a
// request's: a client that gives up (timeout, Ctrl-C) no longer kills them.
// The daemon cancels ctx when it stops and waits for the runs, bounded.
type HookRunner struct {
	ctx context.Context
	wg  sync.WaitGroup
}

// NewHookRunner returns a runner whose hooks stop when ctx is cancelled.
func NewHookRunner(ctx context.Context) *HookRunner { return &HookRunner{ctx: ctx} }

// Go runs fn on its own goroutine with the runner's context. A nil runner
// (handler tests) runs it on context.Background(), untracked.
func (h *HookRunner) Go(fn func(ctx context.Context)) {
	if h == nil {
		go fn(context.Background())
		return
	}
	h.wg.Add(1)
	go func() { defer h.wg.Done(); fn(h.ctx) }()
}

// Wait waits for every run started by Go, at most d; false = some still run.
func (h *HookRunner) Wait(d time.Duration) bool {
	if h == nil {
		return true
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// runPostCreateHooks records the hooks of a new workspace as running, then
// starts them (startHooks).
func runPostCreateHooks(svc *Services, ws *pigtail.Workspace, hooks []pigtail.HookSpec) <-chan error {
	if err := svc.Store.SetWorkspaceHooks(ws.ID, pigtail.HooksResult{Status: pigtail.HooksRunning,
		Log: devserver.LogPath(ws.Path, "hooks"), At: time.Now().UTC()}); err != nil {
		svc.logf("hooks of %s: record %s: %v", ws.ID, pigtail.HooksRunning, err)
	}
	return startHooks(svc, ws.ID, ws.Path, hooks)
}

// startHooks runs the hook sequence of workspace id at path, detached from
// any request and bounded by devserver.HooksTimeout, and records its outcome
// (ok or failed) whatever happens to the client. The returned channel
// (buffered) receives its result whether or not anybody still waits.
func startHooks(svc *Services, id, path string, hooks []pigtail.HookSpec) <-chan error {
	out := make(chan error, 1)
	logPath := devserver.LogPath(path, "hooks")
	record := func(r pigtail.HooksResult) {
		r.Log, r.At = logPath, time.Now().UTC()
		if err := svc.Store.SetWorkspaceHooks(id, r); err != nil {
			svc.logf("hooks of %s: record %s: %v", id, r.Status, err)
		}
	}
	svc.Hooks.Go(func(ctx context.Context) {
		hctx, cancel := context.WithTimeout(ctx, devserver.HooksTimeout)
		defer cancel()
		err := devserver.RunHooks(hctx, path, hooks)
		if err == nil {
			record(pigtail.HooksResult{Status: pigtail.HooksOK})
		} else {
			r := pigtail.HooksResult{Status: pigtail.HooksFailed, Error: err.Error()}
			var he *devserver.HookError
			if errors.As(err, &he) {
				r.Hook = he.Hook
			}
			record(r)
		}
		out <- err
	})
	return out
}
