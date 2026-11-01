#!/usr/bin/env bash
# live-pass.sh — a live verify pass on cmux NIGHTLY, never on the user's cmux.
#
# Throwaway cmux windows (W, K) in a second cmux instance (cmux NIGHTLY,
# launched in the background), proven isolated from the main cmux, without
# taking the user's focus.
# Run `scripts/live-pass.sh help` for the commands.
#
# Every external tool goes through a LIVEPASS_* variable (tests use doubles,
# internal/livepass); every cmux call runs with no CMUX_* variable of the
# caller (C9). bash 3.2 (macOS), POSIX tools only for what CI runs on Linux.

set -u

: "${LIVEPASS_NIGHTLY_APP:=/Applications/cmux NIGHTLY.app}"
: "${LIVEPASS_NIGHTLY_CLI:=$LIVEPASS_NIGHTLY_APP/Contents/Resources/bin/cmux}"
: "${LIVEPASS_NIGHTLY_SOCKET:=/tmp/cmux-nightly.sock}"
: "${LIVEPASS_MAIN_APP:=/Applications/cmux.app}"
: "${LIVEPASS_MAIN_CLI:=$LIVEPASS_MAIN_APP/Contents/Resources/bin/cmux}"
: "${LIVEPASS_OPEN:=open}"
: "${LIVEPASS_LSAPPINFO:=lsappinfo}"
: "${LIVEPASS_IOREG:=ioreg}"
: "${LIVEPASS_MAC_FREE:=mac-idle}"
: "${LIVEPASS_LIVE_LOG:=$HOME/.config/pigtail/daemon.log}"
: "${LIVEPASS_LEDGER:=${XDG_STATE_HOME:-$HOME/.local/state}/pigtail-live-pass/ledger}"
: "${LIVEPASS_SOCKET_WAIT:=20}"
: "${LIVEPASS_STOP_WAIT:=10}"
: "${LIVEPASS_FOCUS_EVERY:=1}"
: "${LIVEPASS_FOCUS_MIN:=60}"
if [ -z "${LIVEPASS_ROOT:-}" ]; then
	LIVEPASS_ROOT=/tmp
	_claude_tmp="/private/tmp/claude-$(id -u)"
	if [ -d "$_claude_tmp" ] && [ -w "$_claude_tmp" ]; then
		LIVEPASS_ROOT=$_claude_tmp
	fi
fi

NIGHTLY_BUNDLE_ID=com.cmuxterm.app.nightly
CURRENT="$LIVEPASS_ROOT/lp.current"
SOCK_MAX=103
D2_PASSES=3
STATE_DIR=""

die() {
	echo "live-pass: $2" >&2
	exit "$1"
}

say() { echo "live-pass: $*" >&2; }

iso_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# --- cmux, with the caller's CMUX_* removed (C9) ---------------------------

# strip_cmux unsets every exported CMUX_* variable of the current (sub)shell.
strip_cmux() {
	local v
	for v in $(compgen -e); do
		case $v in CMUX_*) unset "$v" ;; esac
	done
}

# cmux_clean runs a command with no CMUX_* variable of the caller.
cmux_clean() { (strip_cmux; exec "$@"); }

# nightly_cli: the NIGHTLY CLI, aimed at the NIGHTLY socket and nothing else.
nightly_cli() {
	(
		strip_cmux
		export CMUX_SOCKET_PATH="$LIVEPASS_NIGHTLY_SOCKET"
		exec "$LIVEPASS_NIGHTLY_CLI" "$@"
	)
}

# main_cli: the main cmux's own CLI (never PATH, never CMUX_BUNDLED_CLI_PATH),
# no CMUX_* at all: it reaches the main cmux by its default socket.
main_cli() { (strip_cmux; exec "$LIVEPASS_MAIN_CLI" "$@"); }

# --- state ----------------------------------------------------------------

state_file() { echo "$STATE_DIR/state.env"; }

# state_set KEY VALUE rewrites state.env (temp file + mv, never half written).
state_set() {
	local f tmp
	f=$(state_file)
	tmp="$f.tmp.$$"
	{
		[ -f "$f" ] && grep -v "^$1=" "$f"
		printf '%s=%q\n' "$1" "$2"
	} >"$tmp"
	mv "$tmp" "$f"
	eval "$1=\$2"
}

# state_defaults: every state variable at its empty value — the one init of
# up (a new pass) and load_state (before sourcing state.env), so no state
# variable is ever read unset under set -u.
state_defaults() {
	W="" K="" NIGHTLY_PID="" NIGHTLY_LAUNCHED=0 NIGHTLY_VERSION="" MAIN_WINDOWS=""
	# shellcheck disable=SC2034 # MAIN_KEY_WINDOW, FRONT_AT_UP: state kept for the reader
	MAIN_KEY_WINDOW="" LIVE_LOG_OFFSET="" DAEMON_PID="" FRONT_AT_UP="" SAMPLER_PID=""
	MAC_FREE_ASKED=0 STATUS_RUNS=0 ISOLATION_BROKEN=0 PASS_IDS="" MAIN_IDS=""
	NIGHTLY_WINDOWS="" PASS_WS_SEEN_W=0 PASS_WS_SEEN_K=0
}

load_state() {
	[ -L "$CURRENT" ] && [ -d "$CURRENT" ] || return 1
	STATE_DIR=$(cd "$CURRENT" && pwd -P) || return 1
	state_defaults
	# shellcheck disable=SC1090
	. "$(state_file)"
}

# --- focus (C10, C11, I2) -------------------------------------------------

