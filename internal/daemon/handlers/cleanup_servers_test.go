package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeProcs wires every process seam of Services to a fake table. Pids are
// >= 9000001: a missed seam signals nothing (ESRCH).
type fakeProcs struct {
	listeners map[int][]int
	cwds      map[int]string
	parents   map[int]int
	immortal  map[int]bool
	cwdErr    error
	portErr   error
	portCalls atomic.Int32
	log       *orderLog
}

func newFakeProcs(log *orderLog, ws *pigtail.Workspace) *fakeProcs {
	return &fakeProcs{
		listeners: map[int][]int{3044: {9000001}},
		cwds:      map[int]string{9000001: ws.Path},
		parents:   map[int]int{},
		immortal:  map[int]bool{},
		log:       log,
	}
}

func (f *fakeProcs) wire(svc *Services) {
	svc.PortBusy = func(port int) (bool, []int, error) {
		f.portCalls.Add(1)
		if f.portErr != nil {
			return false, nil, f.portErr
		}
		p := f.listeners[port]
		return len(p) > 0, append([]int(nil), p...), nil
	}
	svc.ProcessCwds = func(pids []int) (map[int]string, error) {
		if f.cwdErr != nil {
			return nil, f.cwdErr
		}
		m := map[int]string{}
		for _, p := range pids {
			if c, ok := f.cwds[p]; ok {
				m[p] = c
			}
		}
		return m, nil
	}
	svc.ProcessParents = func() (map[int]int, error) { return f.parents, nil }
	svc.ProcessAlive = func(pid int) bool {
		for _, ls := range f.listeners {
			for _, l := range ls {
				if l == pid {
					return true
				}
			}
		}
		return false
	}
	svc.ServerStopGrace = time.Millisecond
	svc.KillPIDs = func(pids []int) {
		f.log.add(fmt.Sprintf("kill %v", pids))
		for _, p := range pids {
			if f.immortal[p] {
				continue
			}
			for port, ls := range f.listeners {
				var keep []int
				for _, l := range ls {
					if l != p {
						keep = append(keep, l)
					}
				}
				f.listeners[port] = keep
			}
		}
	}
}

func errorBody(t *testing.T, body []byte) (code, msg string) {
	t.Helper()
	var m struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return m.Code, m.Message
}

func TestCleanupStopsDevServersBeforeTeardown(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	f := newFakeProcs(log, ws)
	f.parents = map[int]int{9000002: 9000001}
	f.wire(svc)

	rec := deleteWorkspace(t, svc, ws.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if want := []string{"kill [9000001 9000002]", "backend.Cleanup"}; !reflect.DeepEqual(log.get(), want) {
		t.Fatalf("order = %v, want %v", log.get(), want)
	}
	var out CleanupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := []pigtail.StoppedServer{{Kinds: []string{"dev"}, Port: 3044, PIDs: []int{9000001, 9000002}}}
	if !reflect.DeepEqual(out.ServersStopped, want) {
		t.Fatalf("servers_stopped = %+v, want %+v", out.ServersStopped, want)
	}
}

func TestCleanupRefusesWhenADevServerSurvives(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	f := newFakeProcs(log, ws)
	f.immortal[9000001] = true
	f.wire(svc)
	sub := svc.Bus.Subscribe()
	defer sub.Close()
	stateDir, err := devserver.EnsureStateDir(ws.Path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stateDir, "dev.owner")
	if err := os.WriteFile(marker, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := deleteWorkspace(t, svc, ws.ID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
	}
	code, msg := errorBody(t, rec.Body.Bytes())
	if code != "DEV_SERVER_RUNNING" {
		t.Fatalf("code %q", code)
	}
	for _, s := range []string{":3044", "9000001", "nothing was removed"} {
		if !strings.Contains(msg, s) {
			t.Fatalf("message %q lacks %q", msg, s)
		}
	}
	for _, e := range log.get() {
		if e == "backend.Cleanup" {
			t.Fatal("backend.Cleanup ran")
		}
	}
	if _, err := svc.Store.GetWorkspace(ws.ID); err != nil {
		t.Fatalf("row gone: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("state dir content gone: %v", err)
	}
	if !waitForEvent(t, sub, pigtail.EventWorkspaceCleanupFailed) {
		t.Fatal("no cleanup_failed event")
	}
}

// --force releases nothing: not an unknown owner, not a failed probe.
func TestCleanupUnknownOwnerRefusesEvenWithForce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeProcs)
	}{
		{"cwd probe fails", func(f *fakeProcs) { f.cwdErr = errors.New("lsof boom") }},
		{"port probe fails", func(f *fakeProcs) { f.portErr = errors.New("lsof boom") }},
	} {
		for _, body := range []string{"", `{"force":true}`} {
			t.Run(tc.name+" "+body, func(t *testing.T) {
				log := &orderLog{}
				svc, ws := incidentWorkspace(t, &orderBackend{log: log})
				f := newFakeProcs(log, ws)
				tc.setup(f)
				f.wire(svc)
				rec := deleteWorkspace(t, svc, ws.ID, body)
				if rec.Code != http.StatusConflict {
					t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
				}
				if len(log.get()) != 0 {
					t.Fatalf("something ran: %v", log.get())
				}
				if _, err := svc.Store.GetWorkspace(ws.ID); err != nil {
					t.Fatalf("row gone: %v", err)
				}
			})
		}
	}
}

func TestCleanupRefusesAListenerWithoutCwdWhenTheWorktreeIsGone(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	if err := os.RemoveAll(ws.Path); err != nil {
		t.Fatal(err)
	}
	f := newFakeProcs(log, ws)
	f.cwds = map[int]string{9000001: ""}
	f.wire(svc)
	rec := deleteWorkspace(t, svc, ws.ID, `{"force":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(log.get()) != 0 {
		t.Fatalf("something ran: %v", log.get())
	}
}

func TestCleanupWithoutPortsProbesNothing(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	if err := svc.Store.UpdateWorkspacePorts(ws.ID, map[string]int{}); err != nil {
		t.Fatal(err)
	}
	f := newFakeProcs(log, ws)
	f.wire(svc)
	rec := deleteWorkspace(t, svc, ws.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.portCalls.Load(); n != 0 {
		t.Fatalf("PortBusy called %d times", n)
	}
}

func TestCleanupWorktreeAlreadyGone(t *testing.T) {
	log := &orderLog{}
	svc, ws := incidentWorkspace(t, &orderBackend{log: log})
	if err := os.RemoveAll(ws.Path); err != nil {
		t.Fatal(err)
	}
	f := newFakeProcs(log, ws)
	f.cwds[9000001] = ws.Path + "/node_modules/.vite"
	f.wire(svc)
	rec := deleteWorkspace(t, svc, ws.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if want := []string{"kill [9000001]", "backend.Cleanup"}; !reflect.DeepEqual(log.get(), want) {
		t.Fatalf("order = %v, want %v", log.get(), want)
	}
}
