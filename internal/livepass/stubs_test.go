package livepass

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
)

// The doubles of scripts/live-pass.sh: every tool the script drives (open,
// lsappinfo, ioreg, mac-idle, both cmux CLIs, osascript, pigtaild) is a small
// sh script under the rig's root, journaling its name, argv and sorted CMUX_*
// variables to <root>/calls.log. No test can launch an app: the « NIGHTLY »
// the fake open starts is a plain sleep.

const (
	winW     = "11111111-1111-4111-8111-111111111111"
	winK     = "22222222-2222-4222-8222-222222222222"
	winDef   = "33333333-3333-4333-8333-333333333333" // the window NIGHTLY opens by itself
	wsDef    = "DDDDDDDD-0000-4000-8000-00000000000D"
	wsW      = "AAAAAAAA-0000-4000-8000-00000000000A"
	wsK      = "BBBBBBBB-0000-4000-8000-00000000000B"
	mainWin  = "6A73C8EF-1392-4C1C-9EFD-0C91EAD493E5"
	mainWin2 = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C"
	mainWs1  = "D9733B18-531F-4A5E-81E1-ABB7536E3CBF"
	mainWs2  = "FA706FC3-D5E3-49FB-A623-5AF8229F4D74"

	nightlyASN = `ASN:0x0-0x2002-"cmux NIGHTLY":`
	versionA   = "0.64.20-nightly.A"
)