# front_app prints the frontmost app's name (exit 0), or
# "not measured: <reason>" (exit 1): an unreadable sample is never "not NIGHTLY".
front_app() {
	local lock asn out name
	lock=$("$LIVEPASS_IOREG" -n Root -d1 -a 2>/dev/null)
	case $lock in
	*'<key>IOConsoleUsers</key>'*) ;;
	*) echo "not measured: lock state unreadable"; return 1 ;;
	esac
	case $lock in
	*'<key>CGSSessionScreenIsLocked</key>'*) echo "not measured: screen locked"; return 1 ;;
	esac
	asn=$("$LIVEPASS_LSAPPINFO" front 2>/dev/null | head -n 1)
	asn=$(printf '%s' "$asn" | tr -d '\r\n')
	if [ -z "$asn" ]; then
		echo "not measured: empty"
		return 1
	fi
	out=$("$LIVEPASS_LSAPPINFO" info -only name "$asn" 2>/dev/null)
	# Old: "LSDisplayName"="<Name>". macOS 27: a leading "<Name>" ASN:… line.
	name=$(printf '%s\n' "$out" | sed -n -e 's/^"LSDisplayName"="\(.*\)" *$/\1/p' -e 's/^"\(.*\)" ASN:[^ ]*\( .*\)\{0,1\}$/\1/p' | head -n 1)
	if [ -z "$name" ]; then
		echo "not measured: unrecognized"
		return 1
	fi
	if [ "$name" = loginwindow ]; then
		echo "not measured: loginwindow"
		return 1
	fi
	echo "$name"
}

NIGHTLY_NAME="cmux NIGHTLY"

# focus_sample <source>: one sample, filed (focus.log when NIGHTLY is
# frontmost, focus-readable.log when readable, focus-unmeasured.log when not).
# Prints the app or "not measured: <reason>"; exit 0 readable, 1 not.
focus_sample() {
	local src=$1 app rc
	app=$(front_app)
	rc=$?
	if [ $rc -ne 0 ]; then
		echo "$(iso_now) $src ${app#not measured: }" >>"$STATE_DIR/focus-unmeasured.log"
		echo "$app"
		return 1
	fi
	echo "$(iso_now) $src $app" >>"$STATE_DIR/focus-readable.log"
	if [ "$app" = "$NIGHTLY_NAME" ]; then
		echo "$(iso_now) $src" >>"$STATE_DIR/focus.log"
	fi
	echo "$app"
}

focus_seen() { [ -s "$STATE_DIR/focus.log" ]; }

focus_readable_count() {
	if [ -f "$STATE_DIR/focus-readable.log" ]; then
		wc -l <"$STATE_DIR/focus-readable.log" | tr -d ' '
	else
		echo 0
	fi
}

# focus_dirty_reason: the reason a pass that saw NIGHTLY frontmost is dirty.
focus_dirty_reason() {
	local src
	src=$(head -n 1 "$STATE_DIR/focus.log" | awk '{print $2}')
	case $src in
	sampler) echo "nightly frontmost (sampler)" ;;
	*) echo "nightly frontmost at $src" ;;
	esac
}

sampler_loop() {
	while [ -d "$STATE_DIR" ]; do
		focus_sample sampler >/dev/null
		sleep "$LIVEPASS_FOCUS_EVERY"
	done
}

# --- NIGHTLY (C11 nightly-running, nightly-pid) ---------------------------

# nightly_pid <asn>: the pid only when lsappinfo answers a "pid"=<digits> line
# (old) or a `pid = <digits>` token in its block (macOS 27); anything else is empty.
nightly_pid() {
	"$LIVEPASS_LSAPPINFO" info -only pid "$1" 2>/dev/null | sed -n -e 's/^"pid"=\([0-9][0-9]*\) *$/\1/p' -e 's/^ *pid = \([0-9][0-9]*\)\( .*\)\{0,1\}$/\1/p' | head -n 1
}

# nightly_running: 2 = cannot tell (lsappinfo lists no loginwindow), 1 = not
# running, 0 = running (prints its pid, empty when unreadable).
nightly_running() {
	local lw asn
	lw=$("$LIVEPASS_LSAPPINFO" find name=loginwindow 2>/dev/null)
	[ -n "$lw" ] || return 2
	asn=$("$LIVEPASS_LSAPPINFO" find "bundleid=$NIGHTLY_BUNDLE_ID" 2>/dev/null | head -n 1)
	[ -n "$asn" ] || return 1
	nightly_pid "$asn"
	return 0
}

# steps <seconds>: number of 0.2 s waits in that many seconds.
steps() { echo $(($1 * 5)); }

wait_nightly_pid() {
	local n i pid
	n=$(steps "$LIVEPASS_SOCKET_WAIT")
	i=0
	while [ "$i" -le "$n" ]; do
		pid=$(nightly_running)
		if [ -n "$pid" ]; then
			echo "$pid"
			return 0
		fi
		sleep 0.2
		i=$((i + 1))
	done
	return 1
}

nightly_version() {
	local plist="$LIVEPASS_NIGHTLY_APP/Contents/Info.plist"
	[ -r "$plist" ] || return 0
	grep -A1 '<key>CFBundleShortVersionString</key>' "$plist" |
		sed -n 's/.*<string>\(.*\)<\/string>.*/\1/p' | head -n 1
}

# --- D2 ledger (C10, C11 ledger) ------------------------------------------

LEDGER_REASON=""
LEDGER_COUNT=0

# ledger_count <version>: sets LEDGER_COUNT to the consecutive clean passes
# of that NIGHTLY version, read from the end; an unmeasured pass is skipped, a
# dirty one or another version stops the count. Sets LEDGER_REASON when it
# could not be measured (count 0). Not run in $( ): it sets globals.
ledger_count() {
	local cur=$1 lines n i line d v ver count
	LEDGER_REASON=""
	LEDGER_COUNT=0
	if [ -z "$cur" ]; then
		LEDGER_REASON="nightly version unreadable"
		return
	fi
	if [ ! -e "$LIVEPASS_LEDGER" ]; then
		return
	fi
	if [ ! -r "$LIVEPASS_LEDGER" ] || ! lines=$(cat "$LIVEPASS_LEDGER" 2>/dev/null); then
		LEDGER_REASON="ledger unreadable"
		return
	fi
	n=0
	local -a arr=()
	while IFS= read -r line; do
		[ -n "$line" ] || continue
		arr[n]=$line
		n=$((n + 1))
	done <<EOF
$lines
EOF
	count=0
	i=$((n - 1))
	while [ "$i" -ge 0 ]; do
		# shellcheck disable=SC2086
		set -- ${arr[i]}
		d=${1:-} v=${2:-} ver=${3:-}
		: "$d"
		[ "$ver" = "$cur" ] || break
		case $v in
		clean) count=$((count + 1)) ;;
		unmeasured) ;;
		*) break ;;
		esac
		i=$((i - 1))
	done
	LEDGER_COUNT=$count
}

