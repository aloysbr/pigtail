#!/bin/sh
# check-stamp.test.sh — prove when scripts/check-stamp.sh writes the stamp of
# a full make check, on throwaway git repos with fake commands.
#
# It runs under make test-hooks ⊂ make check ⊂ pre-push, where git exports
# GIT_DIR (and friends) toward the pushed repo: drop them first, or every
# git call below would act on that repo (incident 2026-09-04).
# shellcheck disable=SC2046 # one name per word, on purpose
unset $(git rev-parse --local-env-vars)
cd "$(dirname "$0")/.." || exit 1
script=$(pwd)/scripts/check-stamp.sh
fails=0

fail() {
	echo "FAIL $1"
	fails=$((fails + 1))
}

# new_repo prints the path of a fresh repo with one commit and a clean tree.
new_repo() {
	r=$(mktemp -d) || exit 1
	git -C "$r" init -q
	git -C "$r" config user.email t@example.com
	git -C "$r" config user.name t
	echo a >"$r/a"
	git -C "$r" add a
	git -C "$r" commit -q -m one
	printf '%s\n' "$r"
}

stamp_of() { printf '%s/pigtail-check/%s' "$(git -C "$1" rev-parse --path-format=absolute --git-common-dir)" "$2"; }

# clean tree, green command → stamp with its keys
r=$(new_repo)
sha=$(git -C "$r" rev-parse HEAD)
out=$(cd "$r" && sh "$script" run -- true 2>&1)
status=$?
f=$(stamp_of "$r" "$sha")
[ "$status" = 0 ] || fail "green: exit $status"
[ -f "$f" ] || fail "green: no stamp at $f"
for k in sha= seconds= go= ended=; do
	grep -q "$k" "$f" 2>/dev/null || fail "green: stamp lacks $k"
done
grep -q "sha=$sha " "$f" 2>/dev/null || fail "green: stamp does not name $sha"
[ "$(printf '%s\n' "$out" | tail -n 1)" = "check-stamp: stamped $sha" ] || fail "green: stdout $out"

# verify the stamped sha, then another one
got=$(cd "$r" && sh "$script" verify "$sha" 2>/dev/null)
[ $? = 0 ] || fail "verify: exit not 0"
[ "$got" = "$(cat "$f")" ] || fail "verify: printed $got"
other=2222222222222222222222222222222222222222
got=$(cd "$r" && sh "$script" verify "$other" 2>&1)
[ $? = 1 ] || fail "verify other: exit not 1"
[ "$got" = "check-stamp: no full make check recorded for $other" ] || fail "verify other: $got"
rm -rf "$r"

# red command → its exit, no stamp
r=$(new_repo)
sha=$(git -C "$r" rev-parse HEAD)
out=$(cd "$r" && sh "$script" run -- false 2>&1)
status=$?
[ "$status" = 1 ] || fail "red: exit $status, want 1"
[ ! -e "$(stamp_of "$r" "$sha")" ] || fail "red: a stamp was written"
printf '%s\n' "$out" | grep -qx 'check-stamp: check failed — no stamp' || fail "red: $out"
rm -rf "$r"

# dirty tree at start (a tracked file modified, or an untracked one) → no stamp
for dirt in modified untracked; do
	r=$(new_repo)
	sha=$(git -C "$r" rev-parse HEAD)
	if [ "$dirt" = modified ]; then echo b >"$r/a"; else echo n >"$r/new"; fi
	out=$(cd "$r" && sh "$script" run -- true 2>&1)
	status=$?
	[ "$status" = 0 ] || fail "dirty $dirt: exit $status"
	[ ! -e "$(stamp_of "$r" "$sha")" ] || fail "dirty $dirt: a stamp was written"
	printf '%s\n' "$out" | grep -qx 'check-stamp: tree dirty — no stamp' || fail "dirty $dirt: $out"
	rm -rf "$r"
done

# the command edits a file → no stamp
r=$(new_repo)
sha=$(git -C "$r" rev-parse HEAD)
out=$(cd "$r" && sh "$script" run -- sh -c 'echo c > a' 2>&1)
[ ! -e "$(stamp_of "$r" "$sha")" ] || fail "changed: a stamp was written"
printf '%s\n' "$out" | grep -qx 'check-stamp: tree changed during the run — no stamp' || fail "changed: $out"
rm -rf "$r"

