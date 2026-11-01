#!/bin/sh
# check-stamp.sh — the proof a full make check passed on a sha.
#
#   check-stamp.sh run -- <cmd…>   run cmd; stamp HEAD only if cmd exits 0,
#                                  the tree was clean at the start, and HEAD
#                                  and the tree are unchanged at the end
#   check-stamp.sh verify <sha>    exit 0 and print the stamp, or exit 1
#
# The stamp is one line `sha=… seconds=… go=… ended=…` in
# $(git rev-parse --git-common-dir)/pigtail-check/<sha>: shared by every
# worktree of the clone, local to this machine. Tools read it through
# `verify` instead of re-running the tests (the merge gate, D1/D9).
#
# It follows the repo of the working directory, never an inherited GIT_DIR
# (git exports one to hooks, and make check runs under the pre-push hook).
# shellcheck disable=SC2046 # one name per word, on purpose
unset $(git rev-parse --local-env-vars)

say() { printf 'check-stamp: %s\n' "$*"; }

common_dir() { git rev-parse --path-format=absolute --git-common-dir 2>/dev/null; }

# dry_run: under `make -n` the recursive make only prints its recipes, so a
# zero exit proves nothing. GNU make puts single-letter flags, dashless, in
# the first word of MAKEFLAGS ("n", "nk"); long forms may follow.
dry_run() {
	# shellcheck disable=SC2086 # split MAKEFLAGS into words on purpose
	set -- $MAKEFLAGS
	case ${1:-} in
	*[!a-zA-Z]* | "") ;;
	*n*) return 0 ;;
	esac
	for w in "$@"; do
		case $w in
		--) return 1 ;;
		-n | --dry-run | --just-print | --recon) return 0 ;;
		esac
	done
	return 1
}

fingerprint() { git status --porcelain --untracked-files=all | cksum; }

case $1 in
run)
	shift
	[ "${1:-}" = "--" ] && shift
	# No command would exit 0 and stamp a check that never ran.
	if [ $# = 0 ]; then
		echo "usage: check-stamp.sh run -- <cmd…> | verify <sha>" >&2
		exit 2
	fi
	if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		"$@"
		status=$?
		say "not a git checkout — no stamp"
		exit "$status"
	fi
	head0=$(git rev-parse HEAD)
	dirty0=$(git status --porcelain --untracked-files=all)
	print0=$(fingerprint)
	start=$(date +%s)
	"$@"
	status=$?
	end=$(date +%s)
	if [ "$status" != 0 ]; then
		say "check failed — no stamp"
		exit "$status"
	fi
	if dry_run; then
		say "dry run — no stamp"
		exit 0
	fi
	if [ -n "$dirty0" ]; then
		say "tree dirty — no stamp"
		exit 0
	fi
	if [ "$(git rev-parse HEAD)" != "$head0" ]; then
		say "HEAD moved during the run — no stamp"
		exit 0
	fi
	if [ "$(fingerprint)" != "$print0" ]; then
		say "tree changed during the run — no stamp"
		exit 0
	fi
	dir=$(common_dir)/pigtail-check
	mkdir -p "$dir" || exit 1
	gover=$(go env GOVERSION 2>/dev/null) || gover=unknown
	[ -n "$gover" ] || gover=unknown
	tmp=$(mktemp "$dir/.tmp.XXXXXX") || exit 1
	if ! printf 'sha=%s seconds=%s go=%s ended=%s\n' "$head0" "$((end - start))" "$gover" \
		"$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$tmp" || ! mv "$tmp" "$dir/$head0"; then
		rm -f "$tmp"
		exit 1
	fi
	say "stamped $head0"
	;;
verify)
	sha=$2
	[ -n "$sha" ] || { echo "usage: check-stamp.sh verify <sha>" >&2; exit 2; }
	f=$(common_dir)/pigtail-check/$sha
	if [ -n "$(common_dir)" ] && [ -f "$f" ]; then
		cat "$f"
		exit 0
	fi
	say "no full make check recorded for $sha" >&2
	exit 1
	;;
*)
	echo "usage: check-stamp.sh run -- <cmd…> | verify <sha>" >&2
	exit 2
	;;
esac