# ledger_add <clean|dirty|unmeasured> <reason>
ledger_add() {
	local ver=${NIGHTLY_VERSION:-}
	[ -n "$ver" ] || ver=unknown
	mkdir -p "$(dirname "$LIVEPASS_LEDGER")" || {
		say "cannot write the D2 ledger $LIVEPASS_LEDGER"
		return 1
	}
	if ! { echo "$(iso_now) $1 $ver $2" >>"$LIVEPASS_LEDGER"; } 2>/dev/null; then
		say "cannot write the D2 ledger $LIVEPASS_LEDGER: pass not recorded"
		return 1
	fi
	echo "D2: pass recorded $1${2:+ ($2)}"
}

# --- processes (C11 stopped) ----------------------------------------------

alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }

# stop_pid <pid> <label>: SIGTERM, wait LIVEPASS_STOP_WAIT. 0 stopped (or
# already gone, said on stderr), 1 survived.
stop_pid() {
	local pid=$1 label=$2 n i
	if ! alive "$pid"; then
		say "$label already gone (pid $pid)"
		return 0
	fi
	kill -TERM "$pid" 2>/dev/null
	n=$(steps "$LIVEPASS_STOP_WAIT")
	i=0
	while [ "$i" -lt "$n" ]; do
		alive "$pid" || return 0
		sleep 0.2
		i=$((i + 1))
	done
	alive "$pid" && return 1
	return 0
}

# --- teardown (down, failed up) -------------------------------------------

# teardown <record: 1|0>: stop everything the pass started, close W and K,
# record the pass in the D2 ledger when asked, release mac-idle, clear the
# state. Returns 5 when a process survived or cannot be proven stopped.
teardown() {
	local record=$1 rc=0 verdict reason n
	if [ -n "$SAMPLER_PID" ]; then
		if alive "$SAMPLER_PID"; then
			kill -TERM "$SAMPLER_PID" 2>/dev/null
		fi
		state_set SAMPLER_PID ""
	fi
	if [ -n "$DAEMON_PID" ]; then
		daemon_stop_pid || rc=5
	fi
	if [ -n "$W" ]; then
		nightly_cli close-window --window "$W" >/dev/null 2>&1 || say "close-window W ($W) failed"
		state_set W ""
	fi
	if [ -n "$K" ]; then
		nightly_cli close-window --window "$K" >/dev/null 2>&1 || say "close-window K ($K) failed"
		state_set K ""
	fi
	if [ "$NIGHTLY_LAUNCHED" = 1 ]; then
		if [ -z "$NIGHTLY_PID" ]; then
			say "cmux NIGHTLY pid unknown — not proven stopped (state kept: $STATE_DIR)"
			return 5
		fi
		if ! stop_pid "$NIGHTLY_PID" "cmux NIGHTLY"; then
			state_set ISOLATION_BROKEN 1
			say "cmux NIGHTLY survived SIGTERM (pid $NIGHTLY_PID)"
			say "run: kill $NIGHTLY_PID, then scripts/live-pass.sh down"
			return 5
		fi
	fi
	if [ "$record" = 1 ]; then
		n=$(focus_readable_count)
		if focus_seen; then
			verdict=dirty reason=$(focus_dirty_reason)
		elif [ "$ISOLATION_BROKEN" = 1 ] || [ $rc -ne 0 ]; then
			verdict=dirty reason="isolation broken"
		elif [ "${STATUS_RUNS:-0}" -lt 1 ]; then
			verdict=unmeasured reason="no status"
		elif [ "$n" -lt "$LIVEPASS_FOCUS_MIN" ]; then
			verdict=unmeasured reason="readable focus samples $n/$LIVEPASS_FOCUS_MIN"
		else
			verdict=clean reason=""
		fi
		ledger_add "$verdict" "$reason"
	fi
	if [ "$MAC_FREE_ASKED" = 1 ]; then
		"$LIVEPASS_MAC_FREE" --release >/dev/null 2>&1 || say "mac-idle --release failed"
	fi
	rm -f "$CURRENT"
	rm -rf "$STATE_DIR"
	return $rc
}

# daemon_stop_pid: SIGTERM the branch daemon, SIGKILL when it ignores it
# (exit 5 at the end of down, isolation broken).
daemon_stop_pid() {
	local pid=$DAEMON_PID
	if stop_pid "$pid" "branch daemon"; then
		state_set DAEMON_PID ""
		return 0
	fi
	kill -KILL "$pid" 2>/dev/null
	state_set DAEMON_PID ""
	state_set ISOLATION_BROKEN 1
	say "branch daemon ignored SIGTERM, killed (pid $pid)"
	return 1
}

# --- up -------------------------------------------------------------------