type rig struct {
	t    *testing.T
	root string

	// Knobs read when the rig is built (newRig) or at each run.
	callerEnv     []string
	extraEnv      []string
	defaultLedger bool
	focusMin      int
	pidDelay      int
	socketDelay   int
	// socketWait and stopWait are the script's LIVEPASS_SOCKET_WAIT and
	// LIVEPASS_STOP_WAIT: the script's own defaults (20 s, 10 s) unless a
	// test expects the wait to expire (a dead socket, a stubborn process),
	// which shortens it. A wide wait ends at the event; only a hang reaches it.
	socketWait int
	stopWait   int

	stubborn []int // processes the test started that ignore SIGTERM
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func scriptPath(t *testing.T) string { return filepath.Join(repoRoot(t), "scripts", "live-pass.sh") }

// newRig builds the doubles under a short root in /tmp: t.TempDir() lives
// under /var/folders/…/T/TestXxx…/001, past the 103-byte socket limit once
// lp.XXXXXX/pigtaild.sock is added.
func newRig(t *testing.T) *rig {
	t.Helper()
	root := testutil.MkdirTemp(t, "lp-")
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(root + "/root/lp.XXXXXX/pigtaild.sock"); n > 103 {
		t.Fatalf("rig root too long for a socket: %d bytes", n)
	}
	r := &rig{t: t, root: root, focusMin: 3, socketWait: 20, stopWait: 10}
	t.Cleanup(r.cleanup)
	for _, d := range []string{"bin", "root", "home", "main.app/Contents/Resources/bin",
		"cmux NIGHTLY.app/Contents/Resources/bin"} {
		r.mkdir(d)
	}
	r.write("bin/open", 0o755, fakeOpen)
	r.write("bin/lsappinfo", 0o755, fakeLsappinfo)
	r.write("bin/ioreg", 0o755, fakeIoreg)
	r.write("bin/mac-idle", 0o755, fakeMacFree)
	r.write("bin/osascript", 0o755, journal("osascript"))
	r.write("bin/cmux", 0o755, journal("TRAP path-cmux"))
	r.write("trap-cli", 0o755, journal("TRAP bundled-cli"))
	r.write("main.app/Contents/Resources/bin/cmux", 0o755, fakeMainCLI)
	r.write("cmux NIGHTLY.app/Contents/Resources/bin/cmux", 0o755, fakeNightlyCLI)
	r.setVersion(versionA)
	r.write("live.log", 0o644, "")
	r.write("front", 0o644, "Arc")
	r.setMainWindows(`[{"id" : "` + mainWin + `", "index" : 0, "key" : true, "selected_workspace_id" : "` + mainWs1 + `", "workspace_count" : 2}]`)
	r.seedLedger(cleanLines(3)...)
	return r
}

func (r *rig) path(rel string) string { return filepath.Join(r.root, rel) }

func (r *rig) mkdir(rel string) {
	r.t.Helper()
	if err := os.MkdirAll(r.path(rel), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) write(rel string, mode os.FileMode, body string) {
	r.t.Helper()
	if err := os.WriteFile(r.path(rel), []byte(body), mode); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) touch(rel string)  { r.write(rel, 0o644, "") }
func (r *rig) remove(rel string) { _ = os.Remove(r.path(rel)) }
func (r *rig) setFront(app string) {
	r.t.Helper()
	tmp := r.path("front.tmp")
	if err := os.WriteFile(tmp, []byte(app), 0o644); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(tmp, r.path("front")); err != nil {
		r.t.Fatal(err)
	}
}
func (r *rig) setMainWindows(json string) { r.write("main-windows.json", 0o644, json) }
func (r *rig) setMacFree(code int)        { r.write("macfree-rc", 0o644, strconv.Itoa(code)) }

func (r *rig) setVersion(v string) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>com.cmuxterm.app.nightly</string>
`
	if v != "" {
		body += "\t<key>CFBundleShortVersionString</key>\n\t<string>" + v + "</string>\n"
	}
	body += "</dict>\n</plist>\n"
	r.write("cmux NIGHTLY.app/Contents/Info.plist", 0o644, body)
}

func cleanLines(n int) []string { return ledgerLines("clean", versionA, n) }

func ledgerLines(verdict, version string, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, "2026-10-19T10:00:00Z "+verdict+" "+version)
	}
	return out
}

// seedLedger replaces the D2 ledger (no line = no file: a first pass).
func (r *rig) seedLedger(lines ...string) {
	r.t.Helper()
	p := r.path("ledger")
	_ = os.Remove(p)
	if len(lines) == 0 {
		return
	}
	r.write("ledger", 0o644, strings.Join(lines, "\n")+"\n")
}

func (r *rig) ledger() string {
	b, _ := os.ReadFile(r.ledgerPath())
	return string(b)
}

func (r *rig) ledgerPath() string { return r.path("ledger") }

// lastVerdict is the verdict and reason of the ledger's last line.
func (r *rig) lastVerdict() (verdict, reason string) {
	lines := strings.Split(strings.TrimSpace(r.ledger()), "\n")
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 3 {
		return "", ""
	}
	return f[1], strings.Join(f[3:], " ")
}

func (r *rig) appendLiveLog(line string) {
	r.t.Helper()
	f, err := os.OpenFile(r.path("live.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintln(f, line); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) nightlySocket() string { return r.path("n.sock") }
func (r *rig) nightlyCLI() string {
	return r.path("cmux NIGHTLY.app/Contents/Resources/bin/cmux")
}

func (r *rig) env() []string {
	env := []string{
		"PATH=" + r.path("bin") + ":/usr/bin:/bin",
		"HOME=" + r.path("home"),
		"RIG=" + r.root,
		"LIVEPASS_NIGHTLY_APP=" + r.path("cmux NIGHTLY.app"),
		"LIVEPASS_NIGHTLY_SOCKET=" + r.nightlySocket(),
		"LIVEPASS_MAIN_APP=" + r.path("main.app"),
		"LIVEPASS_OPEN=" + r.path("bin/open"),
		"LIVEPASS_LSAPPINFO=" + r.path("bin/lsappinfo"),
		"LIVEPASS_IOREG=" + r.path("bin/ioreg"),
		"LIVEPASS_MAC_FREE=" + r.path("bin/mac-idle"),
		"LIVEPASS_ROOT=" + r.path("root"),
		"LIVEPASS_LIVE_LOG=" + r.path("live.log"),
		"LIVEPASS_SOCKET_WAIT=" + strconv.Itoa(r.socketWait),
		"LIVEPASS_STOP_WAIT=" + strconv.Itoa(r.stopWait),
		"LIVEPASS_FOCUS_EVERY=0.2",
		"LIVEPASS_FOCUS_MIN=" + strconv.Itoa(r.focusMin),
		"RIG_PID_DELAY=" + strconv.Itoa(r.pidDelay),
		"RIG_SOCKET_DELAY=" + strconv.Itoa(r.socketDelay),
	}
	if !r.defaultLedger {
		env = append(env, "LIVEPASS_LEDGER="+r.ledgerPath())
	}
	caller := r.callerEnv
	if caller == nil {
		caller = []string{
			"CMUX_SURFACE_ID=80914656-BF88-4780-9982-CA1665755494",
			"CMUX_WORKSPACE_ID=B89B4597-2397-45F6-81D9-95F3B0FA83F8",
			"CMUX_SOCKET=" + r.path("main.sock"),
			"CMUX_SOCKET_PATH=" + r.path("main.sock"),
			"CMUX_BUNDLED_CLI_PATH=" + r.path("trap-cli"),
		}
	}
	env = append(env, caller...)
	return append(env, r.extraEnv...)
}

// runBash runs `bash <script> args…` with env; a background child that keeps
// stdout/stderr open surfaces as exec.ErrWaitDelay instead of blocking.
func runBash(script string, env []string, args ...string) (stdout, stderr string, code int, err error) {
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = env
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	code = 0
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrWaitDelay):
		return o.String(), e.String(), -1, err
	case errors.As(err, &ee):
		code, err = ee.ExitCode(), nil
	default:
		return o.String(), e.String(), -1, err
	}
	return o.String(), e.String(), code, err
}

func (r *rig) run(args ...string) (stdout, stderr string, code int) {
	r.t.Helper()
	o, e, c, err := runBash(scriptPath(r.t), r.env(), args...)
	if errors.Is(err, exec.ErrWaitDelay) {
		r.t.Fatalf("background child kept stdout/stderr open (%v): stdout %q stderr %q", args, o, e)
	}
	if err != nil {
		r.t.Fatalf("run %v: %v", args, err)
	}
	if m := bashErrorLine.FindString(e); m != "" {
		r.t.Fatalf("%v: bash reported an error of the script itself (%q)\nstderr:\n%s", args, m, e)
	}
	return o, e, c
}

// bashErrorLine is an error bash writes about the script's own code — an
// unset variable under set -u, a syntax or arithmetic error — never one of
// the script's messages (live-pass: …). Every run of the suite goes through
// rig.run, so any test catches it (verify round 1 of PR #228: PASS_IDS
// unbound at up went unseen because no test read up's stderr).
var bashErrorLine = regexp.MustCompile(`(?m)^.*live-pass\.sh: line [0-9]+: .*$|unbound variable`)

// mustRun runs and requires exit code want.
func (r *rig) mustRun(want int, args ...string) (stdout, stderr string) {
	r.t.Helper()
	o, e, c := r.run(args...)
	if c != want {
		r.t.Fatalf("%v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s\ncalls:\n%s", args, c, want, o, e, r.tailCalls(40))
	}
	return o, e
}

func (r *rig) calls() string {
	b, _ := os.ReadFile(r.path("calls.log"))
	return string(b)
}

func (r *rig) tailCalls(n int) string {
	lines := strings.Split(strings.TrimSpace(r.calls()), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// callLines are the calls.log lines of one double (its journal name).
func (r *rig) callLines(name string) []string {
	var out []string
	for _, l := range strings.Split(r.calls(), "\n") {
		if strings.HasPrefix(l, name+" ") {
			out = append(out, l)
		}
	}
	return out
}

func (r *rig) countCalls(name, argsPrefix string) int {
	n := 0
	for _, l := range r.callLines(name) {
		args, _, _ := strings.Cut(strings.TrimPrefix(l, name+" "), " ||")
		if strings.HasPrefix(args, argsPrefix) {
			n++
		}
	}
	return n
}

// stateDir is the pass state directory lp.current points at, "" when none.
func (r *rig) stateDir() string {
	p, err := filepath.EvalSymlinks(r.path("root/lp.current"))
	if err != nil {
		return ""
	}
	return p
}

func (r *rig) passDirs() []string {
	m, _ := filepath.Glob(r.path("root/lp.*"))
	return m
}

// state reads the KEY=value lines of the current pass's state.env, through
// bash itself (values are %q-quoted).
func (r *rig) state() map[string]string {
	r.t.Helper()
	dir := r.stateDir()
	if dir == "" {
		return nil
	}
	return readState(r.t, filepath.Join(dir, "state.env"))
}

func readState(t *testing.T, file string) map[string]string {
	t.Helper()
	out, err := exec.Command("bash", append([]string{"-c",
		`. "$1"; shift; for k in "$@"; do printf '%s=%s\0' "$k" "${!k-}"; done`, "bash", file}, stateKeys...)...).Output()
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	m := map[string]string{}
	for _, kv := range strings.Split(string(out), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

var stateKeys = []string{"W", "K", "NIGHTLY_PID", "NIGHTLY_LAUNCHED", "NIGHTLY_VERSION", "MAIN_WINDOWS",
	"MAIN_KEY_WINDOW", "LIVE_LOG_OFFSET", "DAEMON_PID", "FRONT_AT_UP", "SAMPLER_PID",
	"MAC_FREE_ASKED", "STATUS_RUNS", "ISOLATION_BROKEN", "PASS_IDS", "MAIN_IDS",
	"PASS_WS_SEEN_W", "PASS_WS_SEEN_K", "NIGHTLY_WINDOWS"}

// setState rewrites one key of the current pass's state.env.
func (r *rig) setState(key, val string) {
	r.t.Helper()
	f := filepath.Join(r.stateDir(), "state.env")
	b, err := os.ReadFile(f)
	if err != nil {
		r.t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if !strings.HasPrefix(l, key+"=") {
			keep = append(keep, l)
		}
	}
	keep = append(keep, key+"="+strconv.Quote(val))
	if err := os.WriteFile(f, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) passFile(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.stateDir(), name))
	return string(b)
}

func lineCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// nightlyPid is the pid the fake open wrote for the fake NIGHTLY.
func (r *rig) nightlyPid() int {
	b, err := os.ReadFile(r.path("nightly.pid"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// startNightly starts a fake NIGHTLY as if the user had launched it.
func (r *rig) startNightly() int {
	r.t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	r.write("nightly.pid", 0o644, strconv.Itoa(cmd.Process.Pid))
	return cmd.Process.Pid
}

// startStubborn starts a process that ignores SIGTERM.
func (r *rig) startStubborn() int {
	r.t.Helper()
	cmd := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	r.stubborn = append(r.stubborn, cmd.Process.Pid)
	return cmd.Process.Pid
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// A zombie still answers kill -0: ps tells whether it is defunct.
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

// waitReadable waits until focus-readable.log holds n lines. The sampler's
// pace depends on machine load (each tick forks the stubs), so the clock is
// no judge: it gives up only when the count stops advancing for a stall
// period (the sampler is dead or blind), or at a generous hard cap.
func (r *rig) waitReadable(n int) {
	r.t.Helper()
	const stall, hardCap = 15 * time.Second, 3 * time.Minute
	last, lastMove, start := -1, time.Now(), time.Now()
	for {
		got := lineCount(r.passFile("focus-readable.log"))
		if got >= n {
			return
		}
		if got != last {
			last, lastMove = got, time.Now()
		}
		if time.Since(lastMove) > stall || time.Since(start) > hardCap {
			r.t.Fatalf("timed out after %v waiting for %d readable focus samples (got %d)", time.Since(start), n, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitSamplerTicks waits for n new sampler ticks (ioreg calls, readable or
// not). Like waitReadable, the clock is no judge under load: it gives up only
// when the count stops advancing for a stall period, or at a generous hard cap.
func (r *rig) waitSamplerTicks(n int) {
	r.t.Helper()
	const stall, hardCap = 15 * time.Second, 3 * time.Minute
	start := r.countCalls("ioreg", "")
	last, lastMove, began := start, time.Now(), time.Now()
	for {
		got := r.countCalls("ioreg", "")
		if got >= start+n {
			return
		}
		if got != last {
			last, lastMove = got, time.Now()
		}
		if time.Since(lastMove) > stall || time.Since(began) > hardCap {
			r.t.Fatalf("timed out after %v waiting for %d sampler ticks (got %d)", time.Since(began), n, got-start)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *rig) cleanup() {
	pids := append([]int{}, r.stubborn...)
	pids = append(pids, r.nightlyPid())
	for _, d := range r.passDirs() {
		st := filepath.Join(d, "state.env")
		if _, err := os.Stat(st); err == nil {
			out, _ := exec.Command("bash", "-c", `. "$1"; echo "${SAMPLER_PID:-} ${DAEMON_PID:-} ${NIGHTLY_PID:-}"`, "bash", st).Output()
			for _, f := range strings.Fields(string(out)) {
				if n, err := strconv.Atoi(f); err == nil {
					pids = append(pids, n)
				}
			}
		}
	}
	if b, err := os.ReadFile(r.path("daemon.pids")); err == nil {
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				pids = append(pids, n)
			}
		}
	}
	for _, p := range pids {
		if p > 0 {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	}
	_ = os.Chmod(r.path("root"), 0o755)
	_ = os.Chmod(r.ledgerPath(), 0o644)
	_ = os.RemoveAll(r.root)
}

// envOf parses the " ||" CMUX_* part of a journal line.
func envOf(line string) []string {
	_, env, ok := strings.Cut(line, " ||")
	if !ok {
		return nil
	}
	f := strings.Fields(env)
	sort.Strings(f)
	return f
}

func argsOf(name, line string) string {
	a, _, _ := strings.Cut(strings.TrimPrefix(line, name+" "), " ||")
	return a
}

// journal is a double that only records it was called.
func journal(name string) string {
	return "#!/bin/sh\n" + journalLine(name) + "exit 0\n"
}

// journalLine appends `<name> <argv> || <sorted CMUX_*>` to calls.log.
func journalLine(name string) string {
	return `printf '%s %s ||%s\n' "` + name + `" "$*" " $(env | grep '^CMUX_' | sort | tr '\n' ' ')" >> "$RIG/calls.log"` + "\n"
}

var fakeOpen = "#!/bin/sh\n" + journalLine("open") + `
if [ -f "$RIG/nightly-stubborn" ]; then
	sh -c 'trap "" TERM; while :; do sleep 1; done' >/dev/null 2>&1 </dev/null &
else
	sleep 600 >/dev/null 2>&1 </dev/null &
fi
echo $! > "$RIG/nightly.pid"
echo "${RIG_PID_DELAY:-0}" > "$RIG/pid-delay-left"
exit 0
`

var fakeLsappinfo = "#!/bin/sh\n" + journalLine("lsappinfo") + `
nightly_alive() {
	[ -f "$RIG/nightly.pid" ] || return 1
	p=$(cat "$RIG/nightly.pid")
	[ -n "$p" ] && kill -0 "$p" 2>/dev/null || return 1
	st=$(ps -o stat= -p "$p" 2>/dev/null)
	case $st in Z*) return 1 ;; esac
	return 0
}
case "$1" in
find)
	[ -f "$RIG/lsappinfo-blind" ] && exit 0
	case "$2" in
	name=loginwindow) echo 'ASN:0x0-0x1001-"loginwindow":' ;;
	bundleid=com.cmuxterm.app.nightly)
		nightly_alive || exit 0
		left=$(cat "$RIG/pid-delay-left" 2>/dev/null || echo 0)
		if [ "${left:-0}" -gt 0 ]; then
			echo $((left - 1)) > "$RIG/pid-delay-left"
			exit 0
		fi
		echo 'ASN:0x0-0x2002-"cmux NIGHTLY":'
		;;
	esac
	;;