# the command commits → HEAD moved, no stamp for either sha
r=$(new_repo)
sha=$(git -C "$r" rev-parse HEAD)
out=$(cd "$r" && sh "$script" run -- sh -c 'echo d > a && git commit -q -am two' 2>&1)
[ ! -e "$(stamp_of "$r" "$sha")" ] || fail "moved: a stamp was written for the old HEAD"
[ ! -e "$(stamp_of "$r" "$(git -C "$r" rev-parse HEAD)")" ] || fail "moved: a stamp was written for the new HEAD"
printf '%s\n' "$out" | grep -qx 'check-stamp: HEAD moved during the run — no stamp' || fail "moved: $out"
rm -rf "$r"

# under make -n the recursive make only prints: never a stamp
for flags in n nk "-n" "--dry-run"; do
	r=$(new_repo)
	sha=$(git -C "$r" rev-parse HEAD)
	out=$(cd "$r" && MAKEFLAGS=$flags sh "$script" run -- true 2>&1)
	[ ! -e "$(stamp_of "$r" "$sha")" ] || fail "dry run ($flags): a stamp was written"
	printf '%s\n' "$out" | grep -qx 'check-stamp: dry run — no stamp' || fail "dry run ($flags): $out"
	rm -rf "$r"
done
# other make flags still stamp
r=$(new_repo)
sha=$(git -C "$r" rev-parse HEAD)
(cd "$r" && MAKEFLAGS="k --no-print-directory -- PREPUSH_FILES=nn" sh "$script" run -- true >/dev/null 2>&1)
[ -f "$(stamp_of "$r" "$sha")" ] || fail "make -k: no stamp"
rm -rf "$r"

# outside a git checkout → the command still runs, no stamp
d=$(mktemp -d) || exit 1
out=$(cd "$d" && GIT_CEILING_DIRECTORIES=$d sh "$script" run -- sh -c 'echo ran > ran' 2>&1)
status=$?
[ "$status" = 0 ] || fail "no git: exit $status"
[ -f "$d/ran" ] || fail "no git: the command did not run"
printf '%s\n' "$out" | grep -qx 'check-stamp: not a git checkout — no stamp' || fail "no git: $out"
rm -rf "$d"

# run without a command → usage, exit 2, no stamp (an empty command would
# exit 0 and stamp a check that never ran)
for args in "run" "run --"; do
	r=$(new_repo)
	sha=$(git -C "$r" rev-parse HEAD)
	# shellcheck disable=SC2086 # split args into words on purpose
	err=$(cd "$r" && sh "$script" $args 2>&1 >/dev/null)
	status=$?
	[ "$status" = 2 ] || fail "empty ($args): exit $status"
	[ ! -e "$(stamp_of "$r" "$sha")" ] || fail "empty ($args): a stamp was written"
	[ "$err" = "usage: check-stamp.sh run -- <cmd…> | verify <sha>" ] || fail "empty ($args): stderr $err"
	rm -rf "$r"
done

# an inherited GIT_DIR toward another repo is ignored: the stamp goes to the
# repo of the working directory, the witness stays intact
r=$(new_repo)
w=$(new_repo)
sha=$(git -C "$r" rev-parse HEAD)
wsha=$(git -C "$w" rev-parse HEAD)
out=$(cd "$r" && GIT_DIR=$w/.git sh "$script" run -- true 2>&1)
[ -f "$(stamp_of "$r" "$sha")" ] || fail "GIT_DIR: no stamp in the working repo: $out"
[ ! -e "$w/.git/pigtail-check" ] || fail "GIT_DIR: the witness got a stamp"
[ "$(git -C "$w" rev-parse HEAD)" = "$wsha" ] || fail "GIT_DIR: the witness HEAD moved"
[ -z "$(git -C "$w" status --porcelain)" ] || fail "GIT_DIR: the witness tree changed"
rm -rf "$r" "$w"

if [ "$fails" != 0 ]; then
	echo "check-stamp.test.sh: $fails failure(s)"
	exit 1
fi
echo "check-stamp.test.sh: ok"