cmd_up() {
	local want_mac_free=0 rc running=1 pid="" front front_rc count reason mode out wins key
	while [ $# -gt 0 ]; do
		case $1 in
		--mac-idle) want_mac_free=1 ;;
		*) usage >&2; exit 2 ;;
		esac
		shift
	done
	if ! { [ -d "$LIVEPASS_ROOT" ] && [ -r "$LIVEPASS_ROOT" ] && [ -w "$LIVEPASS_ROOT" ]; }; then
		die 2 "LIVEPASS_ROOT is not a readable directory ($LIVEPASS_ROOT)"
	fi
	if [ -L "$CURRENT" ]; then
		[ -d "$CURRENT" ] && die 2 "a live pass is already up ($(cd "$CURRENT" && pwd -P)); run: scripts/live-pass.sh down"
		rm -f "$CURRENT" # a dangling pointer: its pass is gone
	fi
	[ -d "$LIVEPASS_NIGHTLY_APP" ] || die 2 "cmux NIGHTLY not found ($LIVEPASS_NIGHTLY_APP)"
	local sock_probe="$LIVEPASS_ROOT/lp.XXXXXX/pigtaild.sock"
	if [ ${#sock_probe} -gt $SOCK_MAX ]; then
		die 2 "socket path too long: $sock_probe is ${#sock_probe} bytes, more than $SOCK_MAX (set LIVEPASS_ROOT shorter)"
	fi
	pid=$(nightly_running)
	rc=$?
	case $rc in
	0) running=0 ;;
	1) running=1 ;;
	*) die 2 "cannot tell whether cmux NIGHTLY runs (lsappinfo listed no loginwindow)" ;;
	esac
	front=$(front_app)
	front_rc=$?
	if [ $front_rc -eq 0 ] && [ "$front" = "$NIGHTLY_NAME" ]; then
		die 2 "cmux NIGHTLY is frontmost before up — the user is using it; retry once it is in the background"
	fi

	STATE_DIR=$(mktemp -d "$LIVEPASS_ROOT/lp.XXXXXX") || die 2 "cannot create the pass state in $LIVEPASS_ROOT"
	STATE_DIR=$(cd "$STATE_DIR" && pwd -P)
	: >"$STATE_DIR/focus.log"
	: >"$STATE_DIR/focus-readable.log"
	: >"$STATE_DIR/focus-unmeasured.log"
	state_defaults
	state_set STATUS_RUNS 0
	state_set ISOLATION_BROKEN 0
	state_set MAC_FREE_ASKED 0

	# D2 (the user): mac-idle at up while the ledger has < 3 clean passes of this
	# NIGHTLY version, or on --mac-idle. D3: the only plain mac-idle of the pass.
	state_set NIGHTLY_VERSION "$(nightly_version)"
	ledger_count "$NIGHTLY_VERSION"
	count=$LEDGER_COUNT
	reason=""
	[ -n "$LEDGER_REASON" ] && reason=" (not measured: $LEDGER_REASON)"
	[ "$count" -gt $D2_PASSES ] && count=$D2_PASSES
	if [ "$count" -lt $D2_PASSES ]; then mode=required; else mode=optional; fi
	echo "D2: clean passes $count/$D2_PASSES$reason, mac-idle $mode"
	echo "focus: readable focus quota $LIVEPASS_FOCUS_MIN (LIVEPASS_FOCUS_MIN)"
	if [ "$mode" = required ] || [ $want_mac_free = 1 ]; then
		if command -v "$LIVEPASS_MAC_FREE" >/dev/null 2>&1; then
			"$LIVEPASS_MAC_FREE"
			rc=$?
		else
			rc="not found: $LIVEPASS_MAC_FREE"
		fi
		if [ "$rc" != 0 ]; then
			rm -rf "$STATE_DIR"
			die 4 "the Mac is busy (mac-idle $rc)"
		fi
		state_set MAC_FREE_ASKED 1
	fi

	# The focus read before anything was created is the up sample.
	if [ $front_rc -eq 0 ]; then
		echo "$(iso_now) up $front" >>"$STATE_DIR/focus-readable.log"
	else
		echo "$(iso_now) up ${front#not measured: }" >>"$STATE_DIR/focus-unmeasured.log"
		echo "focus: not measured (${front#not measured: })"
	fi
	state_set FRONT_AT_UP "$([ $front_rc -eq 0 ] && echo "$front")"

	# the user's windows, read once, before NIGHTLY is launched (nothing to undo).
	if ! out=$(main_cli --json list-windows 2>/dev/null); then
		rm -rf "$STATE_DIR"
		die 3 "main cmux did not list the user' window"
	fi
	wins=$(window_ids "$out")
	if [ -z "$wins" ]; then
		rm -rf "$STATE_DIR"
		die 3 "main cmux did not list the user' window"
	fi
	key=$(key_window "$out")
	state_set MAIN_WINDOWS "$wins"
	state_set MAIN_KEY_WINDOW "$key"
	# the user's workspaces (C11 main-ids): without them no log check could tell
	# his ids from the pass's, so up refuses rather than run blind.
	if ! read_main_ids; then
		rm -rf "$STATE_DIR"
		die 3 "main cmux listed no workspace of the user"
	fi
	state_set LIVE_LOG_OFFSET "$(live_log_size)"

	if [ $running = 0 ]; then
		state_set NIGHTLY_LAUNCHED 0
		state_set NIGHTLY_PID "$pid"
		echo "cmux NIGHTLY already running${pid:+ (pid $pid)}: reused, down will not stop it"
	else
		"$LIVEPASS_OPEN" -g -a "$(basename "$LIVEPASS_NIGHTLY_APP" .app)"
		if ! pid=$(wait_nightly_pid); then
			rm -rf "$STATE_DIR"
			die 3 "cmux NIGHTLY launched but its pid was not read within $LIVEPASS_SOCKET_WAIT s — not proven stopped, quit it by hand"
		fi
		state_set NIGHTLY_PID "$pid"
		state_set NIGHTLY_LAUNCHED 1
	fi
	ln -s "$STATE_DIR" "$CURRENT" || {
		teardown 0
		die 2 "cannot create $CURRENT"
	}

	if ! wait_nightly_socket; then
		teardown 0
		die 3 "nightly socket did not answer within $LIVEPASS_SOCKET_WAIT s ($LIVEPASS_NIGHTLY_SOCKET)"
	fi
	# The windows NIGHTLY opened by itself are pass windows too.
	state_set NIGHTLY_WINDOWS "$(window_ids "$(nightly_cli --json list-windows 2>/dev/null)")"
	local w k
	w=$(new_window) || { teardown 0; die 3 "nightly new-window failed (W)"; }
	state_set W "$w"
	k=$(new_window) || { teardown 0; die 3 "nightly new-window failed (K)"; }
	state_set K "$k"
	refresh_pass_ids

	if ! front=$(focus_sample up); then
		echo "focus: not measured (${front#not measured: })"
	elif [ "$front" = "$NIGHTLY_NAME" ]; then
		say "cmux NIGHTLY took the focus at up (state kept: $STATE_DIR)"
		die 6 "run: scripts/live-pass.sh down"
	fi

	(sampler_loop) >/dev/null 2>&1 </dev/null &
	state_set SAMPLER_PID "$!"

	echo "cmux NIGHTLY ${NIGHTLY_VERSION:-(version unreadable)}${NIGHTLY_PID:+ pid $NIGHTLY_PID}"
	echo "W=$W"
	echo "K=$K"
	echo "state: $STATE_DIR"
}

