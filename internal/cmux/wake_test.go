package cmux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const probeSurface = "3A23D5DD-59EE-471D-9270-32F0FB06E099"

// recordingCLI installs a stand-in that logs its env and args, then answers.
func recordingCLI(t *testing.T, delay, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	p := filepath.Join(dir, "cmux")
	// printf, not echo: /bin/sh's echo expands "\r", and the literal \r that
	// SendLineBounded sends after the text must stay visible in the log.
	script := "#!/bin/sh\nenv | grep '^CMUX_' >> " + log + "\nprintf 'ARGS %s\\n' \"$*\" >> " + log +
		"\nsleep " + delay + "\nprintf '%s' '" + stdout + "'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, p)
	return log
}

// send, read-screen and identify are killed at the 3 s bound
// (relayCallTimeout); the sessions listing has its own, listingCallTimeout.
func TestWakeCallsAreBounded(t *testing.T) {
	recordingCLI(t, "0.5", "")
	defer SetCallTimeoutForTest(100 * time.Millisecond)()
	if err := SendLineBounded(probeSurface, "lis ta boîte pigtail"); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("send must be killed at the bound, got %v", err)
	}
	if _, err := ReadScreenBounded(probeSurface); err == nil {
		t.Fatal("read-screen must be killed at the bound")
	}
	if _, err := FocusedSurfaceID(); err == nil {
		t.Fatal("identify must be killed at the bound")
	}
}

// the sessions listing is bounded by listingCallTimeout, the three
// others by relayCallTimeout (3 s): shrinking one must not kill the other.
// The stand-in sleeps 1 s: ten times the short bound, so the bounded calls
// are always killed, and far below the generous listing bound, so a loaded
// machine (make check, -race) never kills the listing.
func TestWakeListingUsesListingTimeout(t *testing.T) {
	recordingCLI(t, "1", "[]")
	defer SetCallTimeoutForTest(100 * time.Millisecond)()
	defer SetListingTimeoutForTest(time.Minute)()
	if _, err := SessionsJSONListing(); err != nil {
		t.Fatalf("listing must follow listingCallTimeout, got %v", err)
	}
	if err := SendLineBounded(probeSurface, "lis ta boîte pigtail"); err == nil {
		t.Fatal("send must stay at relayCallTimeout")
	}
	if _, err := ReadScreenBounded(probeSurface); err == nil {
		t.Fatal("read-screen must stay at relayCallTimeout")
	}
	if _, err := FocusedSurfaceID(); err == nil {
		t.Fatal("identify must stay at relayCallTimeout")
	}
	// the listing made at send time follows relayCallTimeout, not the
	// listing bound — it sits inside the CLI's 10 s context of msg send.
	if _, err := SessionsJSONListingBounded(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("send-time listing must be killed at relayCallTimeout, got %v", err)
	}
}

