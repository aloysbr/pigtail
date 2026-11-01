#!/bin/sh
# web-size.sh — the web front's bundle budget (spec risk: <= 500 KB gzip).
# Sums the gzip -9 size of every .js and .css of the embedded dist
# (internal/webui/dist/assets, not web/dist), prints the total and fails
# above the budget. Over budget: review the dependencies first (lazy-load
# the palette before dropping cmdk). Usage: web-size.sh [dist-dir].
cd "$(dirname "$0")/.." || exit 1
dir=${1:-internal/webui/dist}
budget=${WEB_SIZE_BUDGET_KB:-500}
total=0
n=0
for f in "$dir"/assets/*.js "$dir"/assets/*.css; do
	[ -f "$f" ] || continue
	sz=$(gzip -9 -c "$f" | wc -c | tr -d ' ')
	total=$((total + sz))
	n=$((n + 1))
done
if [ "$n" -eq 0 ]; then
	echo "web-size: no .js or .css under $dir/assets — nothing measured" >&2
	exit 1
fi
kb=$(awk -v b="$total" 'BEGIN { printf "%.1f", b / 1024 }')
if [ "$total" -gt $((budget * 1024)) ]; then
	echo "web-size: $kb KB gzip over $n files — over the $budget KB budget" >&2
	exit 1
fi
echo "web-size: $kb KB gzip over $n files (budget $budget KB)"
