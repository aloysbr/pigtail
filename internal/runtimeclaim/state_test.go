package runtimeclaim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

var t0 = time.Unix(1_800_000_000, 0)

func aliveAll(string, int, string) bool  { return true }
func aliveNone(string, int, string) bool { return false }

func req(session, branch string) State {
	return State{Reason: "acceptance #218", PR: 218, Session: session, Label: "label " + session, PID: 4242, ConfigDir: "/cfg",
		Branch: branch, SHA: strings.Repeat("a", 40)}
}

func tokenText(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, TokenName))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestClaimNewThenSameSessionUpdates(t *testing.T) {
	dir := t.TempDir()
	st, err := Claim(dir, req("sess-A", "feat/a"), false, aliveAll, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.ID, "rtc_") || len(st.ID) != len("rtc_")+16 || !st.Since.Equal(t0) {
		t.Fatalf("new claim = %+v", st)
	}
	if tok := tokenText(t, dir); tok != fmt.Sprintf("%s on %d\n", st.ID, t0.Unix()) {
		t.Fatalf("token = %q", tok)
	}
	r := req("sess-A", "feat/b")
	r.SHA, r.PR = strings.Repeat("b", 40), 219
	up, err := Claim(dir, r, false, aliveAll, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if up.ID != st.ID || !up.Since.Equal(t0) || up.Branch != "feat/b" || up.SHA != r.SHA || up.PR != 219 {
		t.Fatalf("update = %+v, want id/since kept and branch/sha/pr replaced", up)
	}
	got, ok, err := Load(dir)
	if err != nil || !ok || got.ID != st.ID || got.Branch != "feat/b" {
		t.Fatalf("Load = %+v %v %v", got, ok, err)
	}
}

func TestClaimHeldByOtherSession(t *testing.T) {
	dir := t.TempDir()
	if _, err := Claim(dir, req("sess-A", "feat/a"), false, aliveAll, t0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	_, err := Claim(dir, req("sess-B", "feat/b"), false, aliveAll, t0)
	var held *HeldError
	if !errors.As(err, &held) || held.Liveness != Alive || held.Cur.Session != "sess-A" {
		t.Fatalf("err = %v, want *HeldError alive held by sess-A", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("state file changed on a refused claim")
	}
}

func TestClaimTakeOverOnlyWhenStale(t *testing.T) {
	dir := t.TempDir()
	first, err := Claim(dir, req("sess-A", "feat/a"), false, aliveAll, t0)
	if err != nil {
		t.Fatal(err)
	}
	var held *HeldError
	if _, err := Claim(dir, req("sess-B", "feat/b"), true, aliveAll, t0); !errors.As(err, &held) {
		t.Fatalf("take-over of a live holder: err = %v, want *HeldError", err)
	}
	st, err := Claim(dir, req("sess-B", "feat/b"), true, aliveNone, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if st.Session != "sess-B" || !strings.HasPrefix(st.Reason, "took over from sess-A") || st.ID == first.ID {
		t.Fatalf("take-over = %+v", st)
	}
}

func TestReleaseRules(t *testing.T) {
	cases := []struct {
		name    string
		session string
		force   bool
		alive   AliveFunc
		pid     int
		wantOK  bool
		wantErr bool
	}{
		{"holder", "sess-A", false, aliveAll, 4242, true, false},
		{"other alive", "sess-B", false, aliveAll, 4242, false, true},
		{"other force stale", "sess-B", true, aliveNone, 4242, true, false},
		{"other force holder alive", "sess-B", true, aliveAll, 4242, false, true},
		{"other force unknown", "sess-B", true, aliveNone, 0, false, true},
		{"human force", "", true, aliveAll, 4242, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			r := req("sess-A", "feat/a")
			r.PID = c.pid
			st, err := Claim(dir, r, false, aliveAll, t0)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, FileName))
			rel, ok, err := Release(dir, c.session, c.force, c.alive, t0.Add(time.Hour))
			if c.wantErr {
				var nh *NotHolderError
				if !errors.As(err, &nh) || nh.Cur.Session != "sess-A" {
					t.Fatalf("err = %v, want *NotHolderError", err)
				}
				after, _ := os.ReadFile(filepath.Join(dir, FileName))
				if !bytes.Equal(before, after) {
					t.Fatal("state file changed on a refused release")
				}
				return
			}
			if err != nil || ok != c.wantOK || rel.ID != st.ID {
				t.Fatalf("Release = %+v %v %v", rel, ok, err)
			}
			if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("JSON still there after release: %v", err)
			}
			if tok := tokenText(t, dir); tok != fmt.Sprintf("%s off %d\n", st.ID, t0.Add(time.Hour).Unix()) {
				t.Fatalf("token = %q", tok)
			}
		})
	}
	t.Run("nothing", func(t *testing.T) {
		if _, ok, err := Release(t.TempDir(), "sess-A", false, aliveAll, t0); ok || err != nil {
			t.Fatalf("no claim: ok=%v err=%v, want false nil", ok, err)
		}
	})
}

func TestReleaseUnreadableByHumanForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Release(dir, "sess-A", true, aliveNone, t0); err == nil {
		t.Fatal("a session must not repair an unreadable claim")
	}
	if _, ok, err := Release(dir, "", true, aliveNone, t0); err != nil || !ok {
		t.Fatalf("Ada --force on an unreadable claim: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable JSON not removed: %v", err)
	}
}

func TestLivenessStaleWhenPidFileNamesOtherSession(t *testing.T) {
	cfg := t.TempDir()
	pid := os.Getpid()
	if err := os.MkdirAll(filepath.Join(cfg, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": "sess-OTHER"})
	if err := os.WriteFile(filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st := State{Session: "sess-A", PID: pid, ConfigDir: cfg}
	if got := st.Liveness(claudesession.SessionAlive); got != Stale {
		t.Fatalf("pid file naming another session: %q, want stale", got)
	}
	st.Session = "sess-OTHER"
	if got := st.Liveness(claudesession.SessionAlive); got != Alive {
		t.Fatalf("pid file naming the holder: %q, want alive", got)
	}
	if got := (State{Session: "sess-A", ConfigDir: cfg}).Liveness(claudesession.SessionAlive); got != Unknown {
		t.Fatalf("PID 0: %q, want unknown", got)
	}
	if got := (State{}).Liveness(claudesession.SessionAlive); got != Alive {
		t.Fatalf("Ada: %q, want alive", got)
	}
}

func TestClaimRefusesUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(dir, req("sess-A", "feat/a"), true, aliveNone, t0); err == nil {
		t.Fatal("want an error on an unreadable file")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, FileName)); string(raw) != "{nope" {
		t.Fatalf("unreadable file overwritten: %q", raw)
	}
	if _, _, err := Load(dir); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("Load error = %v, want an error without the path", err)
	}
}

func TestClaimConcurrentSingleWinner(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Claim(dir, req(fmt.Sprintf("sess-%d", i), "feat/a"), false, aliveAll, t0)
		}(i)
	}
	wg.Wait()
	wins, held := 0, 0
	for _, err := range errs {
		var h *HeldError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &h):
			held++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || held != 7 {
		t.Fatalf("wins=%d held=%d, want 1 and 7", wins, held)
	}
}

func TestClaimLockTimeout(t *testing.T) {
	dir := t.TempDir()
	old := lockWait
	lockWait = 50 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	f, err := os.OpenFile(filepath.Join(dir, LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(dir, req("sess-A", "feat/a"), false, aliveAll, t0); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("err = %v, want ErrLockTimeout", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state written despite the lock timeout")
	}
}

func TestDeadlineLegacyFileSincePlusTTL(t *testing.T) {
	dir := t.TempDir()
	raw := fmt.Sprintf(`{"id":"rtc_0123456789abcdef","session":"sess-A","since":%q}`, t0.UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, ok, err := Load(dir)
	if err != nil || !ok {
		t.Fatalf("Load = %+v %v %v", st, ok, err)
	}
	if !st.Deadline().Equal(t0.Add(4 * time.Hour)) {
		t.Fatalf("Deadline = %v", st.Deadline())
	}
	if st.Expired(t0.Add(4*time.Hour - time.Second)) {
		t.Fatal("expired one second before the deadline")
	}
	if !st.Expired(t0.Add(4 * time.Hour)) {
		t.Fatal("not expired at the deadline")
	}
}

func TestClaimSetsAndRenewsDeadline(t *testing.T) {
	dir := t.TempDir()
	st, err := Claim(dir, req("sess-A", "feat/a"), false, aliveAll, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ExpiresAt.Equal(t0.Add(4 * time.Hour)) {
		t.Fatalf("ExpiresAt = %v", st.ExpiresAt)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(raw), `"expires_at"`) {
		t.Fatalf("file lacks expires_at: %s", raw)
	}
	up, err := Claim(dir, req("sess-A", "feat/a"), false, aliveAll, t0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if up.ID != st.ID || !up.Since.Equal(t0) || !up.ExpiresAt.Equal(t0.Add(7*time.Hour)) {
		t.Fatalf("renewal = %+v", up)
	}
	dir2 := t.TempDir()
	if _, err := Claim(dir2, req("sess-A", "feat/a"), false, aliveAll, t0); err != nil {
		t.Fatal(err)
	}
	tk, err := Claim(dir2, req("sess-B", "feat/b"), true, aliveNone, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !tk.ExpiresAt.Equal(t0.Add(5 * time.Hour)) {
		t.Fatalf("take-over ExpiresAt = %v", tk.ExpiresAt)
	}
}
