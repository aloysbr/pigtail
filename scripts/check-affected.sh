#!/bin/sh
# check-affected.sh <file> — the pre-push run: test what the listed files
# touch (internal/prepush selects it), or `make check-push-steps` (make check minus -race and the stamp) on any doubt.
#
# <file> holds one module-relative path per line, or `*` (no diff base),
# written by .githooks/pre-push. The selector prints `full <reason>`, or a
# `make <targets>` line, then `go-test <packages>` and `go-test-run <package>
# <regex>` lines. It never runs git; neither does this script.
cd "$(dirname "$0")/.." || exit 1
files=$1
make=${MAKE:-make}

full() {
	echo "pre-push: full check, without -race ($1)"
	exec "$make" --no-print-directory check-push-steps
}

[ -n "$files" ] && [ -f "$files" ] || full "no file list"

plan=$(go run ./internal/prepush/cmd/affected -files "$files")
status=$?
[ "$status" = 0 ] || full "selector exit $status"
first=$(printf '%s\n' "$plan" | head -n 1)
case $first in
"full "*) full "${first#full }" ;;
"make "*) ;;
*) full "unreadable selection: $first" ;;
esac

targets=${first#make }
pkgs=$(printf '%s\n' "$plan" | sed -n 's/^go-test //p')
runs=$(printf '%s\n' "$plan" | grep '^go-test-run ')
n=$(printf '%s\n' "$plan" | awk '$1 == "go-test" { n += NF - 1 } $1 == "go-test-run" { n++ } END { print n + 0 }')
echo "pre-push: affected — $n package(s), targets: $targets"
printf '%s\n' "$plan" | sed -n 's/^go-test/pre-push:   go-test/p'

# shellcheck disable=SC2086 # one target per word
"$make" --no-print-directory $targets || exit
if [ -n "$pkgs" ]; then
	# shellcheck disable=SC2086 # one package per word
	go test -count=1 -p 3 -parallel 4 -timeout 20m $pkgs || exit
fi
if [ -n "$runs" ]; then
	printf '%s\n' "$runs" | while read -r _ pkg regex; do
		go test -count=1 -timeout 20m -run "$regex" "$pkg" || exit
	done || exit
fi
echo "✓ check-affected passed"
