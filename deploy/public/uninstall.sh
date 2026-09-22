#!/usr/bin/env bash
# OpenRMM agent uninstaller (Linux/macOS).
#
# `rmm-agent -action uninstall` only drops the service definition. Everything
# else stays: the binary, the enrolment state — which holds credentials that
# still authenticate to the server — and, on macOS, the remote-desktop bundle,
# its LaunchAgent and the privacy grants attached to it. A decommissioned
# endpoint keeps working credentials on disk until this runs.
set -euo pipefail

KEEP_STATE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --keep-state) KEEP_STATE=1; shift;;
    -h|--help)
      echo "usage: sudo bash $0 [--keep-state]"
      echo "  --keep-state  leave /etc/rmm-agent in place (reinstall keeps the same device)"
      exit 0;;
    *) echo "error: unknown argument: $1"; exit 2;;
  esac
done

[ "$(id -u)" = "0" ] || { echo "error: run as root (e.g. sudo bash $0)"; exit 1; }

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
STATE_DIR="/etc/rmm-agent"
REMOTE_LABEL="io.openrmm.remote"
LEGACY_REMOTE_LABEL="com.openrmm.remote"

# Both the current location and the one used before the agent moved out of
# /usr/local/bin on macOS.
for candidate in /usr/local/libexec/openrmm/rmm-agent /usr/local/bin/rmm-agent; do
  if [ -x "$candidate" ]; then
    "$candidate" -action stop >/dev/null 2>&1 || true
    "$candidate" -action uninstall >/dev/null 2>&1 || true
    rm -f "$candidate" "${candidate}.sig" "${candidate}.update-etag" \
      "${candidate}.new" "${candidate}.update-pending" "${candidate}.install-old"
    echo "Removed $candidate"
  fi
done
rmdir /usr/local/libexec/openrmm 2>/dev/null || true

# Service definitions the agent's own uninstall may not have reached (it is gone
# by now, and a definition left behind would keep trying to start a missing
# binary at every boot).
for unit in /etc/systemd/system/rmm-agent.service /lib/systemd/system/rmm-agent.service \
            /usr/lib/systemd/system/rmm-agent.service /etc/init/rmm-agent.conf /etc/init.d/rmm-agent; do
  [ -e "$unit" ] || continue
  rm -f "$unit"
  echo "Removed $unit"
done
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ "$OS" = "darwin" ]; then
  if [ -f /Library/LaunchDaemons/rmm-agent.plist ]; then
    launchctl bootout system/rmm-agent >/dev/null 2>&1 || true
    rm -f /Library/LaunchDaemons/rmm-agent.plist
    echo "Removed the rmm-agent LaunchDaemon"
  fi
  CONSOLE_UID="$(stat -f '%u' /dev/console 2>/dev/null || echo 0)"
  for label in "$REMOTE_LABEL" "$LEGACY_REMOTE_LABEL"; do
    plist="/Library/LaunchAgents/${label}.plist"
    if [ "$CONSOLE_UID" != "0" ]; then
      launchctl bootout "gui/${CONSOLE_UID}/${label}" >/dev/null 2>&1 || true
    fi
    [ ! -f "$plist" ] || { rm -f "$plist"; echo "Removed $plist"; }
    # Privacy rows survive the application and would keep listing an allowed app
    # that no longer exists. Accessibility is system-wide, Screen Recording is
    # the console user's, so both contexts are reset.
    tccutil reset All "$label" >/dev/null 2>&1 || true
    if [ "$CONSOLE_UID" != "0" ]; then
      launchctl asuser "$CONSOLE_UID" tccutil reset All "$label" >/dev/null 2>&1 || true
    fi
  done
  if [ -d "/Library/Application Support/OpenRMM" ]; then
    rm -rf "/Library/Application Support/OpenRMM"
    echo "Removed the remote-desktop bundle"
  fi
  rm -f "${STATE_DIR}/remote-session.json"
fi

if [ "$KEEP_STATE" = "1" ]; then
  echo "Kept $STATE_DIR (credentials still on disk; remove the device in the console if it is being retired)."
else
  rm -rf "$STATE_DIR"
  echo "Removed $STATE_DIR"
fi

echo
echo "OpenRMM agent removed."
echo "Local removal does not revoke anything: delete the device in the OpenRMM"
echo "console so its credentials stop being accepted."
