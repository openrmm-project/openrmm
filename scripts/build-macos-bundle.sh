#!/bin/bash
# Assemble, icon, sign and archive the macOS sidecar bundle.
#
# Privacy grants are keyed to a bundle, so the sidecar ships as one. Everything
# must be in place BEFORE codesign: signing seals the contents, and anything
# added afterwards invalidates the signature — which costs every grant the
# bundle holds, not just the new file.
#
# Usage: build-macos-bundle.sh <arch> <built-binary> <output.tar.gz>
# RMM_MACOS_CODESIGN_IDENTITY selects a persistent keychain identity when set.
# The default ad-hoc signature is suitable for builds that do not need to retain
# privacy grants across versions.
set -euo pipefail

arch="$1"; binary="$2"; out="$3"
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

# The agent clears the endpoint's stale privacy grants after an update, because
# an ad-hoc signature makes every build a new application to macOS. This marker
# tells it not to, and it has to exist before signing to stay inside the seal.
if [ -n "${RMM_MACOS_CODESIGN_IDENTITY:-}" ] && [ "$RMM_MACOS_CODESIGN_IDENTITY" != "-" ]; then
  printf '%s\n' "$RMM_MACOS_CODESIGN_IDENTITY" > "$app/Contents/Resources/StableCodeIdentity"
fi

codesign --sign "${RMM_MACOS_CODESIGN_IDENTITY:--}" --force --identifier io.openrmm.remote "$app"
codesign --verify --deep --strict --verbose=2 "$app"

mkdir -p "$(dirname "$out")"
tar -czf "$out" -C "$stage" RMMRemote.app
echo "built $out ($arch)"