wait_nightly_socket() {
	local n i
	n=$(steps "$LIVEPASS_SOCKET_WAIT")
	i=0
	while [ "$i" -le "$n" ]; do
		nightly_cli list-windows >/dev/null 2>&1 && return 0
		sleep 0.2
		i=$((i + 1))
	done
	return 1
}

# new_window prints the UUID of a new NIGHTLY window.
new_window() {
	local out id
	out=$(nightly_cli new-window 2>/dev/null) || return 1
	id=$(printf '%s\n' "$out" | grep -oE '[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}' | head -n 1)
	[ -n "$id" ] || return 1
	echo "$id"
}

# window_ids <list-windows JSON>: the window ids, space separated.
window_ids() {
	printf '%s' "$1" | tr -d '\n' | tr '{' '\n' |
		sed -n 's/.*"id" *: *"\([0-9A-Fa-f-]\{36\}\)".*/\1/p' | tr '\n' ' ' | sed 's/ *$//'
}

# key_window <list-windows JSON>: the id of the window whose "key" is true.
key_window() {
	printf '%s' "$1" | tr -d '\n' | tr '{' '\n' | grep -E '"key" *: *true' |
		sed -n 's/.*"id" *: *"\([0-9A-Fa-f-]\{36\}\)".*/\1/p' | head -n 1
}

live_log_size() {
	[ -r "$LIVEPASS_LIVE_LOG" ] || return 0
	wc -c <"$LIVEPASS_LIVE_LOG" | tr -d ' '
}

# --- down -----------------------------------------------------------------

cmd_down() {
	local rc front
	load_state || die 2 "no live pass is up"
	if front=$(focus_sample down) && [ "$front" = "$NIGHTLY_NAME" ]; then
		say "cmux NIGHTLY is frontmost at down"
	fi
	teardown 1
	rc=$?
	exit $rc
}

# --- status (C5, C11) -------------------------------------------------------

STATUS_FAILS=0
STATUS_FAILED=""
STATUS_UNMEASURED=""
STATUS_OK=""

# check_line <id> <ok|FAIL|nm> [reason]: the one way a status line is
# written. A check calls it with ok last, once its proof of reading (C11) is
# established; FAIL and nm (not measured) say why.
check_line() {
	case $2 in
	ok)
		echo "$1: ok"
		STATUS_OK="$STATUS_OK $1"
		;;
	FAIL)
		echo "$1: FAIL $3"
		say "$3"
		STATUS_FAILS=$((STATUS_FAILS + 1))
		STATUS_FAILED="$STATUS_FAILED $1"
		;;
	*)
		echo "$1: not measured: $3"
		STATUS_UNMEASURED="$STATUS_UNMEASURED $1"
		;;
	esac
}

# list_ws_ids <list-workspaces JSON>: the workspace ids, one per line ("id"
# appears once per workspace; cloud_vm_id and the like never match).
list_ws_ids() {
	printf '%s' "$1" | grep -oE '"id" *: *"[0-9A-Fa-f-]{36}"' | grep -oE '[0-9A-Fa-f-]{36}'
}

# add_ids <list> <ids…>: the union, space separated.
add_ids() {
	local list=$1 id
	shift
	for id in "$@"; do
		case " $list " in *" $id "*) ;; *) list="${list:+$list }$id" ;; esac
	done
	echo "$list"
}

# refresh_pass_ids: PASS_IDS = W, K and the workspaces of W and K (union of
# every reading: up, after each tab, each status); PASS_WS_SEEN_<W|K>=1 once a
# reading returned at least one workspace of that window (proof of pass-ids).
refresh_pass_ids() {
	local which win out ids ws
	# shellcheck disable=SC2086
	ids=$(add_ids "$PASS_IDS" $W $K $NIGHTLY_WINDOWS)
	for which in W K $NIGHTLY_WINDOWS; do
		case $which in
		W | K) eval "win=\$$which" ;;
		*) win=$which ;;
		esac
		[ -n "$win" ] || continue
		out=$(nightly_cli --json list-workspaces --window "$win" 2>/dev/null) || continue
		ws=$(list_ws_ids "$out")
		if [ -n "$ws" ]; then
			# shellcheck disable=SC2086
			ids=$(add_ids "$ids" $ws)
			case $which in W | K) state_set "PASS_WS_SEEN_$which" 1 ;; esac
		fi
	done
	state_set PASS_IDS "$ids"
}

# read_main_ids: MAIN_IDS = the user's windows and their workspaces (C11
# main-ids). 1 when the main CLI fails or lists no workspace at all.
read_main_ids() {
	local win out ids=$MAIN_WINDOWS ws
	for win in $MAIN_WINDOWS; do
		out=$(main_cli --json list-workspaces --window "$win" 2>/dev/null) || return 1
		ws=$(list_ws_ids "$out")
		# shellcheck disable=SC2086
		[ -n "$ws" ] && ids=$(add_ids "$ids" $ws)
	done
	[ "$ids" != "$MAIN_WINDOWS" ] || return 1
	state_set MAIN_IDS "$ids"
}

