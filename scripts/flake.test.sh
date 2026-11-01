#!/bin/sh
# flake.test.sh — prove scripts/flake.sh counts runs and failures, keeps the
# output of each failed run, and runs the command in the -C directory.
cd "$(dirname "$0")/.." || exit 1
script=$(pwd)/scripts/flake.sh
fails=0

fail() {
	echo "FAIL $1"
	fails=$((fails + 1))
}

[ -f "$script" ] || { echo "FAIL $script missing"; exit 1; }

# all green → runs=8 fail=0, exit 0
out=$(sh "$script" -p 4 -n 8 -- true 2>&1)
status=$?
[ "$status" = 0 ] || fail "green: exit $status"
printf '%s\n' "$out" | grep -qx 'flake: runs=8 fail=0' || fail "green: stdout $out"

# all red → fail=8, exit 1, logs=<dir> with one output per failure
out=$(sh "$script" -p 4 -n 8 -- sh -c 'echo boom; exit 3' 2>&1)
status=$?
[ "$status" = 1 ] || fail "red: exit $status"
printf '%s\n' "$out" | grep -qx 'flake: runs=8 fail=8' || fail "red: stdout $out"
logs=$(printf '%s\n' "$out" | sed -n 's/^flake: logs=//p')
if [ -z "$logs" ] || [ ! -d "$logs" ]; then
	fail "red: no logs dir in $out"
else
	n=$(find "$logs" -type f | wc -l | tr -d ' ')
	[ "$n" = 8 ] || fail "red: $n log files, want 8"
	grep -q boom "$logs"/* 2>/dev/null || fail "red: logs do not hold the output"
	rm -rf "$logs"
fi

# `false` itself → fail=8, exit 1
out=$(sh "$script" -p 4 -n 8 -- false 2>&1)
status=$?
[ "$status" = 1 ] || fail "false: exit $status"
printf '%s\n' "$out" | grep -qx 'flake: runs=8 fail=8' || fail "false: stdout $out"
logs=$(printf '%s\n' "$out" | sed -n 's/^flake: logs=//p')
[ -n "$logs" ] && rm -rf "$logs"

# -C <dir> runs the command in that directory
d=$(mktemp -d) || exit 1
: >"$d/marker"
out=$(sh "$script" -p 2 -n 4 -C "$d" -- sh -c 'test -f marker' 2>&1)
status=$?
[ "$status" = 0 ] || fail "-C: exit $status ($out)"
printf '%s\n' "$out" | grep -qx 'flake: runs=4 fail=0' || fail "-C: stdout $out"
rm -rf "$d"

# no command → usage, exit 2
sh "$script" -p 2 -n 2 >/dev/null 2>&1
status=$?
[ "$status" = 2 ] || fail "no command: exit $status"

if [ "$fails" -gt 0 ]; then
	echo "flake.test.sh: $fails failure(s)"
	exit 1
fi
echo "flake.test.sh: ok"
