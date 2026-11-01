package runtimeclaim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenKeyMatchesBashGrammar(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := TokenKey(dir); got != "+-" {
		t.Fatalf("nothing: %q, want +-", got)
	}
	write(TokenName, "rtc_x on 1\n")
	write(FileName, "{}")
	if got := TokenKey(dir); got != "rtc_x:on+j" {
		t.Fatalf("on + JSON: %q", got)
	}
	_ = os.Remove(filepath.Join(dir, FileName))
	write(TokenName, "rtc_x off 2\n")
	if got := TokenKey(dir); got != "rtc_x:off+-" {
		t.Fatalf("off without JSON: %q", got)
	}
	for _, bad := range []string{"rtc_x on\n", "mnt_x on 1\n", "rtc_x on 1 extra\n", "rtc_x maybe 1\n", "rtc_x on 1a\n", "rtc_x on -1\n", "rtc_x on +1\n"} {
		write(TokenName, bad)
		if got := TokenKey(dir); !strings.HasPrefix(got, "bad+") {
			t.Fatalf("malformed %q: %q, want bad+…", bad, got)
		}
	}
}

func TestWitnessRoundTripAndLock(t *testing.T) {
	dir := t.TempDir()
	if got := ReadWitness(dir, "s1"); got != (Witness{}) {
		t.Fatalf("no witness: %+v", got)
	}
	if err := WriteWitness(dir, "s1", Witness{Key: "rtc_x:on+j", Shown: "rtc_x:on"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadWitness(dir, "s1"); got.Key != "rtc_x:on+j" || got.Shown != "rtc_x:on" {
		t.Fatalf("witness = %+v", got)
	}
	unlock, ok, err := LockSession(dir, "s1")
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if _, ok2, err := LockSession(dir, "s1"); err != nil || ok2 {
		t.Fatalf("second lock: ok=%v err=%v, want busy", ok2, err)
	}
	unlock()
	if err := WriteWitness(dir, "../x", Witness{}); err == nil {
		t.Fatal("invalid session accepted")
	}
}
