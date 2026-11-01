#!/bin/sh
# flake.sh — run a command N times, P at a time, and count the failures: the
# reproduction of a flaky test under load.
#
# usage: sh scripts/flake.sh [-p P] [-n N] [-C dir] -- <cmd…>
#   -p  runs at a time (default 36: 2 × 18 cores)
#   -n  runs in all (default 300)
#   -C  directory each run starts in (default: the current one)
#
# It loads the Mac on purpose: always run it under the admission queue,
#   pigtail admit --class go-test -- sh scripts/flake.sh …
#
# stdout: `flake: runs=<N> fail=<k>`; when k ≥ 1, also `flake: logs=<dir>`,
# a directory holding the output of each failed run (one file per failure;
# the outputs of green runs are dropped). Exit 0 when no run failed, 1 when
# one did, 2 on a usage error.
#
# For a Go test, build the binary first (`go test -race -c -o …`) so every
# run tests the same code, then pass `-test.run '^TestX$' -test.count=1`.

usage() {
	echo "usage: sh scripts/flake.sh [-p P] [-n N] [-C dir] -- <cmd…>" >&2
	exit 2
}

p=36
n=300
dir=.
while [ $# -gt 0 ]; do
	case $1 in
	-p) [ $# -ge 2 ] || usage; p=$2; shift 2 ;;
	-n) [ $# -ge 2 ] || usage; n=$2; shift 2 ;;
	-C) [ $# -ge 2 ] || usage; dir=$2; shift 2 ;;
	--) shift; break ;;
	*) usage ;;
	esac
done
[ $# -gt 0 ] || usage
case $p$n in *[!0-9]*) usage ;; esac
[ "$p" -ge 1 ] && [ "$n" -ge 1 ] || usage
[ -d "$dir" ] || { echo "flake: no directory $dir" >&2; exit 2; }

logs=$(mktemp -d "${TMPDIR:-/tmp}/flake.XXXXXX") || exit 2

# Each run writes its output to run-<i>.log and drops it when green; the
# wrapper always exits 0 so xargs runs every one of the N.
seq "$n" | xargs -P "$p" -I{} sh -c '
	i=$1 logs=$2 dir=$3
	shift 3
	out="$logs/run-$i.log"
	if (cd "$dir" && exec "$@") >"$out" 2>&1; then
		rm -f "$out"
	fi
	exit 0
' flake-run {} "$logs" "$dir" "$@"

fail=$(find "$logs" -type f -name 'run-*.log' | wc -l | tr -d ' ')
echo "flake: runs=$n fail=$fail"
if [ "$fail" -gt 0 ]; then
	echo "flake: logs=$logs"
	exit 1
fi
rm -rf "$logs"
exit 0