front)
	f=$(cat "$RIG/front" 2>/dev/null)
	[ -n "$f" ] && echo 'ASN:0x0-0x3003:'
	;;
info)
	what=$3 asn=$4
	if [ -f "$RIG/lsappinfo-v27" ]; then
		# macOS 27 block shapes (measured 27.0.1).
		case "$what" in
		name)
			case "$asn" in
			ASN:0x0-0x3003:*)
				f=$(cat "$RIG/front" 2>/dev/null)
				case "$f" in
				@null) printf '[ NULL ]  [ NULL ]  \n    bundleID=[ NULL ] \n' ;;
				@garbage) echo 'something else' ;;
				*) printf '"%s" ASN:0x0-0x3003: (in front) \n    bundleID=[ NULL ] \n    bundle path=[ NULL ] \n    executable path=[ NULL ] \n !cgsConnection !signalled type=[ NULL ]  flavor=[ NULL ]  Version=[ NULL ]  Arch=!!none \n' "$f" ;;
				esac
				;;
			*) printf '[ NULL ]  [ NULL ]  \n    bundleID=[ NULL ] \n' ;;
			esac
			;;
		pid)
			printf '[ NULL ]  [ NULL ]  \n    bundleID=[ NULL ] \n    bundle path=[ NULL ] \n    executable path=[ NULL ] \n'
			if [ "$asn" = 'ASN:0x0-0x2002-"cmux NIGHTLY":' ] && nightly_alive; then
				if [ -f "$RIG/pid-garbage" ]; then
					if grep -q junk "$RIG/pid-garbage"; then p='4242x'; else p='[ NULL ]'; fi
				else
					p=$(cat "$RIG/nightly.pid")
				fi
			else
				p='[ NULL ]'
			fi
			printf '    pid = %s !cgsConnection !signalled type=[ NULL ]  flavor=[ NULL ]  Version=[ NULL ]  Arch=!!none \n' "$p"
			;;
		esac
		exit 0
	fi
	case "$what" in
	name)
		case "$asn" in
		'ASN:0x0-0x1001-"loginwindow":') echo '"LSDisplayName"="loginwindow"' ;;
		ASN:0x0-0x3003:*)
			f=$(cat "$RIG/front" 2>/dev/null)
			case "$f" in
			@null) echo '"LSDisplayName"=[ NULL ]' ;;
			@garbage) echo 'LSDisplayName: something' ;;
			*) echo "\"LSDisplayName\"=\"$f\"" ;;
			esac
			;;
		*) echo '"LSDisplayName"=[ NULL ]' ;;
		esac
		;;
	pid)
		if [ "$asn" = 'ASN:0x0-0x2002-"cmux NIGHTLY":' ] && nightly_alive; then
			if [ -f "$RIG/pid-garbage" ]; then
				if grep -q junk "$RIG/pid-garbage"; then echo 'pid: 4242x'; else echo '"pid"=[ NULL ] '; fi
			else
				echo "\"pid\"=$(cat "$RIG/nightly.pid")"
			fi
		else
			echo '"pid"=[ NULL ] '
		fi
		;;
	esac
	;;
