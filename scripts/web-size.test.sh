#!/bin/sh
# web-size.test.sh — prove the bundle budget of scripts/web-size.sh on
# throwaway dists: a small one passes, one inflated by 600 KB of
# incompressible bytes fails, an empty one fails (nothing measured is
# not a pass).
cd "$(dirname "$0")/.." || exit 1
fail=0
tmp=$(mktemp -d) || exit 1
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/small/assets" "$tmp/big/assets" "$tmp/empty/assets"
printf 'console.log("web")\n' >"$tmp/small/assets/index.js"
printf 'body{color:red}\n' >"$tmp/small/assets/index.css"
cp "$tmp/small/assets/index.css" "$tmp/big/assets/index.css"
head -c 614400 /dev/urandom >"$tmp/big/assets/index.js"

check() {
	name=$1 dir=$2 want=$3
	out=$(sh scripts/web-size.sh "$dir" 2>&1)
	got=$?
	if [ "$got" -ne "$want" ]; then
		echo "FAIL $name: exit $got, want $want — $out"
		fail=1
	else
		echo "ok   $name: $out"
	fi
}

check small "$tmp/small" 0
check inflated-600k "$tmp/big" 1
check empty "$tmp/empty" 1
exit $fail
