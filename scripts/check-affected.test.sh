#!/bin/sh
# check-affected.test.sh — prove what the pre-push run (scripts/check-affected.sh)
# and the test targets of the Makefile execute, against fakes and make -n:
#   no_race_p3             the affected go test has no -race, runs -p 3 -parallel 4;
#   full_fallback_unstamped the full fallback runs check-push-steps, never check
#                          (check has -race and stamps the sha);
#   test_race_flags        make test-race is -race -p 3 -parallel 4, make test-push
#                          the same without -race.
cd "$(dirname "$0")/.." || exit 1
root=$(pwd)
fail=0
ok() { echo "ok   $1"; }
ko() { echo "FAIL $1"; fail=1; }

tmp=$(mktemp -d) || exit 1
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"

# The fake go logs its argv; `go run …/affected` answers with a scripted plan
# (FAKE_PLAN=ok) or fails (FAKE_PLAN=fail); every other call succeeds.
cat >"$tmp/bin/go" <<'FAKE'
#!/bin/sh
echo "go $*" >>"$LOG"
if [ "$1" = run ]; then
	[ "$FAKE_PLAN" = ok ] || exit 3
	printf 'make vet\ngo-test ./x\ngo-test-run ./y ^TestZ$\n'
fi
exit 0
FAKE
cat >"$tmp/bin/fakemake" <<'FAKE'
#!/bin/sh
echo "make $*" >>"$LOG"
exit 0
FAKE
chmod +x "$tmp/bin/go" "$tmp/bin/fakemake"
echo 'cmd/pigtail/main.go' >"$tmp/files"

run() { # plan -> $tmp/log
	: >"$tmp/log"
	PATH="$tmp/bin:$PATH" LOG="$tmp/log" FAKE_PLAN=$1 MAKE="$tmp/bin/fakemake" \
		sh "$root/scripts/check-affected.sh" "$tmp/files" >/dev/null 2>&1
}

run ok
tests=$(grep '^go test' "$tmp/log")
[ -n "$tests" ] || ko "no_race_p3: no go test was run: $(cat "$tmp/log")"
echo "$tests" | grep -q -- '-race' && ko "no_race_p3: a -race reached the affected go test: $tests" || ok "no_race_p3: no -race"
[ "$(echo "$tests" | grep -c -- '-p 3 -parallel 4')" -ge 1 ] && ok "no_race_p3: -p 3 -parallel 4 on the package run" || ko "no_race_p3: want -p 3 -parallel 4: $tests"
echo "$tests" | grep -- '-run' | grep -q -- '-timeout 20m' && ok "no_race_p3: targeted -run kept" || ko "no_race_p3: targeted -run missing: $tests"

run fail
if grep -q '^make .*check-push-steps' "$tmp/log" && ! grep -Eq '^make( --no-print-directory)? check$' "$tmp/log"; then
	ok "full_fallback_unstamped: runs check-push-steps"
else
	ko "full_fallback_unstamped: want make check-push-steps, never make check: $(cat "$tmp/log")"
fi

race=$(make -n test-race 2>&1)
echo "$race" | grep -q -- 'go test -race -count=1 -p 3 -parallel 4 -timeout 20m ./...' \
	&& ok "test_race_flags: test-race" || ko "test_race_flags: test-race: $race"
push=$(make -n test-push 2>&1)
if echo "$push" | grep -q -- 'go test -count=1 -p 3 -parallel 4 -timeout 20m ./...' && ! echo "$push" | grep -q -- '-race'; then
	ok "test_race_flags: test-push"
else
	ko "test_race_flags: test-push: $push"
fi

exit "$fail"