esac
exit 0
`

var fakeIoreg = "#!/bin/sh\n" + journalLine("ioreg") + `
[ -f "$RIG/ioreg-empty" ] && exit 0
printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0">\n<dict>\n\t<key>IOConsoleLocked</key>\n\t<false/>\n\t<key>IOConsoleUsers</key>\n\t<array>\n\t\t<dict>\n\t\t\t<key>kCGSSessionOnConsoleKey</key>\n\t\t\t<true/>\n'
[ -f "$RIG/locked" ] && printf '\t\t\t<key>CGSSessionScreenIsLocked</key>\n\t\t\t<true/>\n'
printf '\t\t</dict>\n\t</array>\n</dict>\n</plist>\n'
exit 0
`

var fakeMacFree = "#!/bin/sh\n" + journalLine("mac-idle") + `
[ "$1" = --release ] && exit 0
rc=$(cat "$RIG/macfree-rc" 2>/dev/null || echo 0)
if [ "$rc" = 0 ]; then echo "free (test)"; else echo "busy (test)"; fi
exit "$rc"
`

var fakeMainCLI = "#!/bin/sh\n" + journalLine("main") + `
[ -f "$RIG/main-list-fails" ] && exit 1
case "$*" in
"--json list-windows") cat "$RIG/main-windows.json" ;;
"--json list-workspaces --window "*)
	w=$4
	if [ -f "$RIG/main-ws-empty" ]; then printf '{"window_ref" : "window:1", "workspaces" : []}'; exit 0; fi
	if [ "$w" = "` + mainWin + `" ]; then
		printf '{"window_ref" : "window:1", "workspaces" : [{"cloud_vm_id" : null, "id" : "` + mainWs1 + `", "index" : 0}, {"id" : "` + mainWs2 + `", "index" : 1}]}'
	else
		printf '{"window_ref" : "window:2", "workspaces" : []}'
	fi
	;;
