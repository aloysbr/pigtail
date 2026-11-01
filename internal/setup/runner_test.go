package setup

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerRefusesUnderTest(t *testing.T) {
	err := ExecRunner(context.Background(), "launchctl", "list")
	if err == nil || !strings.Contains(err.Error(), "refuses") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

type recorded struct {
	name string
	args []string
}

// fakeRunner is a minimal launchd: bootstrap loads a label, bootout unloads
// it, print answers nil only while it is loaded. Every call is recorded.
func fakeRunner(rec *[]recorded) Runner {
	loaded := map[string]bool{}
	return func(_ context.Context, name string, args ...string) error {
		*rec = append(*rec, recorded{name, args})
		switch args[0] {
		case "bootstrap":
			loaded[args[1]+"/"+labelOf(args[2])] = true
		case "bootout":
			if !loaded[args[1]] {
				return errors.New("boot-out failed: 3: no such process")
			}
			delete(loaded, args[1])
		case "print":
			if !loaded[args[1]] {
				return errors.New("could not find service")
			}
		}
		return nil
	}
}

// labelOf derives a label from a plist path (/p/sh.x.plist → sh.x).
func labelOf(plist string) string {
	return strings.TrimSuffix(filepath.Base(plist), ".plist")
}

// verbs keeps the launchctl verb of each call, prints left out.
func verbs(rec []recorded) []string {
	var v []string
	for _, r := range rec {
		if r.args[0] != "print" {
			v = append(v, r.args[0])
		}
	}
	return v
}

// noSleep makes the settle polls instant for the test's duration.
func noSleep(t *testing.T) {
	t.Helper()
	old := sleepCtx
	sleepCtx = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { sleepCtx = old })
}

func TestLoadUnloadJobArgs(t *testing.T) {
	noSleep(t)
	var rec []recorded
	if err := loadJob(context.Background(), fakeRunner(&rec), "gui/501", "sh.x", "/p/sh.x.plist"); err != nil {
		t.Fatal(err)
	}
	want := []recorded{
		{"launchctl", []string{"bootout", "gui/501/sh.x"}},
		{"launchctl", []string{"print", "gui/501/sh.x"}},
		{"launchctl", []string{"bootstrap", "gui/501", "/p/sh.x.plist"}},
		{"launchctl", []string{"print", "gui/501/sh.x"}},
	}
	if !reflect.DeepEqual(rec, want) {
		t.Fatalf("load = %v", rec)
	}
	rec = nil
	if err := unloadJob(context.Background(), fakeRunner(&rec), "gui/501", "sh.x"); err == nil {
		t.Fatal("unload of a job this fake never loaded: want bootout's error")
	}
	if !reflect.DeepEqual(rec, want[:1]) {
		t.Fatalf("unload = %v", rec)
	}
}

// dyingLaunchd reproduces launchd after a bootout of a running job
// (2026-10-31 repro on a throwaway job): bootout answers 0 at once, the old
// instance stays in the domain for `dying` more prints, and a bootstrap while
// it is still there fails with exit 5. failFirst makes the first bootstrap
// fail even once the job is gone (a transient refusal).
type dyingLaunchd struct {
	loaded     bool
	dying      int
	failFirst  bool
	bootstraps int
	prints     int
}

var errBootstrap5 = errors.New("bootstrap failed: 5: input/output error")

func (l *dyingLaunchd) run(_ context.Context, _ string, args ...string) error {
	switch args[0] {
	case "bootout":
		if l.loaded && l.dying == 0 {
			l.loaded = false
		}
		return nil
	case "print":
		l.prints++
		if l.loaded {
			return nil
		}
		if l.dying > 0 {
			l.dying--
			return nil
		}
		return errors.New("could not find service")
	case "bootstrap":
		l.bootstraps++
		if l.loaded || l.dying > 0 {
			return errBootstrap5
		}
		if l.failFirst && l.bootstraps == 1 {
			return errBootstrap5
		}
		l.loaded = true
		return nil
	}
	return errors.New("unexpected " + args[0])
}

func TestLoadJobWaitsForBootoutToSettle(t *testing.T) {
	noSleep(t)
	l := &dyingLaunchd{dying: 3}
	if err := loadJob(context.Background(), l.run, "gui/501", "sh.x", "/p/sh.x.plist"); err != nil {
		t.Fatalf("loadJob = %v, want the job loaded once the old instance is gone", err)
	}
	if !l.loaded || l.bootstraps != 1 {
		t.Fatalf("loaded=%v bootstraps=%d, want loaded after exactly one bootstrap", l.loaded, l.bootstraps)
	}
}

func TestLoadJobRetriesFailedBootstrap(t *testing.T) {
	noSleep(t)
	l := &dyingLaunchd{failFirst: true}
	if err := loadJob(context.Background(), l.run, "gui/501", "sh.x", "/p/sh.x.plist"); err != nil {
		t.Fatalf("loadJob = %v, want a second round to load it", err)
	}
	if !l.loaded || l.bootstraps != 2 {
		t.Fatalf("loaded=%v bootstraps=%d, want 2", l.loaded, l.bootstraps)
	}
}

func TestLoadJobGivesUpWhenJobNeverLeaves(t *testing.T) {
	noSleep(t)
	l := &dyingLaunchd{dying: 1 << 30}
	err := loadJob(context.Background(), l.run, "gui/501", "sh.x", "/p/sh.x.plist")
	if err == nil || !strings.Contains(err.Error(), "sh.x") {
		t.Fatalf("err = %v, want an error naming the label", err)
	}
	if l.bootstraps != 0 {
		t.Fatalf("bootstraps = %d, want none while the old instance is still loaded", l.bootstraps)
	}
	if l.prints > settlePolls+1 {
		t.Fatalf("prints = %d, want the wait bounded by settlePolls (%d)", l.prints, settlePolls)
	}
}

func TestLoadJobStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &dyingLaunchd{dying: 1 << 30}
	if err := loadJob(ctx, l.run, "gui/501", "sh.x", "/p/sh.x.plist"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
