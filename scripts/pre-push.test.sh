#!/bin/sh
# pre-push.test.sh — prove what .githooks/pre-push executes and which files
# it hands over, on throwaway git repos, with fake `pigtail` and `make`
# binaries that log their name and each argument in brackets (never "$*",
# which would flatten a quoted --label) and copy the PREPUSH_FILES list. The
# real pigtail is never called.
#
# It runs under make test-hooks ⊂ make check ⊂ pre-push, where git exports
# GIT_DIR toward the pushed repo: drop git's local env vars first, or the
# throwaway repos below would be built inside that repo.
# shellcheck disable=SC2046 # one name per word, on purpose
unset $(git rev-parse --local-env-vars)
cd "$(dirname "$0")/.." || exit 1
root=$(pwd)
zero=0000000000000000000000000000000000000000
fails=0

fail() {
	echo "FAIL $*"
	fails=$((fails + 1))
}

commit() { # commit <repo> <msg> <file>…: write each file, commit, print the sha
	r=$1 msg=$2
	shift 2
	for f in "$@"; do
		mkdir -p "$r/$(dirname "$f")"
		echo "$msg" >"$r/$f"
		git -C "$r" add "$f"
	done
	git -C "$r" commit -q -m "$msg"
	git -C "$r" rev-parse HEAD
}

new_repo() {
	r=$(mktemp -d) || exit 1
	git -C "$r" init -q
	git -C "$r" config user.email t@example.com
	git -C "$r" config user.name t
	printf '%s\n' "$r"
}

# The pushed repo: c0 (origin/main) → c1 → c2, and a side commit off c0.
repo=$(new_repo)
c0=$(commit "$repo" c0 a.txt)
git -C "$repo" update-ref refs/remotes/origin/main "$c0"
c1=$(commit "$repo" c1 e.txt)
c2=$(commit "$repo" c2 b/c.go d.md)
git -C "$repo" checkout -q -b side "$c0"
side=$(commit "$repo" side s.txt)
git -C "$repo" checkout -q -
since_c1='b/c.go
d.md'
since_c0='b/c.go
d.md
e.txt'

# run_case <name> <with_pigtail> <stdin> <want argv> <want files> [<want exit> <fake exit> <git_dir> <cwd>]
#   git_dir: the GIT_DIR exported to the hook, as git does ("-" = none: cwd discovery)
run_case() {
	name=$1 with_pigtail=$2 stdin=$3 want=$4 want_files=$5
	want_exit=${6:-0} fake_exit=${7:-0} git_dir=${8:-$repo/.git} cwd=${9:-$repo}
	tmp=$(mktemp -d) || exit 1
	mkdir "$tmp/bin"
	: >"$tmp/log"
	fake='#!/bin/sh
printf "%s" "$(basename "$0")" >> "$LOG"
for a in "$@"; do
	case $a in
	PREPUSH_FILES=*) f=${a#PREPUSH_FILES=}; cp "$f" "$COPY"; printf "%s\n" "$f" > "$COPY.path"; printf "[PREPUSH_FILES=…]" >> "$LOG" ;;
	*) printf "[%s]" "$a" >> "$LOG" ;;
	esac
