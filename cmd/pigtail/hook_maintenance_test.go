package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
)

func TestMaintenanceHookOnceAndSoleWriter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir, "PIGTAIL_SOCKET": filepath.Join(dir, "absent.sock")}
	getenv := func(k string) string { return env[k] }
	u := time.Now().Add(time.Hour)
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Until: &u, Since: time.Now()})
	payload := `{"session_id":"s1","hook_event_name":"UserPromptSubmit"}`
	var out, errw bytes.Buffer
	if rc := runMaintenanceHook(strings.NewReader(payload), &out, &errw, getenv, time.Now); rc != 0 ||
		!strings.Contains(out.String(), `"additionalContext"`) || !strings.Contains(out.String(), "pigtail system message") ||
		!strings.Contains(out.String(), `"hookEventName":"UserPromptSubmit"`) {
		t.Fatalf("first: rc=%d out=%s err=%s", rc, out.String(), errw.String())
	}
	out.Reset()
	runMaintenanceHook(strings.NewReader(payload), &out, &errw, getenv, time.Now)
	if out.Len() != 0 {
		t.Fatalf("second call must be silent: %s", out.String())
	}
	if w := maintenance.ReadWitness(dir, "s1"); w.Shown != "mnt_a:on" || w.Key != "mnt_a:on+j" {
		t.Fatalf("witness %+v", w)
	}
}

// A subagent's payload carries the parent's session_id: it must neither get
// the notice nor touch the witness; the main session gets it next.
func TestMaintenanceHookSkipsSubagent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	getenv := func(k string) string { return env[k] }
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Since: time.Now()})
	var out, errw bytes.Buffer
	sub := `{"session_id":"s1","hook_event_name":"PostToolUse","agent_id":"a-1","agent_type":"general-purpose"}`
	runMaintenanceHook(strings.NewReader(sub), &out, &errw, getenv, time.Now)
	if out.Len() != 0 {
		t.Fatalf("subagent got the notice: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, maintenance.SeenDirName, "s1")); !os.IsNotExist(err) {
		t.Fatalf("subagent touched the witness: %v", err)
	}
	main := `{"session_id":"s1","hook_event_name":"PostToolUse"}`
	runMaintenanceHook(strings.NewReader(main), &out, &errw, getenv, time.Now)
	if !strings.Contains(out.String(), "pigtail system message") {
		t.Fatalf("main session must still get it: %s", out.String())
	}
}

// N concurrent hook calls for one session: exactly one notice (flock).
func TestMaintenanceHookConcurrentOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	getenv := func(k string) string { return env[k] }
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Since: time.Now()})
	payload := `{"session_id":"s1","hook_event_name":"PostToolUse"}`
	const n = 32
	outs := make([]bytes.Buffer, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			var errw bytes.Buffer
			runMaintenanceHook(strings.NewReader(payload), &outs[i], &errw, getenv, time.Now)
		}(i)
	}
	close(start)
	wg.Wait()
	shown := 0
	for i := range outs {
		if strings.Contains(outs[i].String(), "pigtail system message") {
			shown++
		}
	}
	if shown != 1 {
		t.Fatalf("%d notices for %d concurrent calls, want exactly 1", shown, n)
	}
}

// The per-session lock cannot be taken (maintenance-seen is not a directory):
// the hook must fail loudly — exit != 0 with the cause as the LAST stderr
// line — so the hook script  raises its failure alert instead
// of the notice being lost in silence.
func TestMaintenanceHookLockFailureExitsNonZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	getenv := func(k string) string { return env[k] }
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Since: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, maintenance.SeenDirName), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	rc := runMaintenanceHook(strings.NewReader(`{"session_id":"s1","hook_event_name":"PostToolUse"}`), &out, &errw, getenv, time.Now)
	lines := strings.Split(strings.TrimRight(errw.String(), "\n"), "\n")
	if rc == 0 || out.Len() != 0 || !strings.Contains(lines[len(lines)-1], "pigtail maintenance: cannot lock") {
		t.Fatalf("rc=%d out=%q stderr=%q", rc, out.String(), errw.String())
	}
}