*) exit 1 ;;
esac
exit 0
`

var fakeNightlyCLI = "#!/bin/sh\n" + journalLine("nightly") + `
case "$1" in
list-windows)
	[ -f "$RIG/socket-dead" ] && exit 1
	left=$(cat "$RIG/socket-left" 2>/dev/null || echo "${RIG_SOCKET_DELAY:-0}")
	if [ "${left:-0}" -gt 0 ]; then
		echo $((left - 1)) > "$RIG/socket-left"
		echo "Error: socket not answering" >&2
		exit 1
	fi
	echo 0 > "$RIG/socket-left"
	echo '[]'
	;;
new-window)
	n=$(cat "$RIG/nw-count" 2>/dev/null || echo 0)
	echo $((n + 1)) > "$RIG/nw-count"
	if [ "$n" = 0 ]; then echo "OK ` + winW + `"; else echo "OK ` + winK + `"; fi
	;;
close-window)
	[ "$2" = --window ] && [ -n "$3" ] || exit 1
	echo OK
	;;
new-workspace)
	n=$(cat "$RIG/ws-count" 2>/dev/null || echo 0)
	echo $((n + 1)) > "$RIG/ws-count"
	win=""
	prev=""
	for a in "$@"; do
		[ "$prev" = --window ] && win=$a
		prev=$a
	done
	printf 'CCCCCCCC-0000-4000-8000-%012d\n' "$((n + 1))" >> "$RIG/ws-$win"
	echo "OK workspace:$((n + 10))"
	;;
