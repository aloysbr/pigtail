#!/bin/sh
# prepush-differential.sh — replay the pre-push selector on the last 30
# first-parent commits of origin/main (D11, internal/prepush
# TestSelectionCoversRecentCommits). Git stays here; the Go test reads the
# `<sha>\t<path>` lines this script writes.
#
# Run it admitted: pigtail admit --class go-test -- sh scripts/prepush-differential.sh
# shellcheck disable=SC2046 # one name per word, on purpose
unset $(git rev-parse --local-env-vars)
cd "$(dirname "$0")/.." || exit 1

tmp=$(mktemp) || exit 1
trap 'rm -f "$tmp"' EXIT INT TERM

shas=$(git log --first-parent -n 30 --format=%H origin/main) || exit 1
[ -n "$shas" ] || { echo "prepush-differential: origin/main has no commit" >&2; exit 1; }
for sha in $shas; do
	git diff --name-only --no-renames "$sha^" "$sha" | while IFS= read -r p; do
		printf '%s\t%s\n' "$sha" "$p"
	done
done >"$tmp" || exit 1

echo "prepush-differential: $(wc -l <"$tmp" | tr -d ' ') path(s) in $(printf '%s\n' "$shas" | wc -l | tr -d ' ') commit(s)"
PREPUSH_DIFF_INPUT=$tmp go test -count=1 -v -run '^TestSelectionCoversRecentCommits$' ./internal/prepush/
