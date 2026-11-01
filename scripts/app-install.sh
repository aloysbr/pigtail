#!/usr/bin/env bash
# app-install.sh <staged.app> <dest.app> — swaps a freshly built Pigtail.app
# in and retires the two apps it replaces (PigtailBar.app, Pigtail Cockpit.app).
#
# Called by `make app` with PIGTAIL_APPS_DIR (where the old apps live) and
# PIGTAIL_TRASH_DIR (where they go) written on its command line; both are
# required, never defaulted. Every directory is proven writable before any
# process is stopped; a process that survives SIGTERM aborts the install
# (never SIGKILL, never an rm of a live app), and on any failure what was
# stopped is reopened. A running app is relaunched as the menu bar alone.
# Seams for the tests: PS_BIN, KILL_BIN, OPEN_BIN, PIGTAIL_INSTALL_WAIT.
# Bash 3.2 compatible (macOS /bin/bash).
set -euo pipefail

staged="${1:?staged}"
dest="${2:?dest}"
: "${PIGTAIL_APPS_DIR:?}"
: "${PIGTAIL_TRASH_DIR:?}"
PS_BIN="${PS_BIN:-ps}"
KILL_BIN="${KILL_BIN:-kill}"
OPEN_BIN="${OPEN_BIN:-open}"
wait_s="${PIGTAIL_INSTALL_WAIT:-5}"

legacy_bar="$PIGTAIL_APPS_DIR/PigtailBar.app"
legacy_window="$PIGTAIL_APPS_DIR/Pigtail Cockpit.app"

# The pids whose command line starts with <bundle>/Contents/MacOS/ (exact
# path prefix, spaces included; never pgrep -f).
pids_of() {
	local bundle=$1 pid args
	"$PS_BIN" -Awwo pid=,args= | while read -r pid args; do
		case "$args" in
		"$bundle/Contents/MacOS/"*) echo "$pid" ;;
		esac
	done
}

# A real write, not `test -w` (which does not see TCC).
writable() {
	local probe="$1/.app-install-probe.$$"
	[ -d "$1" ] && (: >"$probe") 2>/dev/null && rm -f "$probe"
}

# Polls up to wait_s seconds for <bundle> to have (want=1) or lose (want=0) a process.
await() {
	local bundle=$1 want=$2 i=0 n=$((wait_s * 5))
	while :; do
		if [ -n "$(pids_of "$bundle")" ]; then [ "$want" = 1 ] && return 0; else [ "$want" = 0 ] && return 0; fi
		[ "$i" -ge "$n" ] && return 1
		sleep 0.2
		i=$((i + 1))
	done
}

# 1. Preflight, before anything is stopped.
if [ ! -d "$staged" ]; then
	echo "app-install: $staged not found, nothing stopped" >&2
	exit 1
fi
# The destination's parent and the trash may be absent (a custom APP_DEST, a
# test trash): create them. /Applications is never created, only checked.
for dir in "$(dirname "$dest")" "$PIGTAIL_TRASH_DIR"; do
	[ -d "$dir" ] || [ "$dir" = /Applications ] || mkdir -p "$dir" 2>/dev/null || true
done
for dir in "$(dirname "$dest")" "$PIGTAIL_APPS_DIR" "$PIGTAIL_TRASH_DIR"; do
	if ! writable "$dir"; then
		echo "app-install: cannot write to $dir, nothing stopped" >&2
		exit 1
	fi
done

was_running=0
replaced=0
stopped=()

# 4. On failure, reopen what was stopped: the menu bar never stays dead.
on_exit() {
	local code=$? b
	[ "$code" = 0 ] && return 0
	if [ "$replaced" = 1 ]; then
		if [ "$was_running" = 1 ]; then
			"$OPEN_BIN" -g -a "$dest" --args --background || true
			echo "app-install: relaunched $dest after failure" >&2
		fi
	else
		for b in ${stopped[@]+"${stopped[@]}"}; do
			[ -d "$b" ] || continue
			if [ "$b" = "$dest" ]; then
				"$OPEN_BIN" -g -a "$b" --args --background || true
			else
				"$OPEN_BIN" -g -a "$b" || true
			fi
			echo "app-install: relaunched $b after failure" >&2
		done
	fi
	exit "$code"
}
trap on_exit EXIT

# 2-3. Stop the live targets.
for b in "$dest" "$legacy_bar" "$legacy_window"; do
	pids=$(pids_of "$b")
	[ -n "$pids" ] || continue
	was_running=1
	stopped+=("$b")
	for pid in $pids; do
		"$KILL_BIN" -TERM "$pid" || true
	done
done
for b in ${stopped[@]+"${stopped[@]}"}; do
	if ! await "$b" 0; then
		# Read every pid first, then take the first line: no `head` at the end of a
		# pipe (SIGPIPE on the producer would trip pipefail + set -e).
		left=$(pids_of "$b")
		pid=${left%%$'\n'*}
		echo "app-install: $b still running (pid $pid), nothing replaced" >&2
		exit 1
	fi
done

# 5. Swap: the old Pigtail.app is renamed then removed, never trashed.
old="$dest.old.$$"
[ -e "$dest" ] && mv "$dest" "$old"
if ! mv "$staged" "$dest"; then
	[ -e "$old" ] && mv "$old" "$dest"
	exit 1
fi
replaced=1
rm -rf "$old"
echo "replaced"

# 6. The two retired apps, and only them, go to the trash.
stamp=$(date +%Y%m%d-%H%M%S)
for b in "$legacy_bar" "$legacy_window"; do
	[ -e "$b" ] || continue
	target="$PIGTAIL_TRASH_DIR/$(basename "$b" .app) $stamp.app"
	mv "$b" "$target"
	echo "trashed $target"
done

# 7. Relaunch as the menu bar alone, even if a window was open.
if [ "$was_running" = 1 ]; then
	"$OPEN_BIN" -g -a "$dest" --args --background
	if ! await "$dest" 1; then
		echo "app-install: $dest did not start" >&2
		exit 1
	fi
	echo "relaunched --background"
fi