// The envelope names the state file for on/late/corrupt; at off the file is
// gone, so it says who lifted it: here Ada outside Claude Code (D15, D17).
func TestMaintenanceHookEnvelopeOffDoesNotCiteTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	getenv := func(k string) string { return env[k] }
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Since: time.Now()})
	payload := `{"session_id":"s1","hook_event_name":"PostToolUse"}`
	var out, errw bytes.Buffer
	runMaintenanceHook(strings.NewReader(payload), &out, &errw, getenv, time.Now)
	if !strings.Contains(out.String(), "read by pigtail from "+filepath.Join(dir, maintenance.FileName)) {
		t.Fatalf("on envelope must cite the file: %s", out.String())
	}
	_ = maintenance.Lift(dir, "mnt_a", time.Now(), "", "", "")
	out.Reset()
	runMaintenanceHook(strings.NewReader(payload), &out, &errw, getenv, time.Now)
	o := out.String()
	if !strings.Contains(o, `phase=\"off\"`) || strings.Contains(o, maintenance.FileName) || strings.Contains(o, "read by pigtail") ||
		!strings.Contains(o, "lifted by the user outside Claude Code (`pigtail maintenance off`) — not from a peer session.") {
		t.Fatalf("off envelope: %s", o)
	}
}

// The lock failure line names a path once (the .lock), not the directory
// again inside the mkdir error.
func TestMaintenanceHookLockFailureNamesOnePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Since: time.Now()})
	_ = os.WriteFile(filepath.Join(dir, maintenance.SeenDirName), []byte("x"), 0o600)
	var out, errw bytes.Buffer
	runMaintenanceHook(strings.NewReader(`{"session_id":"s1"}`), &out, &errw, func(k string) string { return env[k] }, time.Now)
	if n := strings.Count(errw.String(), dir); n != 1 {
		t.Fatalf("path named %d times: %q", n, errw.String())
	}
}

// The witness write failure line names a path once.
func TestMaintenanceHookWitnessFailureNamesOnePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Since: time.Now()})
	if err := os.MkdirAll(filepath.Join(dir, maintenance.SeenDirName, "s1"), 0o700); err != nil { // the witness is a directory
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	runMaintenanceHook(strings.NewReader(`{"session_id":"s1"}`), &out, &errw, func(k string) string { return env[k] }, time.Now)
	if !strings.Contains(errw.String(), "cannot write witness") || strings.Count(errw.String(), dir) != 1 {
		t.Fatalf("stderr %q", errw.String())
	}
}

func TestMaintenanceHookOriginNamesOwner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	getenv := func(k string) string { return map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}[k] }
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Owner: "sid-owner", OwnerLabel: "Alpha", OwnerScope: "globex", Since: time.Now()}) // read outside any cell: the default scope (D22)
	var out, errw bytes.Buffer
	runMaintenanceHook(strings.NewReader(`{"session_id":"s1","hook_event_name":"PostToolUse"}`), &out, &errw, getenv, time.Now)
	if !strings.Contains(out.String(), "set by session Alpha (`pigtail maintenance on`) and read by pigtail from") || strings.Contains(out.String(), "set by the user") {
		t.Fatalf("%s", out.String())
	}
	_ = maintenance.Lift(dir, "mnt_a", time.Now(), "sid-owner", "Alpha", "globex")
	out.Reset()
	runMaintenanceHook(strings.NewReader(`{"session_id":"s1","hook_event_name":"PostToolUse"}`), &out, &errw, getenv, time.Now)
	if !strings.Contains(out.String(), "lifted by session Alpha (`pigtail maintenance off`) — not from a peer session.") ||
		!strings.Contains(out.String(), "par la session Alpha") {
		t.Fatalf("%s", out.String())
	}
}

func TestMaintenanceHookOriginCorruptSaysUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	getenv := func(k string) string { return map[string]string{"PIGTAIL_MAINTENANCE_DIR": dir}[k] }
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Reason: "bascule", Owner: "sid-owner", OwnerLabel: "Alpha", Since: time.Now()})
	payload := `{"session_id":"s1","hook_event_name":"PostToolUse"}`
	var out, errw bytes.Buffer
	runMaintenanceHook(strings.NewReader(payload), &out, &errw, getenv, time.Now) // s1 saw on
	_ = os.WriteFile(filepath.Join(dir, maintenance.FileName), []byte("{"), 0o600)
	out.Reset()
	runMaintenanceHook(strings.NewReader(payload), &out, &errw, getenv, time.Now)
	o := out.String()
	if !strings.Contains(o, `phase=\"corrupt\"`) || !strings.Contains(o, "(unreadable: who set it is unknown)") || strings.Contains(o, "set by") {
		t.Fatalf("%s", o)
	}
}