# first_id_in <file|-> <ids…>: the first id the text names (case
# insensitive, fixed strings), "" when none.
first_id_in() {
	local text=$1 id
	shift
	for id in "$@"; do
		if printf '%s' "$text" | grep -i -F -q -- "$id"; then
			echo "$id"
			return 0
		fi
	done
	return 1
}

check_pass_ids() {
	refresh_pass_ids
	local missing=""
	[ "${PASS_WS_SEEN_W:-0}" = 1 ] || missing="W"
	[ "${PASS_WS_SEEN_K:-0}" = 1 ] || missing="${missing:+$missing and }K"
	if [ -n "$missing" ]; then
		check_line pass-ids nm "nightly listed no workspace of $missing"
		return
	fi
	check_line pass-ids ok
}

check_live_log() {
	local size new id
	if [ -z "$LIVE_LOG_OFFSET" ] || [ ! -r "$LIVEPASS_LIVE_LOG" ]; then
		check_line live-log nm "live pigtaild log unreadable ($LIVEPASS_LIVE_LOG)"
		return
	fi
	size=$(wc -c <"$LIVEPASS_LIVE_LOG" | tr -d ' ')
	if [ "$size" -lt "$LIVE_LOG_OFFSET" ]; then
		check_line live-log nm "live pigtaild log shrank since up (rotated?)"
		return
	fi
	if [ "$size" -eq "$LIVE_LOG_OFFSET" ]; then
		check_line live-log nm "live pigtaild log did not move since up"
		return
	fi
	new=$(tail -c +$((LIVE_LOG_OFFSET + 1)) "$LIVEPASS_LIVE_LOG")
	# A pass id named is a finding whatever the proofs: never an absence.
	# shellcheck disable=SC2086
	if id=$(first_id_in "$new" $PASS_IDS); then
		check_line live-log FAIL "live pigtaild wrote about a pass workspace or window ($id)"
		return
	fi
	if ! printf '%s' "$STATUS_OK" | grep -q -w pass-ids; then
		check_line live-log nm "live pigtaild log not searched: pass ids not measured"
		return
	fi
	# shellcheck disable=SC2086
	if ! first_id_in "$new" $MAIN_IDS >/dev/null; then
		check_line live-log nm "live pigtaild log names no workspace of the user since up"
		return
	fi
	check_line live-log ok
}

MAIN_LISTING=""
MAIN_LISTING_OK=0

check_main_listing() {
	local ids win found=0
	if ! MAIN_LISTING=$(main_cli --json list-windows 2>/dev/null); then
		check_line main-listing FAIL "main cmux listing failed"
		return
	fi
	ids=$(window_ids "$MAIN_LISTING")
	for win in $MAIN_WINDOWS; do
		case " $ids " in *" $win "*) found=1 ;; esac
	done
	if [ $found = 0 ]; then
		check_line main-listing FAIL "main cmux listing has no window of the user"
		return
	fi
	MAIN_LISTING_OK=1
	for win in $W $K; do
		case " $ids " in
		*" $win "*)
			check_line main-listing FAIL "main cmux lists a pass window ($win)"
			return
			;;
		esac
	done
	check_line main-listing ok
}

AIM_OK=0

check_branch_aim() {
	local line want
	if [ -z "$DAEMON_PID" ]; then
		check_line branch-aim nm "branch daemon not started"
		return
	fi
	want="cmux: every call aimed at socket $LIVEPASS_NIGHTLY_SOCKET (-cmux-socket)"
	if [ ! -r "$STATE_DIR/pigtaild.log" ]; then
		check_line branch-aim FAIL "branch daemon log unreadable ($STATE_DIR/pigtaild.log)"
		return
	fi
	while IFS= read -r line || [ -n "$line" ]; do
		case $line in
		*"$want")
			AIM_OK=1
			check_line branch-aim ok
			return
			;;
		esac
	done <"$STATE_DIR/pigtaild.log"
	check_line branch-aim FAIL "branch daemon log has no \"$want\" line"
}

check_branch_log() {
	local text id
	if [ -z "$DAEMON_PID" ]; then
		check_line branch-log nm "branch daemon not started"
		return
	fi
	text=$(cat "$STATE_DIR/pigtaild.log" 2>/dev/null)
	# shellcheck disable=SC2086
	if id=$(first_id_in "$text" $MAIN_IDS); then
		check_line branch-log FAIL "branch daemon wrote about a main workspace or window ($id)"
		return
	fi
	if [ $AIM_OK != 1 ]; then
		check_line branch-log nm "branch daemon not proven aimed at NIGHTLY (branch-aim)"
		return
	fi
	# shellcheck disable=SC2086
	if ! first_id_in "$text" $PASS_IDS >/dev/null; then
		check_line branch-log nm "branch pigtaild log names no pass workspace"
		return
	fi
	check_line branch-log ok
}

FRONT_NOW=""
FRONT_NOW_OK=0

check_focus_now() {
	if ! FRONT_NOW=$(focus_sample status); then
		check_line focus-now nm "focus (${FRONT_NOW#not measured: })"
		return
	fi
	FRONT_NOW_OK=1
	if [ "$FRONT_NOW" = "$NIGHTLY_NAME" ]; then
		check_line focus-now FAIL "nightly is frontmost now (status)"
		return
	fi
	check_line focus-now ok
}

check_sampler() {
	local first
	if focus_seen; then
		first=$(head -n 1 "$STATE_DIR/focus.log")
		# shellcheck disable=SC2086
		set -- $first
		check_line sampler FAIL "nightly was frontmost at $1 ($2)"
		return
	fi
	if ! alive "$SAMPLER_PID"; then
		check_line sampler nm "sampler (dead)"
		return
	fi
	if ! awk '$2 == "sampler"' "$STATE_DIR/focus-readable.log" | grep -q .; then
		check_line sampler nm "sampler (no readable sample)"
		return
	fi
	check_line sampler ok
}