--json)
	case "$2 $3" in
	"list-windows ")
		printf '[{"id" : "` + winDef + `", "key" : true}]'
		;;
	"list-workspaces --window")
		w=$4
		[ -f "$RIG/pass-ws-empty-$w" ] && { printf '{"workspaces":[]}'; exit 0; }
		case "$w" in
		` + winW + `) first=` + wsW + ` ;;
		` + winK + `) first=` + wsK + ` ;;
		` + winDef + `) first=` + wsDef + ` ;;
		*) first="" ;;
		esac
		printf '{"window_ref" : "window:9", "workspaces" : ['
		sep=""
		if [ -n "$first" ]; then printf '{"cloud_vm_id" : null, "id" : "%s", "index" : 0}' "$first"; sep=", "; fi
		if [ -f "$RIG/ws-$w" ]; then
			while read -r id; do printf '%s{"id" : "%s"}' "$sep" "$id"; sep=", "; done < "$RIG/ws-$w"
		fi
		printf ']}'
		;;
	*) exit 1 ;;
	esac
	;;
*) exit 1 ;;
esac
exit 0
`

// writePigtaild installs the fake branch pigtaild and returns its path. Knobs
// are files under the root: old-pigtaild (a binary without -cmux-socket),
// silent-check-args (exit 0, no ok line), no-aim-line, die-at-birth.
func (r *rig) writePigtaild() string {
	r.write("bin/pigtaild", 0o755, fakePigtaild)
	return r.path("bin/pigtaild")
}

var fakePigtaild = `#!/bin/sh
if [ "$1" = -check-args ]; then
	shift
	printf 'pigtaild-check %s\n' "$*" >> "$RIG/calls.log"
	if [ -f "$RIG/old-pigtaild" ]; then
		echo "flag provided but not defined: -cmux-socket" >&2
		exit 2
	fi
	[ -f "$RIG/silent-check-args" ] && exit 0
	echo "pigtaild -check-args: ok"
	exit 0
fi
printf 'pigtaild-run %s\n' "$*" >> "$RIG/calls.log"
env | sort > "$RIG/daemon-env.$$"
echo $$ >> "$RIG/daemon.pids"
if [ -f "$RIG/old-pigtaild" ]; then
	echo "flag provided but not defined: -cmux-socket" >&2
	exit 2
fi
sock="" aim=""
prev=""
for a in "$@"; do
	[ "$prev" = -socket ] && sock=$a
	[ "$prev" = -cmux-socket ] && aim=$a
	prev=$a
done
if [ -f "$RIG/die-at-birth" ]; then
	echo "2026/10/20 10:00:00 fatal: open store: database is locked (fake)" >&2
	exit 1
fi
[ -f "$RIG/no-aim-line" ] || echo "2026/10/20 10:00:00 cmux: every call aimed at socket $aim (-cmux-socket)" >&2
: > "$sock"
exec sleep 600
`

// daemonEnv is the environment the fake pigtaild ran with (sorted lines).
func (r *rig) daemonEnv(pid string) []string {
	b, err := os.ReadFile(r.path("daemon-env." + pid))
	if err != nil {
		r.t.Fatalf("daemon env of %s: %v", pid, err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}
