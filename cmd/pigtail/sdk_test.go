package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// sdkFakeReg serves SDK sessions to the real routes. liveFrom is the 1-based
// List call from which sub_p's session reads as live (0: never).
type sdkFakeReg struct {
	mu       sync.Mutex
	calls    int
	liveFrom int
	listErr  error
}

func (f *sdkFakeReg) sessions() []sdksession.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	live := f.liveFrom > 0 && f.calls >= f.liveFrom
	state := "ended"
	if live {
		state = "running"
	}
	return []sdksession.Session{{
		ID: "sdk_a", SubjectID: "sub_p", Cwd: "/w", ConfigDir: "/c", ClaudeSessionID: "csid-a", State: state, Live: live,
		ResumeCommand: map[bool]string{false: "cd /w && CLAUDE_CONFIG_DIR=/c claude --resume csid-a"}[live],
		ClaudePIDs:    []int{}, Pending: []sdksession.Request{}, StartedAt: time.Unix(1000, 0).UTC(),
	}}
}

func (f *sdkFakeReg) List(string) ([]sdksession.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.sessions(), nil
}
func (f *sdkFakeReg) Settle(id string) (sdksession.Session, error) {
	if id != "sdk_a" {
		return sdksession.Session{}, sdksession.ErrNotFound
	}
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.sessions()[0], nil
}
func (f *sdkFakeReg) Events(string, int) ([]json.RawMessage, error) { return nil, nil }
func (f *sdkFakeReg) Dial(string, any) (sdksession.HostReply, error) {
	return sdksession.HostReply{OK: true}, nil
}

// sdkDaemon is the real subject routes with the SDK service on f (nil f: the option off).
func sdkDaemon(t *testing.T, f *sdkFakeReg) (*stubDaemon, *store.Store) {
	t.Helper()
	return realSubjectDaemonSvc(t, func(svc *handlers.Services) {
		if f != nil {
			svc.SDK = &handlers.SDKService{Registry: f}
		}
	}, func(chi.Router, *store.Store) {})
}

