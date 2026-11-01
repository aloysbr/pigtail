package runtimeclaim

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func claimAt(t *testing.T, dir, session string, at time.Time) State {
	t.Helper()
	st, err := Claim(dir, req(session, "feat/a"), false, aliveAll, at)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestExpireNeverLiftsBeforeDeadline(t *testing.T) {
	dir := t.TempDir()
	st := claimAt(t, dir, "sess-A", t0)
	exp, ok, err := Expire(dir, t0.Add(TTL-time.Second))
	if err != nil || ok || exp.Lease.ID != "" {
		t.Fatalf("Expire = %+v %v %v", exp, ok, err)
	}
	if _, ok, _ := Load(dir); !ok {
		t.Fatal("lease gone before its deadline")
	}
	if tok := tokenText(t, dir); tok != fmt.Sprintf("%s on %d\n", st.ID, t0.Unix()) {
		t.Fatalf("token = %q", tok)
	}
	if _, ok := ReadExpiry(dir); ok {
		t.Fatal("marker written before the deadline")
	}
}

func TestExpireLiftsAtDeadlineLikeRelease(t *testing.T) {
	dir := t.TempDir()
	st := claimAt(t, dir, "sess-A", t0)
	at := t0.Add(TTL)
	exp, ok, err := Expire(dir, at)
	if err != nil || !ok || exp.Lease.ID != st.ID || !exp.At.Equal(at) {
		t.Fatalf("Expire = %+v %v %v", exp, ok, err)
	}
	if _, ok, _ := Load(dir); ok {
		t.Fatal("JSON still there")
	}
	if tok := tokenText(t, dir); tok != fmt.Sprintf("%s off %d\n", st.ID, at.Unix()) {
		t.Fatalf("token = %q", tok)
	}
	if got, ok := ReadExpiry(dir); !ok || got.Lease.ID != st.ID {
		t.Fatalf("ReadExpiry = %+v %v", got, ok)
	}
	if _, ok, err := Expire(dir, at.Add(time.Hour)); ok || err != nil {
		t.Fatalf("second Expire = %v %v", ok, err)
	}
}

func TestExpireAppliesToHuman(t *testing.T) {
	dir := t.TempDir()
	claimAt(t, dir, "", t0)
	if _, ok, err := Expire(dir, t0.Add(TTL)); !ok || err != nil {
		t.Fatalf("Expire = %v %v", ok, err)
	}
}

func TestExpireLeavesUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Expire(dir, t0.Add(10*time.Hour)); ok || err == nil {
		t.Fatalf("Expire = %v %v, want an error", ok, err)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, []byte("{nope")) {
		t.Fatalf("file changed: %q", raw)
	}
	if _, ok := ReadExpiry(dir); ok {
		t.Fatal("marker written")
	}
}

func TestExpireNoLeaseCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Expire(dir, t0); ok || err != nil {
		t.Fatalf("Expire = %v %v", ok, err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("dir not empty: %v", ents)
	}
}

func TestExpireNoWaitBusyLockLeavesLease(t *testing.T) {
	dir := t.TempDir()
	claimAt(t, dir, "sess-A", t0)
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	_, ok, err := ExpireNoWait(dir, t0.Add(TTL))
	if ok || !errors.Is(err, ErrLockTimeout) || time.Since(start) > time.Second {
		t.Fatalf("ExpireNoWait = %v %v after %v", ok, err, time.Since(start))
	}
}

func TestClaimByOtherSessionOnExpiredLease(t *testing.T) {
	dir := t.TempDir()
	a := claimAt(t, dir, "sess-A", t0)
	at := t0.Add(TTL)
	b, err := Claim(dir, req("sess-B", "feat/b"), false, aliveAll, at)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == a.ID || !b.Since.Equal(at) {
		t.Fatalf("claim = %+v", b)
	}
	if exp, ok := ReadExpiry(dir); !ok || exp.Lease.ID != a.ID {
		t.Fatalf("ReadExpiry = %+v %v", exp, ok)
	}
}

func TestClaimBySameSessionAfterExpiryIsNew(t *testing.T) {
	dir := t.TempDir()
	a := claimAt(t, dir, "sess-A", t0)
	at := t0.Add(5 * time.Hour)
	b := claimAt(t, dir, "sess-A", at)
	if b.ID == a.ID || !b.Since.Equal(at) {
		t.Fatalf("claim = %+v", b)
	}
	if exp, ok := ReadExpiry(dir); !ok || exp.Lease.ID != a.ID {
		t.Fatalf("ReadExpiry = %+v %v", exp, ok)
	}
}

func TestReleaseOfExpiredLease(t *testing.T) {
	dir := t.TempDir()
	a := claimAt(t, dir, "sess-A", t0)
	_, ok, err := Release(dir, "sess-B", false, aliveAll, t0.Add(TTL))
	if ok || err != nil {
		t.Fatalf("Release = %v %v", ok, err)
	}
	if exp, ok := ReadExpiry(dir); !ok || exp.Lease.ID != a.ID {
		t.Fatalf("ReadExpiry = %+v %v", exp, ok)
	}
	if _, ok, _ := Load(dir); ok {
		t.Fatal("JSON still there")
	}
}