done
printf "\n" >> "$LOG"
exit "${FAKE_EXIT:-0}"'
	printf '%s\n' "$fake" >"$tmp/bin/make"
	chmod +x "$tmp/bin/make"
	if [ "$with_pigtail" = 1 ]; then
		printf '%s\n' "$fake" >"$tmp/bin/pigtail"
		chmod +x "$tmp/bin/pigtail"
	fi
	path="$tmp/bin:$(dirname "$(command -v git)"):/usr/bin:/bin"
	if [ "$git_dir" = - ]; then
		(cd "$cwd" && printf '%s\n' "$stdin" | env -u GIT_DIR PATH="$path" LOG="$tmp/log" COPY="$tmp/files" FAKE_EXIT="$fake_exit" \
			sh "$root/.githooks/pre-push" >"$tmp/out" 2>"$tmp/err")
	else
		(cd "$cwd" && printf '%s\n' "$stdin" | env GIT_DIR="$git_dir" PATH="$path" LOG="$tmp/log" COPY="$tmp/files" FAKE_EXIT="$fake_exit" \
			sh "$root/.githooks/pre-push" >"$tmp/out" 2>"$tmp/err")
	fi
	status=$?
	got=$(cat "$tmp/log")
	files=$(cat "$tmp/files" 2>/dev/null)
	[ "$status" = "$want_exit" ] || fail "$name: exit $status, want $want_exit ($(cat "$tmp/err"))"
	[ "$got" = "$want" ] || fail "$name: argv
  want $want
  got  $got"
	[ "$files" = "$want_files" ] || fail "$name: files
  want $(printf '%s' "$want_files" | tr '\n' ' ')
  got  $(printf '%s' "$files" | tr '\n' ' ')"
	if [ -f "$tmp/files.path" ] && [ -e "$(cat "$tmp/files.path")" ]; then
		fail "$name: the hook left its file list behind"
	fi
	if [ "$want_exit" = 75 ]; then
		want_err='pre-push: push refused — no turn in the go-test queue after 30 min (pigtail slots shows who holds it).
pre-push: push again later, or skip the queue with PIGTAIL_ADMIT=0 git push.'
		[ "$(grep '^pre-push: push' "$tmp/err")" = "$want_err" ] || fail "$name: stderr
$(cat "$tmp/err")"
	fi
	rm -rf "$tmp"
	echo "ok $name"
}

admitted='pigtail[admit][--class][go-test][--max-wait][30m][--label][pre-push check-affected][--][make][check-affected][PREPUSH_FILES=…]'
fallback='make[check-affected][PREPUSH_FILES=…]'

run_case admitted 1 "refs/heads/x $c2 refs/heads/x $c1" "$admitted" "$since_c1"
run_case fallback 0 "refs/heads/x $c2 refs/heads/x $c1" "$fallback" "$since_c1"
run_case delete-only 1 "refs/heads/x $zero refs/heads/x $c1" '' ''
run_case new-branch 1 "refs/heads/x $c2 refs/heads/x $zero" "$admitted" "$since_c0"
run_case unknown-remote 1 "refs/heads/x $c2 refs/heads/x 2222222222222222222222222222222222222222" "$admitted" "$since_c0"
run_case diverged 1 "refs/heads/x $c2 refs/heads/x $side" "$admitted" "$since_c0"
run_case timeout 1 "refs/heads/x $c2 refs/heads/x $c1" "$admitted" "$since_c1" 75 75
run_case cwd-discovery 1 "refs/heads/x $c2 refs/heads/x $c1" "$admitted" "$since_c1" 0 0 -

# GIT_DIR names the repo the hook reads, not the cwd: a witness repo with
# other files, the hook run from $repo.
witness=$(new_repo)
w0=$(commit "$witness" w0 w.txt)
w1=$(commit "$witness" w1 wdir/w.go)
run_case git-dir 1 "refs/heads/x $w1 refs/heads/x $w0" "$admitted" 'wdir/w.go' 0 0 "$witness/.git" "$repo"
[ "$(git -C "$witness" rev-parse HEAD)" = "$w1" ] || fail "git-dir: the witness HEAD moved"
[ -z "$(git -C "$witness" status --porcelain)" ] || fail "git-dir: the witness tree changed"

git -C "$repo" update-ref -d refs/remotes/origin/main
run_case no-origin 1 "refs/heads/x $c2 refs/heads/x $zero" "$admitted" '*'

rm -rf "$repo" "$witness"
if [ "$fails" != 0 ]; then
	echo "pre-push.test.sh: $fails failure(s)"
	exit 1
fi
echo "pre-push.test.sh: ok"