MAIN_CMUX_NAME=cmux

check_main_key() {
	local objs nkey
	if [ $FRONT_NOW_OK != 1 ]; then
		check_line main-key nm "main key (focus unreadable)"
		return
	fi
	if [ "$FRONT_NOW" != "$MAIN_CMUX_NAME" ]; then
		check_line main-key nm "main key (cmux not frontmost)"
		return
	fi
	if [ $MAIN_LISTING_OK != 1 ]; then
		check_line main-key nm "main key (listing unreadable)"
		return
	fi
	objs=$(printf '%s' "$MAIN_LISTING" | tr -d '\n' | tr '{' '\n' | grep -E '"id" *:')
	nkey=$(printf '%s\n' "$objs" | grep -cE '"key" *: *(true|false)')
	if [ -z "$objs" ] || [ "$nkey" -ne "$(printf '%s\n' "$objs" | grep -c .)" ]; then
		check_line main-key nm "main key (listing has no key field)"
		return
	fi
	if ! printf '%s\n' "$objs" | grep -qE '"key" *: *true'; then
		check_line main-key FAIL "no main cmux window is key while cmux is frontmost"
		return
	fi
	check_line main-key ok
}

cmd_status() {
	local mode
	load_state || die 2 "no live pass is up"
	state_set STATUS_RUNS $((STATUS_RUNS + 1))
	ledger_count "$NIGHTLY_VERSION"
	if [ "$LEDGER_COUNT" -lt $D2_PASSES ]; then mode=required; else mode=optional; fi
	echo "cmux NIGHTLY ${NIGHTLY_VERSION:-(version unreadable)}${NIGHTLY_PID:+ pid $NIGHTLY_PID}, W=$W, K=$K"
	echo "branch daemon $([ -n "$DAEMON_PID" ] && echo "pid $DAEMON_PID" || echo "not started"), log $STATE_DIR/pigtaild.log"
	echo "live pigtaild log $LIVEPASS_LIVE_LOG; focus logs $STATE_DIR/focus*.log"
	echo "D2: clean passes $([ "$LEDGER_COUNT" -gt $D2_PASSES ] && echo $D2_PASSES || echo "$LEDGER_COUNT")/$D2_PASSES${LEDGER_REASON:+ (not measured: $LEDGER_REASON)}, mac-idle $mode"
	check_pass_ids
	check_live_log
	check_main_listing
	check_branch_aim
	check_branch_log
	check_focus_now
	check_sampler
	check_main_key
	if [ $STATUS_FAILS -gt 0 ]; then
		state_set ISOLATION_BROKEN 1
		echo "isolation: broken (FAIL:$STATUS_FAILED)"
		exit 7
	fi
	if [ -z "$STATUS_UNMEASURED" ]; then
		echo "isolation: proven"
	else
		echo "isolation: partial (not measured:$STATUS_UNMEASURED)"
	fi
	exit 0
}

# --- branch daemon (C3) ----------------------------------------------------

REPO_ROOT=$(cd "$(dirname "$0")/.." 2>/dev/null && pwd -P)

cmd_daemon() {
	local sub=${1:-}
	[ $# -gt 0 ] && shift
	case $sub in
	start) daemon_start "$@" ;;
	stop) daemon_stop ;;
	*)
		say "unknown command 'daemon${sub:+ $sub}'"
		usage >&2
		exit 2
		;;
	esac
}

daemon_start() {
	local pigtaild="$REPO_ROOT/bin/pigtaild" out rc pid n i
	local -a extra=() args=()
	while [ $# -gt 0 ]; do
		case $1 in
		--pigtaild)
			[ $# -ge 2 ] || { usage >&2; exit 2; }
			pigtaild=$2
			shift 2
			;;
		--)
			shift
			extra=("$@")
			break
			;;
		*) usage >&2; exit 2 ;;
		esac
	done
	load_state || die 2 "no live pass is up"
	if [ -n "$DAEMON_PID" ] && alive "$DAEMON_PID"; then
		die 2 "branch daemon already running (pid $DAEMON_PID)"
	fi
	[ -x "$pigtaild" ] || die 2 "pigtaild not found or not executable ($pigtaild); run make build"
	args=(-socket "$STATE_DIR/pigtaild.sock" -db "$STATE_DIR/pigtail.db" -http= -outputs-http=
		-cmux-socket "$LIVEPASS_NIGHTLY_SOCKET" -cmux-sidebar -cmux-folders
		-cmux-sidebar-window "$W" -cmux-sidebar-window "$K"
		-refresh-main=false -watchdog=false -ram-budget=false -cmux-check-after-relaunch=off
		-orphan-reaper=off -tmp-sweep=off
		${extra[@]+"${extra[@]}"})
	# -check-args: the real start's flags and validations, nothing started (C11
	# check-args). A pigtaild without -cmux-socket refuses here, before it could
	# drive the main cmux; an exit 0 without its ok line is no acceptance.
	out=$(cmux_clean "$pigtaild" -check-args "${args[@]}" 2>"$STATE_DIR/check-args.err")
	rc=$?
	if [ $rc -ne 0 ]; then
		die 2 "pigtaild refused the pass flags (-check-args): $(head -n 1 "$STATE_DIR/check-args.err" | sed 's/^pigtaild -check-args: //') — a pigtaild without -cmux-socket would drive the main cmux"
	fi
	case $out in
	*"pigtaild -check-args: ok"*) ;;
	*) die 2 "pigtaild -check-args did not confirm the pass flags" ;;
	esac
	mkdir -p "$STATE_DIR/xdg-state"
	rm -f "$STATE_DIR/pigtaild.sock"
	(
		strip_cmux
		unset PIGTAIL_SOCKET
		export CMUX_BUNDLED_CLI_PATH="$LIVEPASS_NIGHTLY_CLI"
		export XDG_STATE_HOME="$STATE_DIR/xdg-state"
		exec "$pigtaild" "${args[@]}"
	) >>"$STATE_DIR/pigtaild.log" 2>&1 </dev/null &
	pid=$!
	state_set DAEMON_PID "$pid"
	n=$(steps "$LIVEPASS_SOCKET_WAIT")
	i=0
	while [ "$i" -le "$n" ]; do
		if ! alive "$pid"; then
			state_set DAEMON_PID ""
			say "branch daemon died at birth (pid $pid); last lines of $STATE_DIR/pigtaild.log:"
			tail -n 20 "$STATE_DIR/pigtaild.log" >&2
			exit 3
		fi
		[ -e "$STATE_DIR/pigtaild.sock" ] && break
		sleep 0.2
		i=$((i + 1))
	done
	if [ ! -e "$STATE_DIR/pigtaild.sock" ]; then
		daemon_stop_pid >/dev/null 2>&1
		say "last lines of $STATE_DIR/pigtaild.log:"
		tail -n 20 "$STATE_DIR/pigtaild.log" >&2
		die 3 "branch daemon did not create its socket within $LIVEPASS_SOCKET_WAIT s"
	fi
	echo "branch daemon pid $pid, socket $STATE_DIR/pigtaild.sock, log $STATE_DIR/pigtaild.log"
}

