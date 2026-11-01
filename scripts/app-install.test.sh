#!/usr/bin/env bash
# app-install.test.sh — prove scripts/app-install.sh against fake ps/kill/open
# and a temporary /Applications; no real process, app or trash is touched.
# Run with bash (arrays): `bash scripts/app-install.test.sh`.
cd "$(dirname "$0")/.." || exit 1
root=$(pwd)
script="$root/scripts/app-install.sh"
failures=0

fail() {
	echo "FAIL $case_name: $*"
	failures=$((failures + 1))
}

# A fresh sandbox: $apps (fake /Applications), $trash, $bin (fakes), $procs
# (one "pid args" line per fake process), kill.log, open.log.
setup() {
	case_name=$1
	before=$failures
	tmp=$(mktemp -d) || exit 1
	apps="$tmp/Applications"
	trash="$tmp/trash"
	bin="$tmp/bin"
	procs="$tmp/procs"
	mkdir -p "$apps" "$trash" "$bin"
	: >"$procs"
	: >"$tmp/kill.log"
	: >"$tmp/open.log"
	: >"$tmp/stubborn"
	mkdir -p "$apps/Pigtail.app.new/Contents/MacOS"
	echo new >"$apps/Pigtail.app.new/Contents/MacOS/Pigtail"

	cat >"$bin/ps" <<'EOF'
#!/usr/bin/env bash
cat "$FAKE_PROCS"
EOF
	# kill -TERM <pid>: the process goes, unless its pid is listed as stubborn.
	cat >"$bin/kill" <<'EOF'
#!/usr/bin/env bash
echo "kill $*" >>"$FAKE_DIR/kill.log"
pid=${2:-}
grep -qx "$pid" "$FAKE_DIR/stubborn" && exit 0
grep -v "^$pid " "$FAKE_PROCS" >"$FAKE_PROCS.tmp" || true
mv "$FAKE_PROCS.tmp" "$FAKE_PROCS"
EOF
	# open -g -a <bundle> [--args …]: starts <bundle>'s executable, unless FAKE_OPEN_NOOP.
	cat >"$bin/open" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "open $*" >>"$FAKE_DIR/open.log"
[ -n "${FAKE_OPEN_NOOP:-}" ] && exit 0
bundle=$3
shift 3
echo "$((RANDOM + 1000)) $bundle/Contents/MacOS/Pigtail $*" >>"$FAKE_PROCS"
EOF
	# mv, failing on demand: FAKE_MV_FAIL=staged (the staged app's move) or =trash (a move into the trash).
	cat >"$bin/mv" <<'EOF'
#!/usr/bin/env bash
case "${FAKE_MV_FAIL:-}" in
staged) case "$1" in *.new) exit 1 ;; esac ;;
trash) case "${@: -1}" in "$FAKE_TRASH"/*) exit 1 ;; esac ;;
esac
exec /bin/mv "$@"
EOF
	chmod +x "$bin/ps" "$bin/kill" "$bin/open" "$bin/mv"
}

# A live fake app: its bundle on disk and a process under it.
app() { # <name.app> <pid>
	mkdir -p "$apps/$1/Contents/MacOS"
	echo old >"$apps/$1/Contents/MacOS/bin"
	echo "$2 $apps/$1/Contents/MacOS/bin" >>"$procs"
}

run() {
	PATH="$bin:$PATH" FAKE_TRASH="$trash" FAKE_PROCS="$procs" FAKE_DIR="$tmp" PS_BIN="$bin/ps" KILL_BIN="$bin/kill" OPEN_BIN="$bin/open" \
		PIGTAIL_INSTALL_WAIT=1 PIGTAIL_APPS_DIR="$apps" PIGTAIL_TRASH_DIR="$trash" \
		bash "$script" "$apps/Pigtail.app.new" "$apps/Pigtail.app" >"$tmp/out" 2>"$tmp/err"
	status=$?
}

trash_count() { find "$trash" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' '; }
new_installed() { [ "$(cat "$apps/Pigtail.app/Contents/MacOS/Pigtail" 2>/dev/null)" = new ]; }
open_count() { wc -l <"$tmp/open.log" | tr -d ' '; }
done_case() {
	chmod -R u+w "$tmp" 2>/dev/null
	rm -rf "$tmp"
	[ "$failures" = "$before" ] && echo "ok $case_name"
}

# 1. fresh install: nothing running, no legacy → dest replaced, no relaunch, no trash entry
setup fresh
run
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
new_installed || fail "dest not installed"
[ ! -e "$apps/Pigtail.app.new" ] || fail "staged left behind"
[ "$(trash_count)" = 0 ] || fail "trash not empty"
[ "$(open_count)" = 0 ] || fail "open called"
[ "$(cat "$tmp/out")" = replaced ] || fail "output: $(cat "$tmp/out")"
done_case

# 2. legacy bar + cockpit running → both terminated, both trashed, dest installed, relaunched once with --background
setup legacy-running
app PigtailBar.app 101
app "Pigtail Cockpit.app" 102
run
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
grep -qx "kill -TERM 101" "$tmp/kill.log" || fail "bar not terminated"
grep -qx "kill -TERM 102" "$tmp/kill.log" || fail "cockpit not terminated"
[ ! -e "$apps/PigtailBar.app" ] && [ ! -e "$apps/Pigtail Cockpit.app" ] || fail "legacy left in place"
[ "$(trash_count)" = 2 ] || fail "trash has $(trash_count) entries"
ls "$trash" | grep -q '^PigtailBar .*\.app$' || fail "bar not in trash"
ls "$trash" | grep -q '^Pigtail Cockpit .*\.app$' || fail "cockpit not in trash"
new_installed || fail "dest not installed"
[ "$(open_count)" = 1 ] || fail "open called $(open_count) times"
grep -qx "open -g -a $apps/Pigtail.app --args --background" "$tmp/open.log" || fail "relaunch: $(cat "$tmp/open.log")"
grep -qx "relaunched --background" "$tmp/out" || fail "no relaunch line"
done_case

# 3. a process ignores SIGTERM → exit 1, stderr names it, dest NOT replaced, staged .new kept, legacy NOT trashed,
#    and every process that WAS stopped is reopened (OPEN_BIN log names the legacy bar)
setup stubborn
app PigtailBar.app 101
app "Pigtail Cockpit.app" 102
echo 102 >"$tmp/stubborn"
run
[ "$status" = 1 ] || fail "exit $status"
grep -q "app-install: $apps/Pigtail Cockpit.app still running (pid 102), nothing replaced" "$tmp/err" || fail "stderr: $(cat "$tmp/err")"
[ ! -e "$apps/Pigtail.app" ] || fail "dest replaced"
[ -d "$apps/Pigtail.app.new" ] || fail "staged removed"
[ -d "$apps/PigtailBar.app" ] && [ -d "$apps/Pigtail Cockpit.app" ] || fail "legacy moved"
[ "$(trash_count)" = 0 ] || fail "trash not empty"
grep -qx "open -g -a $apps/PigtailBar.app" "$tmp/open.log" || fail "bar not reopened: $(cat "$tmp/open.log")"
grep -q "app-install: relaunched $apps/PigtailBar.app after failure" "$tmp/err" || fail "no relaunch line"
done_case

# 4. re-run with the new app running → old dest replaced by rename then removed (trash dir stays empty), relaunched --background
setup rerun
mkdir -p "$apps/Pigtail.app/Contents/MacOS"
echo old >"$apps/Pigtail.app/Contents/MacOS/Pigtail"
echo "201 $apps/Pigtail.app/Contents/MacOS/Pigtail" >>"$procs"
run
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
grep -qx "kill -TERM 201" "$tmp/kill.log" || fail "running app not terminated"
new_installed || fail "dest not replaced"
[ -z "$(find "$apps" -maxdepth 1 -name 'Pigtail.app.old.*')" ] || fail "old copy left"
[ "$(trash_count)" = 0 ] || fail "trash not empty"
[ "$(open_count)" = 1 ] || fail "open called $(open_count) times"
grep -qx "open -g -a $apps/Pigtail.app --args --background" "$tmp/open.log" || fail "relaunch: $(cat "$tmp/open.log")"
done_case

# 5. path with a space ("Pigtail Cockpit.app") matched exactly; a process under "Pigtail Cockpit.app.bak" is not touched
setup exact-path
app "Pigtail Cockpit.app" 301
app "Pigtail Cockpit.app.bak" 302
run
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
grep -qx "kill -TERM 301" "$tmp/kill.log" || fail "cockpit not terminated"
! grep -q "302" "$tmp/kill.log" || fail ".bak touched"
[ -d "$apps/Pigtail Cockpit.app.bak" ] || fail ".bak moved"
grep -q "^302 " "$procs" || fail ".bak process gone"
done_case

# 6. nothing running before → OPEN_BIN never called
setup nothing-running
mkdir -p "$apps/PigtailBar.app/Contents/MacOS"
run
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
[ "$(open_count)" = 0 ] || fail "open called: $(cat "$tmp/open.log")"
[ ! -s "$tmp/kill.log" ] || fail "kill called"
[ "$(trash_count)" = 1 ] || fail "idle legacy bar not trashed"
done_case

# 7. trash dir not writable (chmod 500) → exit 1 before any kill: KILL_BIN log empty, nothing moved
setup trash-readonly
app PigtailBar.app 101
chmod 500 "$trash"
run
[ "$status" = 1 ] || fail "exit $status"
grep -q "app-install: cannot write to $trash, nothing stopped" "$tmp/err" || fail "stderr: $(cat "$tmp/err")"
[ ! -s "$tmp/kill.log" ] || fail "kill called: $(cat "$tmp/kill.log")"
[ -d "$apps/Pigtail.app.new" ] && [ ! -e "$apps/Pigtail.app" ] && [ -d "$apps/PigtailBar.app" ] || fail "something moved"
[ "$(open_count)" = 0 ] || fail "open called"
done_case

# 8. relaunch never shows up (OPEN_BIN fake starts nothing) → exit 1, stderr "did not start"
setup no-start
app PigtailBar.app 101
export FAKE_OPEN_NOOP=1
run
unset FAKE_OPEN_NOOP
[ "$status" = 1 ] || fail "exit $status"
grep -q "app-install: $apps/Pigtail.app did not start" "$tmp/err" || fail "stderr: $(cat "$tmp/err")"
new_installed || fail "dest not installed"
# the EXIT trap relaunches the replaced app once more, as the menu bar alone
[ "$(open_count)" = 2 ] || fail "open called $(open_count) times, want 2 (relaunch + trap): $(cat "$tmp/open.log")"
[ "$(grep -cx "open -g -a $apps/Pigtail.app --args --background" "$tmp/open.log")" = 2 ] || fail "trap relaunch missing: $(cat "$tmp/open.log")"
grep -q "app-install: relaunched $apps/Pigtail.app after failure" "$tmp/err" || fail "no trap relaunch line: $(cat "$tmp/err")"
done_case

# 9. PIGTAIL_TRASH_DIR unset → exit 1, nothing stopped (no silent default)
setup trash-unset
app PigtailBar.app 101
FAKE_PROCS="$procs" FAKE_DIR="$tmp" PS_BIN="$bin/ps" KILL_BIN="$bin/kill" OPEN_BIN="$bin/open" \
	PIGTAIL_INSTALL_WAIT=1 PIGTAIL_APPS_DIR="$apps" \
	env -u PIGTAIL_TRASH_DIR bash "$script" "$apps/Pigtail.app.new" "$apps/Pigtail.app" >"$tmp/out" 2>"$tmp/err"
status=$?
[ "$status" = 1 ] || fail "exit $status"
[ ! -s "$tmp/kill.log" ] || fail "kill called"
[ -d "$apps/Pigtail.app.new" ] && [ ! -e "$apps/Pigtail.app" ] || fail "something moved"
done_case

# 10. several pids under one stubborn bundle → exit 1, the first pid is named, no SIGPIPE-driven silent exit
setup stubborn-many
mkdir -p "$apps/PigtailBar.app/Contents/MacOS"
for n in $(seq 401 700); do
	echo "$n $apps/PigtailBar.app/Contents/MacOS/bin" >>"$procs"
	echo "$n" >>"$tmp/stubborn"
done
run
[ "$status" = 1 ] || fail "exit $status"
grep -q "app-install: $apps/PigtailBar.app still running (pid 401), nothing replaced" "$tmp/err" || fail "stderr: $(cat "$tmp/err")"
[ ! -e "$apps/Pigtail.app" ] || fail "dest replaced"
done_case

# 11. the staged app cannot be moved in → the old app is put back and relaunched --background (trap), exit 1
setup mv-staged-fails
mkdir -p "$apps/Pigtail.app/Contents/MacOS"
echo old >"$apps/Pigtail.app/Contents/MacOS/Pigtail"
echo "201 $apps/Pigtail.app/Contents/MacOS/Pigtail" >>"$procs"
export FAKE_MV_FAIL=staged
run
unset FAKE_MV_FAIL
[ "$status" = 1 ] || fail "exit $status"
[ "$(cat "$apps/Pigtail.app/Contents/MacOS/Pigtail" 2>/dev/null)" = old ] || fail "old app not restored"
[ -z "$(find "$apps" -maxdepth 1 -name 'Pigtail.app.old.*')" ] || fail "old copy left"
[ -d "$apps/Pigtail.app.new" ] || fail "staged lost"
grep -qx "open -g -a $apps/Pigtail.app --args --background" "$tmp/open.log" || fail "no relaunch: $(cat "$tmp/open.log")"
grep -q "app-install: relaunched $apps/Pigtail.app after failure" "$tmp/err" || fail "stderr: $(cat "$tmp/err")"
done_case

# 12. step 6 fails after the swap (a legacy app cannot be trashed) → new app stays, relaunched --background (trap), exit 1
setup mv-trash-fails
app PigtailBar.app 101
export FAKE_MV_FAIL=trash
run
unset FAKE_MV_FAIL
[ "$status" = 1 ] || fail "exit $status"
new_installed || fail "dest not installed"
[ -d "$apps/PigtailBar.app" ] || fail "legacy bar vanished"
grep -qx "open -g -a $apps/Pigtail.app --args --background" "$tmp/open.log" || fail "no relaunch: $(cat "$tmp/open.log")"
grep -q "app-install: relaunched $apps/Pigtail.app after failure" "$tmp/err" || fail "stderr: $(cat "$tmp/err")"
done_case

# 13. APP_DEST's parent absent → created, install succeeds (staged lives elsewhere)
setup dest-parent-missing
mv "$apps/Pigtail.app.new" "$tmp/Pigtail.app.new"
PATH="$bin:$PATH" FAKE_PROCS="$procs" FAKE_DIR="$tmp" PS_BIN="$bin/ps" KILL_BIN="$bin/kill" OPEN_BIN="$bin/open" \
	PIGTAIL_INSTALL_WAIT=1 PIGTAIL_APPS_DIR="$apps" PIGTAIL_TRASH_DIR="$trash" \
	bash "$script" "$tmp/Pigtail.app.new" "$tmp/newdir/sub/Pigtail.app" >"$tmp/out" 2>"$tmp/err"
status=$?
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
[ "$(cat "$tmp/newdir/sub/Pigtail.app/Contents/MacOS/Pigtail" 2>/dev/null)" = new ] || fail "dest not installed"
done_case

# 14. trash dir absent → created, legacy app trashed into it
setup trash-missing
rmdir "$trash"
app PigtailBar.app 101
run
[ "$status" = 0 ] || fail "exit $status: $(cat "$tmp/err")"
[ "$(trash_count)" = 1 ] || fail "trash has $(trash_count) entries"
new_installed || fail "dest not installed"
done_case

# 15. make app creates a missing APP_DEST parent before the cp (dry run)
setup make-dest-parent
if make -n app APP_DEST="$tmp/none/Pigtail.app" 2>/dev/null | grep -q "mkdir -p \"$tmp/none/\""; then :; else fail "Makefile does not mkdir the APP_DEST parent"; fi
done_case

if [ "$failures" != 0 ]; then
	echo "app-install.test.sh: $failures failure(s)"
	exit 1
fi
echo "app-install.test.sh: all cases passed"