func runSDK(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	var o bytes.Buffer
	var err error
	withStubDaemon(t, s, func() {
		cmd := sdkCmd()
		cmd.SetOut(&o)
		cmd.SetErr(&o)
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	return o.String(), err
}

func TestSDKLsAndShow(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := sdkDaemon(t, &sdkFakeReg{})
	out, err := runSDK(t, s, "ls", "--subject", "sub_p")
	if err != nil || !strings.Contains(out, "sdk_a") || !strings.Contains(out, "ended") {
		t.Fatalf("ls = %q, %v", out, err)
	}
	out, err = runSDK(t, s, "ls", "--json")
	var l struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err != nil || json.Unmarshal([]byte(out), &l) != nil || len(l.Sessions) != 1 {
		t.Fatalf("ls --json = %q, %v", out, err)
	}
	out, err = runSDK(t, s, "show", "sdk_a", "--json")
	var got map[string]any
	if err != nil || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("show --json = %q, %v", out, err)
	}
	for _, k := range []string{"subject_id", "claude_session_id", "state", "resume_command"} { // CA8
		if v, ok := got[k]; !ok || v == "" {
			t.Errorf("show --json lacks %s: %v", k, got)
		}
	}
	if _, err := runSDK(t, s, "show", "sdk_nope"); err == nil {
		t.Error("show of an unknown id must fail")
	}
}

func TestSDKCommandsAreScopedReads(t *testing.T) {
	for _, c := range sdkCmd().Commands() {
		if c.Annotations == nil || c.Annotations[scopeAccessKey] != scopedRead()[scopeAccessKey] {
			t.Errorf("pigtail sdk %s must carry scopedRead()", c.Name())
		}
	}
}

// seedLocalSDKSession points the sessions dir at a temporary state home and
// writes one session there: a fresh meta.json with no host.json reads as a
// launch under way (live); corrupt writes a meta.json that cannot be parsed.
func seedLocalSDKSession(t *testing.T, id, subject string, corrupt bool) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(sdksession.DefaultDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("{broken")
	if !corrupt {
		m := sdksession.Meta{Version: 1, ID: id, SubjectID: subject, Cwd: "/w", ConfigDir: "/c", CreatedAt: time.Now()}
		body, _ = json.Marshal(m)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// CA3: a subject with a live SDK session is not spawned onto, dry-run or not,
// and the check is made again under the registry lock.
func TestSpawnRefusedWhileSDKLive(t *testing.T) {
	cwd := t.TempDir()
	withCells(t, func(_, perso string) string { return perso })

	t.Run("dry-run", func(t *testing.T) {
		s, _ := sdkDaemon(t, &sdkFakeReg{liveFrom: 1})
		installSpawnFake(t)
		_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "already has a live session") || !strings.Contains(err.Error(), "sdk_a") {
			t.Fatalf("= %v", err)
		}
	})
	t.Run("under the lock", func(t *testing.T) {
		// Not live at the plan, live at the reservation's Check.
		s, _ := sdkDaemon(t, &sdkFakeReg{liveFrom: 2})
		f := installSpawnFake(t)
		f.launchesAs(persoCellDir(t), cwd)
		_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd)
		if err == nil || !strings.Contains(err.Error(), "already has a live session") {
			t.Fatalf("= %v", err)
		}
		if len(f.created) != 0 {
			t.Fatal("a workspace was created")
		}
	})
	t.Run("SDK_OFF with an empty sessions dir lets it through", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		s, _ := sdkDaemon(t, nil)
		installSpawnFake(t)
		if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run"); err != nil {
			t.Fatalf("SDK off with no host on disk must not block: %v", err)
		}
	})
	t.Run("SDK_OFF with a live host on disk refuses", func(t *testing.T) {
		s, _ := sdkDaemon(t, nil)
		installSpawnFake(t)
		seedLocalSDKSession(t, "sdk_live", "sub_p", false) // after the fake: it sets its own state home
		_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "already has a live session") || !strings.Contains(err.Error(), "sdk_live") {
			t.Fatalf("a host still alive on disk must be seen with the option off: %v", err)
		}
	})
	t.Run("SDK_OFF with an unreadable session refuses", func(t *testing.T) {
		s, _ := sdkDaemon(t, nil)
		installSpawnFake(t)
		seedLocalSDKSession(t, "sdk_bad", "sub_p", true)
		_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "already has a live session") {
			t.Fatalf("an unreadable registry must refuse (fail-safe): %v", err)
		}
	})
	t.Run("any other error refuses", func(t *testing.T) {
		s, _ := sdkDaemon(t, &sdkFakeReg{listErr: errors.New("disk gone")})
		installSpawnFake(t)
		_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "SDK") {
			t.Fatalf("an unreadable SDK registry must refuse: %v", err)
		}
	})
	t.Run("an ended session does not block", func(t *testing.T) {
		s, _ := sdkDaemon(t, &sdkFakeReg{})
		installSpawnFake(t)
		if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--dry-run"); err != nil {
			t.Fatalf("= %v", err)
		}
	})
}

// CA3: a respawn replaces a cmux tab's session; it must not while a SDK
// session of the subject writes. Nothing is closed nor registered.
func TestRespawnRefusedWhileSDKLive(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	for name, tc := range map[string]struct {
		reg     *sdkFakeReg
		wantErr string
	}{
		"live":       {&sdkFakeReg{liveFrom: 1}, "already has a live session"},
		"unreadable": {&sdkFakeReg{listErr: errors.New("disk gone")}, "SDK"},
		"ended":      {&sdkFakeReg{}, ""},
		"option off": {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			s, _ := sdkDaemon(t, tc.reg)
			installRespawnFake(t)
			_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
			if tc.wantErr == "" && err != nil {
				t.Fatalf("= %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("= %v, want %q", err, tc.wantErr)
			}
			if es, _ := spawnreg.Open(spawnreg.DefaultPath()).Entries(); len(es) != 0 {
				t.Fatalf("the registry was written: %v", es)
			}
		})
	}
	t.Run("option off, live host on disk", func(t *testing.T) {
		s, _ := sdkDaemon(t, nil)
		installRespawnFake(t)
		seedLocalSDKSession(t, "sdk_live", "sub_p", false) // after the fake: it sets its own state home
		_, _, err := runRespawnCLI(t, s, "--subject", "sub_p", "--prompt", respawnPrompt, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "already has a live session") {
			t.Fatalf("= %v", err)
		}
	})
}
