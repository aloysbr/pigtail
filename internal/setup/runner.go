package setup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"testing"
	"time"
)

// Runner runs one external command. launchctl is only ever run through a
// Runner, so a test can hand in a fake and never touch the real launchd
// (TestLaunchctlOnlyInSetup guards that no other file execs it).
type Runner func(ctx context.Context, name string, args ...string) error

// ExecRunner runs the command for real. Under `go test` it refuses and runs
// nothing: a test that reaches it has forgotten its fake.
var ExecRunner Runner = func(ctx context.Context, name string, args ...string) error {
	if testing.Testing() {
		return errors.New("setup.ExecRunner refuses to run " + name + " under go test: inject a fake Runner")
	}
	return exec.CommandContext(ctx, name, args...).Run()
}

// Settle knobs of loadJob. launchd's default ExitTimeOut is 20 s: the wait for
// an old instance to leave the domain must outlast it.
const (
	settlePoll     = 250 * time.Millisecond
	settlePolls    = 100 // 25 s
	bootstrapTries = 3
)

// sleepCtx waits d or until ctx ends; tests replace it with a no-op.
var sleepCtx = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// jobLoaded reports whether launchd still holds the job in the domain.
func jobLoaded(ctx context.Context, run Runner, domain, label string) bool {
	return run(ctx, "launchctl", "print", domain+"/"+label) == nil
}

// waitUnloaded polls until the job has left the domain, bounded by
// settlePolls × settlePoll.
func waitUnloaded(ctx context.Context, run Runner, domain, label string) error {
	for i := 0; i < settlePolls; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !jobLoaded(ctx, run, domain, label) {
			return nil
		}
		if err := sleepCtx(ctx, settlePoll); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s/%s still loaded %s after bootout", domain, label, settlePoll*settlePolls)
}

// loadJob boots the job out (best effort) then bootstraps it once launchd
// has really let the old instance go. bootout answers before the old process
// has exited; a bootstrap in that window fails with « 5: Input/output error »
// while print still answers for the dying instance (reproduced 2026-10-31,
// ). So each round waits for the job to leave the domain,
// bootstraps, and counts as loaded only when bootstrap succeeded and print
// answers — never print alone after a failed bootstrap.
func loadJob(ctx context.Context, run Runner, domain, label, plist string) error {
	_ = run(ctx, "launchctl", "bootout", domain+"/"+label)
	var last error
	for try := 1; try <= bootstrapTries; try++ {
		if err := waitUnloaded(ctx, run, domain, label); err != nil {
			return err
		}
		last = run(ctx, "launchctl", "bootstrap", domain, plist)
		if last == nil && jobLoaded(ctx, run, domain, label) {
			return nil
		}
		if last == nil {
			last = fmt.Errorf("%s/%s not loaded after bootstrap", domain, label)
		}
	}
	return fmt.Errorf("bootstrap %s after %d tries: %w", label, bootstrapTries, last)
}

// unloadJob boots the job out.
func unloadJob(ctx context.Context, run Runner, domain, label string) error {
	return run(ctx, "launchctl", "bootout", domain+"/"+label)
}

func guiDomain() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("gui/%s", u.Uid), nil
}
