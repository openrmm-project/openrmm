#!/bin/bash
# Assemble, icon, sign and archive the macOS sidecar bundle.
#
# Privacy grants are keyed to a bundle, so the sidecar ships as one. Everything
# must be in place BEFORE codesign: signing seals the contents, and anything
# added afterwards invalidates the signature — which costs every grant the
# bundle holds, not just the new file.
#
# Usage: build-macos-bundle.sh <arch> <built-binary> <output.app.tar.gz>
# RMM_MACOS_CODESIGN_IDENTITY selects a persistent keychain identity when set.
# The default ad-hoc signature is suitable for builds that do not need to retain
# privacy grants across versions.
#
# Two archives come out: <output> signed with that identity, and beside it
# <name>.adhoc.app.tar.gz, the same bundle with an ad-hoc signature. A Mac is
# installed with one or the other (install.sh --macos-bundle), so an operator
# who does not want their endpoints to depend on the project's certificate does
# not have to build anything. Without an identity both are ad-hoc.
set -euo pipefail

arch="$1"; binary="$2"; out="$3"
case "$out" in
  *.app.tar.gz) adhoc_out="${out%.app.tar.gz}.adhoc.app.tar.gz" ;;
  *) echo "output must end in .app.tar.gz: $out" >&2; exit 2 ;;
esac
root="$(cd "$(dirname "$0")/.." && pwd)"
plist="$root/cmd/rmm-remote/macapp/Info.plist"
icon_src="$root/openrmm/assets/logo_open_rmm_edit3.png"

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
app="$stage/RMMRemote.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

cp "$plist" "$app/Contents/Info.plist"
cp "$binary" "$app/Contents/MacOS/rmm-remote"

# LSUIElement keeps it out of the Dock, so the icon's real audience is the
# Privacy & Security list, where an operator decides whether to trust it.
if command -v iconutil >/dev/null && command -v sips >/dev/null; then
  iconset="$stage/AppIcon.iconset"
  mkdir -p "$iconset"
  for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$icon_src" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    sips -z "$((size * 2))" "$((size * 2))" "$icon_src" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$app/Contents/Resources/AppIcon.icns"
else
  echo "warning: sips/iconutil unavailable — bundling without an icon" >&2
fi

identity="${RMM_MACOS_CODESIGN_IDENTITY:--}"
# Record the actual designated requirement, rather than only the certificate's
# display name. That lets the updater preserve grants for the same identity and
# clear stale rows when a certificate is rotated under the same name. The first
# signature is temporary; adding the marker invalidates it, then the final sign
# seals the complete bundle.
if [ "$identity" != "-" ]; then
  codesign --sign "$identity" --force --identifier io.openrmm.remote "$app"
  requirement="$(codesign -d -r- "$app" 2>&1 | sed -n 's/^designated => //p' | head -n 1)"
  if [ -z "$requirement" ]; then
    echo "could not read the designated requirement for $identity" >&2
    exit 1
  fi
  printf '%s\n' "$requirement" > "$app/Contents/Resources/StableCodeIdentity"
fi

codesign --sign "$identity" --force --identifier io.openrmm.remote "$app"
codesign --verify --deep --strict --verbose=2 "$app"

mkdir -p "$(dirname "$out")"
tar -czf "$out" -C "$stage" RMMRemote.app
echo "built $out ($arch)"

# The ad-hoc variant, from the same contents. The marker goes first: it records
# the certificate's requirement, and an ad-hoc bundle carrying it would claim an
# identity it does not have — the agent would then keep grants that macOS has
# already stopped honouring.
adhoc_stage="$stage/adhoc"
mkdir -p "$adhoc_stage"
cp -R "$app" "$adhoc_stage/RMMRemote.app"
rm -f "$adhoc_stage/RMMRemote.app/Contents/Resources/StableCodeIdentity"
codesign --sign - --force --identifier io.openrmm.remote "$adhoc_stage/RMMRemote.app"
codesign --verify --deep --strict --verbose=2 "$adhoc_stage/RMMRemote.app"
tar -czf "$adhoc_out" -C "$adhoc_stage" RMMRemote.app
echo "built $adhoc_out ($arch, ad-hoc)"
