#!/bin/sh
# macapp-makefile.test.sh — prove the macOS app target of the Makefile by its
# dry run (make -n): nothing is built, signed or installed.
#   - make app builds one Pigtail.app, one swift build of the Pigtail product;
#   - it copies both icons (Ocean, Terra), required;
#   - it signs with SIGN_IDENTITY;
#   - it clears a stale <dest>.new before copying into it;
#   - it hands the install folders to scripts/app-install.sh on its command line;
#   - a verify build (APP_BUNDLE_ID) rewrites the bundle id, a release build does not;
#   - the retired per-app targets are gone.
cd "$(dirname "$0")/.." || exit 1
fail=0
ok() { echo "ok   $1"; }
ko() { echo "FAIL $1"; fail=1; }

vars='SIGN_IDENTITY=TEST-ID APP_DEST=/tmp/x/Pigtail.app PIGTAIL_APPS_DIR=/tmp/x PIGTAIL_TRASH_DIR=/tmp/x/trash'

app=$(eval make -n app $vars 2>&1) || { ko "make -n app exits 0: $app"; app=""; }
n=$(echo "$app" | grep -c 'swift build')
[ "$n" -eq 1 ] && ok "one swift build" || ko "one swift build: got $n"
echo "$app" | grep 'swift build' | grep -q -- '--product Pigtail' && ok "builds the Pigtail product" || ko "builds the Pigtail product"
echo "$app" | grep -q 'cp "macapp/Resources/AppIcon.icns" "macapp/Pigtail.app/Contents/Resources/AppIcon.icns"' \
	&& ok "copies the Océan icon" || ko "copies the Océan icon"
echo "$app" | grep -q 'cp "macapp/Resources/AppIconTerra.icns" "macapp/Pigtail.app/Contents/Resources/AppIconTerra.icns"' \
	&& ok "copies the Terra icon" || ko "copies the Terra icon"
echo "$app" | grep -q 'cp "macapp/Info.plist" "macapp/Pigtail.app/Contents/Info.plist"' \
	&& ok "copies the one Info.plist" || ko "copies the one Info.plist"
echo "$app" | grep -q 'if \[ -f' && ko "an icon copy is conditional" || ok "icon copies are unconditional"
signs=$(echo "$app" | grep -c 'codesign --force --sign "TEST-ID"')
[ "$signs" -ge 2 ] && ok "signs the build and the staged copy with SIGN_IDENTITY" || ko "signs with SIGN_IDENTITY: got $signs"

# The stale .new is removed before the copy into it (line order).
rm_line=$(echo "$app" | grep -n -F 'rm -rf "/tmp/x/Pigtail.app.new"' | head -n 1 | cut -d: -f1)
cp_line=$(echo "$app" | grep -n -F 'cp -R "macapp/Pigtail.app" "/tmp/x/Pigtail.app.new"' | head -n 1 | cut -d: -f1)
if [ -n "$rm_line" ] && [ -n "$cp_line" ] && [ "$rm_line" -lt "$cp_line" ]; then
	ok "a stale .new is removed before the copy"
else
	ko "a stale .new is removed before the copy (rm line '$rm_line', cp line '$cp_line')"
fi

echo "$app" | grep -q -F 'PIGTAIL_APPS_DIR="/tmp/x" PIGTAIL_TRASH_DIR="/tmp/x/trash" scripts/app-install.sh "/tmp/x/Pigtail.app.new" "/tmp/x/Pigtail.app"' \
	&& ok "install folders are written on the script's command line" || ko "install folders are written on the script's command line"

echo "$app" | grep -q 'PlistBuddy' && ko "a release build rewrites its bundle id" || ok "a release build keeps sh.pigtail.app"
verify=$(eval make -n app $vars APP_BUNDLE_ID=sh.pigtail.app.verify 2>&1) || ko "make -n app (verify) exits 0: $verify"
echo "$verify" | grep -q -F 'Set :CFBundleIdentifier sh.pigtail.app.verify' \
	&& ok "a verify build rewrites its bundle id" || ko "a verify build rewrites its bundle id"

noterra=$(eval make -n app $vars APP_TERRA_ICON=/tmp/x/absent-terra.icns 2>&1) \
	&& ko "app without its Terra icon must fail" \
	|| { echo "$noterra" | grep -q 'absent-terra.icns' && ok "app stops when the Terra icon is missing" || ko "app stops when the Terra icon is missing: $noterra"; }
noocean=$(eval make -n app $vars APP_ICON=/tmp/x/absent.icns 2>&1) \
	&& ko "app without its Ocean icon must fail" \
	|| { echo "$noocean" | grep -q 'absent.icns' && ok "app stops when the Ocean icon is missing" || ko "app stops when the Ocean icon is missing: $noocean"; }

make -n pigtail-bar >/dev/null 2>&1 && ko "the retired pigtail-bar target is gone" || ok "the retired pigtail-bar target is gone"

exit $fail