daemon_stop() {
	load_state || die 2 "no live pass is up"
	[ -n "$DAEMON_PID" ] || die 2 "no branch daemon started"
	daemon_stop_pid || exit 5
	echo "branch daemon stopped"
}

# --- env, tab (C4) ----------------------------------------------------------

# pass_env: the NAME=value pairs of a pass session, one per line.
pass_env() {
	printf '%s\n' "PIGTAIL_SOCKET=$STATE_DIR/pigtaild.sock" \
		"CMUX_SOCKET_PATH=$LIVEPASS_NIGHTLY_SOCKET" \
		"CMUX_BUNDLED_CLI_PATH=$LIVEPASS_NIGHTLY_CLI" \
		"LIVEPASS_W=$W" "LIVEPASS_K=$K"
}

cmd_env() {
	local kv
	load_state || die 2 "no live pass is up"
	while IFS= read -r kv; do
		printf 'export %s=%q\n' "${kv%%=*}" "${kv#*=}"
	done <<EOF
$(pass_env)
EOF
}

cmd_tab() {
	local which=${1:-} win claude=0 n kv
	local -a args=()
	load_state || die 2 "no live pass is up"
	case $which in
	W) win=$W ;;
	K) win=$K ;;
	*) die 2 "tab takes W or K, not '$which'" ;;
	esac
	shift
	while [ $# -gt 0 ]; do
		case $1 in
		--claude) claude=1 ;;
		--claude-no-skills) claude=2 ;;
		*) usage >&2; exit 2 ;;
		esac
		shift
	done
	n=$(($(cat "$STATE_DIR/tab-count" 2>/dev/null || echo 0) + 1))
	echo "$n" >"$STATE_DIR/tab-count"
	args=(new-workspace --window "$win" --name "lp-$n" --cwd "$PWD" --focus false)
	while IFS= read -r kv; do
		args+=(--env "$kv")
	done <<EOF
$(pass_env)
EOF
	if [ -n "${CLAUDE_CONFIG_DIR:-}" ]; then
		args+=(--env "CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR")
	fi
	[ $claude = 1 ] && args+=(--command claude)
	[ $claude = 2 ] && args+=(--command "claude --disable-slash-commands")
	nightly_cli "${args[@]}" || die 3 "nightly new-workspace failed in $which ($win)"
	refresh_pass_ids
}

# --- front, help ----------------------------------------------------------

cmd_front() {
	front_app
	exit $?
}

usage() {
	cat <<EOF
usage: scripts/live-pass.sh <command>

  up [--mac-idle]   launch cmux NIGHTLY in the background (open -g), create the
                    throwaway windows W and K, start the focus sampler
  daemon start [--pigtaild <path>] [-- <flags>]
                    start the branch pigtaild (default bin/pigtaild of this repo)
                    aimed at NIGHTLY's W and K only, on a throwaway socket and db
  daemon stop       stop it (SIGTERM, SIGKILL after LIVEPASS_STOP_WAIT s)
  env               print the exports of a pass session; eval them in the same
                    call as the command that uses them, never in a main cmux pane
  tab <W|K> [--claude|--claude-no-skills]
                    open a tab in W or K without focus, with the pass variables
                    and the caller's CLAUDE_CONFIG_DIR (--claude runs claude;
                    --claude-no-skills runs it with --disable-slash-commands, so
                    a slash command typed by the branch daemon runs no skill)
  status            prove the isolation, one line per check (exit 7 on a FAIL)
  front             print the frontmost app, or "not measured: <reason>" (exit 1)
  down              stop everything the pass started, close W and K, record the
                    pass in the D2 ledger ($LIVEPASS_LEDGER)
  help              this text

D2/D3 (the user): up asks mac-idle, at the start only, until the ledger holds
$D2_PASSES clean passes in a row of the current NIGHTLY version; never again during
the pass. A pass is clean with a status and $LIVEPASS_FOCUS_MIN readable focus samples
(readable focus quota, LIVEPASS_FOCUS_MIN) without NIGHTLY frontmost.
Exit codes: 2 precondition, 3 wait failed, 4 Mac busy, 5 a process survived,
6 NIGHTLY took the focus, 7 isolation broken.
EOF
}

main() {
	local cmd=${1:-help}
	[ $# -gt 0 ] && shift
	case $cmd in
	up) cmd_up "$@" ;;
	daemon) cmd_daemon "$@" ;;
	env) cmd_env "$@" ;;
	tab) cmd_tab "$@" ;;
	down) cmd_down "$@" ;;
	status) cmd_status "$@" ;;
	front) cmd_front "$@" ;;
	help | -h | --help) usage ;;
	*)
		say "unknown command '$cmd'"
		usage >&2
		exit 2
		;;
	esac
}

main "$@"