// Correction 36 (journal): cmux prefers CMUX_SURFACE_ID from the env over a
// target; a daemon call carries none, and always names the surface by UUID.
// What is checked is the env the stand-in logged: the bound is widened so a
// loaded machine cannot kill the call before it answers.
func TestWakeCallsDropCmuxEnv(t *testing.T) {
	log := recordingCLI(t, "0", "")
	defer SetCallTimeoutForTest(time.Minute)()
	t.Setenv("CMUX_SURFACE_ID", "AAAAAAAA-0000-4000-8000-000000000000")
	t.Setenv("CMUX_WORKSPACE_ID", "BBBBBBBB-0000-4000-8000-000000000000")
	if err := SendLineBounded(probeSurface, "lis ta boîte pigtail"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	if strings.Contains(got, "CMUX_") {
		t.Fatalf("a CMUX_ variable reached cmux:\n%s", got)
	}
	text := "ARGS send --surface " + probeSurface + " -- lis ta boîte pigtail\n"
	enter := "ARGS send --surface " + probeSurface + " -- \\r\n"
	i, j := strings.Index(got, text), strings.Index(got, enter)
	if i < 0 || j < 0 || i > j {
		t.Fatalf("send argv must be the text, then \\r alone:\n%s", got)
	}
	if strings.Contains(got, "pigtail\\n") {
		t.Fatalf("a \\n is still appended to the text:\n%s", got)
	}
	if strings.Count(got, "ARGS ") != 2 {
		t.Fatalf("want exactly two cmux calls:\n%s", got)
	}
}

// A second send (the Enter) that fails is named as such: the text may sit
// unsubmitted in the pane (measured 2026-10-21).
func TestSendLineBoundedEnterFailureIsNamed(t *testing.T) {
	log := recordingCLI(t, "0", "")
	defer SetCallTimeoutForTest(time.Minute)()
	// The stand-in exits 1 on the \r call only.
	p := os.Getenv(EnvBundledCLI)
	script := "#!/bin/sh\nprintf 'ARGS %s\\n' \"$*\" >> " + log + "\ncase \"$*\" in *'-- \\r') exit 1;; esac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := SendLineBounded(probeSurface, "lis ta boîte pigtail")
	if err == nil || !strings.HasPrefix(err.Error(), "enter after the line: ") {
		t.Fatalf("err = %v, want the Enter failure named", err)
	}
	if b, _ := os.ReadFile(log); strings.Count(string(b), "ARGS ") != 2 {
		t.Fatalf("want the text sent and the Enter attempted:\n%s", b)
	}
}

func TestWakeCallsRefuseBadArgs(t *testing.T) {
	log := recordingCLI(t, "0", "")
	for _, bad := range []struct{ surface, line string }{
		{"surface:12", "lis ta boîte pigtail"}, // not a UUID
		{"", "lis ta boîte pigtail"},           // empty
		{probeSurface, "--help"},               // a flag
		{probeSurface, "a\nb"},                 // a control char
	} {
		if err := SendLineBounded(bad.surface, bad.line); !errors.Is(err, ErrInvalidArg) {
			t.Fatalf("%+v: want ErrInvalidArg", bad)
		}
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("an invalid call reached cmux: %s", b)
	}
}

func TestParseFocusedSurfaceID(t *testing.T) {
	out := []byte(`{"caller":null,"focused":{"surface_id":"3a23d5dd-59ee-471d-9270-32f0fb06e099","surface_ref":"surface:98"}}`)
	if id, err := parseFocusedSurfaceID(out); err != nil || id != probeSurface {
		t.Fatalf("id = %q %v", id, err)
	}
	if id, err := parseFocusedSurfaceID([]byte(`{"caller":null}`)); err != nil || id != "" {
		t.Fatalf("no focus must read as \"\", got %q %v", id, err)
	}
	if _, err := parseFocusedSurfaceID([]byte(`not json`)); err == nil {
		t.Fatal("bad output must be an error, never \"no focus\"")
	}
}

// -cmux-socket (live-pass harness): an aimed socket is the one CMUX_*
// variable the wake calls keep, with the aimed value, whatever the process
// env says.
func TestEnvWithoutCmuxKeepsAimedSocket(t *testing.T) {
	t.Setenv("CMUX_SURFACE_ID", probeSurface)
	t.Setenv(EnvSocketPath, "/tmp/other.sock")
	SetSocketPath("/tmp/n.sock")
	t.Cleanup(func() { SetSocketPath("") })
	var got []string
	for _, kv := range envWithoutCmux() {
		if strings.HasPrefix(kv, "CMUX_") {
			got = append(got, kv)
		}
	}
	if len(got) != 1 || got[0] != EnvSocketPath+"=/tmp/n.sock" {
		t.Fatalf("CMUX_* kept: %q, want only %s=/tmp/n.sock", got, EnvSocketPath)
	}
	if SocketPath() != "/tmp/n.sock" {
		t.Fatalf("SocketPath() = %q", SocketPath())
	}
}

// Unaimed, the wake calls keep no CMUX_* at all (guard of the « cmux notify
// prefers CMUX_SURFACE_ID » correction).
func TestEnvWithoutCmuxUnaimedStripsAll(t *testing.T) {
	t.Setenv("CMUX_SURFACE_ID", probeSurface)
	t.Setenv(EnvSocketPath, "/tmp/other.sock")
	SetSocketPath("")
	n := 0
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CMUX_") {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("positive control: the process env carries %d CMUX_* variables, want ≥ 2", n)
	}
	for _, kv := range envWithoutCmux() {
		if strings.HasPrefix(kv, "CMUX_") {
			t.Errorf("unaimed wake env kept %q", kv)
		}
	}
}

// CheckLine is the one validation of a typed line, exported for the account
// rotation's resume prompt and called by
// SendLineBounded itself.
func TestCheckLine(t *testing.T) {
	if err := CheckLine("lis ta boîte pigtail"); err != nil {
		t.Fatalf("plain line refused: %v", err)
	}
	for _, bad := range []string{"", "  ", "--help", "a\nb", strings.Repeat("é", maxTextRunes+1)} {
		if err := CheckLine(bad); !errors.Is(err, ErrInvalidArg) {
			t.Fatalf("%q: want ErrInvalidArg, got %v", bad, err)
		}
	}
	if err := CheckLine(strings.Repeat("é", maxTextRunes)); err != nil {
		t.Fatalf("%d runes refused: %v", maxTextRunes, err)
	}
}

// SendLineBounded refuses what CheckLine refuses, before cmux runs.
func TestSendLineBoundedUsesCheckLine(t *testing.T) {
	log := recordingCLI(t, "0", "")
	if err := SendLineBounded(probeSurface, strings.Repeat("x", maxTextRunes+1)); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("want ErrInvalidArg, got %v", err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("an invalid call reached cmux: %s", b)
	}
}

// pigtail session resume: the bare Enter at the
// launcher's ↻ prompt, one call, by UUID, no CMUX_ variable but the socket.
func TestSendEnterBoundedArgs(t *testing.T) {
	log := recordingCLI(t, "0", "")
	defer SetCallTimeoutForTest(time.Minute)()
	t.Setenv("CMUX_SURFACE_ID", "AAAAAAAA-0000-4000-8000-000000000000")
	t.Setenv("CMUX_WORKSPACE_ID", "BBBBBBBB-0000-4000-8000-000000000000")
	if err := SendEnterBounded(probeSurface); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "CMUX_") && !strings.HasPrefix(l, "CMUX_SOCKET_PATH=") {
			t.Fatalf("a CMUX_ variable reached cmux: %s", l)
		}
	}
	if want := "ARGS send --surface " + probeSurface + " -- \\r\n"; !strings.Contains(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if strings.Count(got, "ARGS ") != 1 {
		t.Fatalf("want exactly one cmux call:\n%s", got)
	}
}

func TestSendEnterRefusesNonUUID(t *testing.T) {
	log := recordingCLI(t, "0", "")
	if err := SendEnterBounded("surface:3"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("err = %v, want ErrInvalidArg", err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("an invalid call reached cmux: %s", b)
	}
}
