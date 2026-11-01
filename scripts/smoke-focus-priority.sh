#!/usr/bin/env bash
# Isolated E2E smoke for focus priority in memory admission and the CPU slot
# history.
#
# Starts its own pigtaild (own socket, db, tmp repo, sidebar loop OFF), makes a
# subject with a worktree and an open focus, then proves:
#   1. the focus caller is admitted before an older non-focus one;
#   2. text `pigtail slots` marks the focus queue row with `*`, and only it;
#   3. `slots --history` shows a CPU section, one line per hold;
#   4. no focus name nor subject id leaks into `slots --json` or the history file;
#   5. the daemon log is silent about pins and the focus resolver.
#
# Usage: scripts/smoke-focus-priority.sh   (after `make build`)
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin"
S="$(mktemp -d /tmp/pt-smoke-focus.XXXXXX)"
SOCK="$S/p.sock"
DB="$S/p.db"
LOG="$S/daemon.log"
FOCUS_NAME="smoke-secret-focus"
fail=0
DPID=""

P() { "$BIN/pigtail" --socket "$SOCK" "$@"; }

cleanup() {
	[ -n "$DPID" ] && kill "$DPID" 2>/dev/null
	[ -n "$DPID" ] && wait "$DPID" 2>/dev/null
	rm -rf "$S"
}
trap cleanup EXIT

ok() { echo "PASS  $*"; }
ko() { echo "FAIL  $*"; fail=1; }

# A neutral caller: this shell may itself run inside a cmux pane or a session.
unset CMUX_WORKSPACE_ID CLAUDE_CODE_SESSION_ID PIGTAIL_ADMIT PIGTAIL_ADMIT_LEASE PIGTAIL_SOCKET

# Own config dir (setup.ConfigDir honours XDG_CONFIG_HOME): the daemon and the
# CLI read the built-in scope table, never the machine's real scopes.yaml.
export XDG_CONFIG_HOME="$S/config"
mkdir -p "$XDG_CONFIG_HOME"

[ -x "$BIN/pigtail" ] && [ -x "$BIN/pigtaild" ] || { echo "run make build first"; exit 2; }

# Repo with a base ref the worktree can branch from.
mkdir -p "$S/repo" "$S/elsewhere"
git -C "$S/repo" init -q -b main
git -C "$S/repo" -c user.name=smoke -c user.email=smoke@example.invalid commit -q --allow-empty -m init

# One job at a time and a low floor, so the queue is decided by order alone.
"$BIN/pigtaild" --socket "$SOCK" --db "$DB" --http "" --outputs-http "" \
	-admit-max-jobs 1 -admit-floor-mb 64 -cpu-slots 4 -orphan-reaper=off >"$LOG" 2>&1 &
DPID=$!
for _ in $(seq 1 50); do [ -S "$SOCK" ] && break; sleep 0.2; done
[ -S "$SOCK" ] || { echo "daemon did not start"; cat "$LOG"; exit 2; }

cd "$S/repo" || exit 2
P init --yes >/dev/null 2>&1 || { echo "init failed"; exit 2; }
cd "$S/repo/main" || exit 2
THEME=$(P theme create --name smoke --kind project --vault-link Notes/pigtail-smoke --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
SUBJ=$(P subject create --theme "$THEME" --title "Smoke focus subject" --local --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
WT=$(P new "smoke work" --project prj_repo --subject "$SUBJ" --base main --quiet | head -1)
[ -d "$WT" ] || { echo "worktree not created: $WT"; exit 2; }
P focus create "$FOCUS_NAME" --subject "$SUBJ" >/dev/null || { echo "focus create failed"; exit 2; }

# The resolver refreshes every 5 s: wait for a snapshot that knows the focus.
sleep 8

# A blocker holds the only lease; then the non-focus caller queues first, the
# focus caller second. Release order must put the focus caller first.
ORDER="$S/order"
: >"$ORDER"
(cd "$S/elsewhere" && P admit --class generic --label blocker -- sleep 12 >/dev/null 2>&1) &
J1=$!
sleep 2
(cd "$S/elsewhere" && P admit --class generic --label nonfocus -- sh -c "echo nonfocus >>'$ORDER'; sleep 1" >/dev/null 2>&1) &
J2=$!
sleep 2
(cd "$WT" && P admit --class generic --label focus -- sh -c "echo focus >>'$ORDER'; sleep 1" >/dev/null 2>&1) &
J3=$!
sleep 2

TEXT=$(P slots)
JSON=$(P slots --json)

# 2. `*` on the focus row only.
if echo "$TEXT" | grep -E '^\* .*focus' >/dev/null && ! echo "$TEXT" | grep -E '^\* .*nonfocus' >/dev/null; then
	ok "text slots marks the focus queue row with * and not the other"
else
	ko "focus marker wrong:"; echo "$TEXT"
fi

# 4. nothing about the focus itself in the JSON.
if echo "$JSON" | grep -q -e "$FOCUS_NAME" -e "$SUBJ"; then
	ko "slots --json leaks the focus name or the subject id"
else
	ok "slots --json carries no focus name nor subject id"
fi

wait "$J1" "$J2" "$J3"
FIRST=$(head -1 "$ORDER")
if [ "$FIRST" = "focus" ]; then
	ok "the focus caller was admitted before the older non-focus one"
else
	ko "admission order was: $(tr '\n' ' ' <"$ORDER")"
fi

# 3. two CPU holds through the socket, each dropped by closing the connection.
for n in a b; do
	curl -s --max-time 2 --unix-socket "$SOCK" -X POST -H 'Content-Type: application/json' \
		-d "{\"group\":\"smoke:vitest\",\"label\":\"$n.test.ts\"}" http://localhost/cpu/slots >/dev/null 2>&1
done
sleep 2 # let the history writer finish before reading
HIST=$(P slots --history)
if echo "$HIST" | grep -E 'cpu slots, last .*: 2 holds' >/dev/null; then
	ok "slots --history shows the CPU section with one line per hold (2 holds)"
else
	ko "CPU history section wrong:"; echo "$HIST"
fi

# 4. the history files either.
if grep -q -e "$FOCUS_NAME" -e "$SUBJ" "$S"/*history.jsonl 2>/dev/null; then
	ko "a history file leaks the focus name or the subject id"
else
	ok "history files carry no focus name nor subject id"
fi

# 5. the sidebar loop is off: the daemon must say nothing about pins or the resolver.
if grep -i -e "pinned" -e "focuspri" -e "focus resolver" "$LOG" >/dev/null; then
	ko "daemon log mentions pins or the focus resolver with the loop off:"; grep -i -e "pinned" -e "focuspri" -e "focus resolver" "$LOG"
else
	ok "daemon log is silent about pins and the focus resolver"
fi

[ "$fail" = 0 ] && echo "SMOKE OK" || echo "SMOKE FAILED"
exit "$fail"
