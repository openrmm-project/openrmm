#!/usr/bin/env bash
# OpenRMM agent uninstaller (Linux/macOS).
#
# `rmm-agent -action uninstall` only drops the service definition. Everything
# else stays: the binary, the enrolment state — which holds credentials that
# still authenticate to the server — and, on macOS, the remote-desktop bundle,
# its LaunchAgent and the privacy grants attached to it. A decommissioned
# endpoint keeps working credentials on disk until this runs.
set -euo pipefail
# Only the system's own tools, as in install.sh. This runs as root, and `sudo`
# keeps the caller's PATH, where /usr/local/bin and Homebrew's directories come
# first: on a Mac they usually belong to an ordinary account, which could put a
# `uname` or `rm` there for root to run.
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

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

# The service is stopped with the system's tools, never by running the agent:
# on a Mac /usr/local/bin, where agents used to live, is usually writable by the
# account that owns Homebrew, and a file planted there as rmm-agent would run as
# root. The service definitions are removed further down.
if [ "$OS" = "darwin" ]; then
  launchctl bootout system/rmm-agent >/dev/null 2>&1 || true
else
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now rmm-agent.service >/dev/null 2>&1 || true
  fi
  if [ -f /etc/init/rmm-agent.conf ] && command -v initctl >/dev/null 2>&1; then
    initctl stop rmm-agent >/dev/null 2>&1 || true
  fi
  if [ -x /etc/init.d/rmm-agent ]; then
    /etc/init.d/rmm-agent stop >/dev/null 2>&1 || true
  fi
fi

# Both the current location and the one used before the agent moved out of
# /usr/local/bin on macOS.
AGENT_DIRS=""
for candidate in /usr/local/libexec/openrmm/rmm-agent /usr/local/bin/rmm-agent; do
  if [ -x "$candidate" ]; then
    rm -f "$candidate" "${candidate}.sig" "${candidate}.update-etag" "${candidate}.new" \
      "${candidate}.update-pending" "${candidate}.update-pending.new" "${candidate}.install-old" "${candidate}.old"
    echo "Removed $candidate"
    AGENT_DIRS="$AGENT_DIRS $(dirname "$candidate")"
  fi
done

# What the agent and the remote-desktop sidecar write beside the agent binary:
# the agent's log, the sidecar itself on Linux (on macOS it is the bundle removed
# below), what fetching it leaves, and the sidecar's log. None of it was removed,
# and on macOS the leftovers kept /usr/local/libexec/openrmm — with the agent's
# log inside — from being deleted at all. Only in a directory an agent was
# found in, and only regular files: /usr/local/bin is shared, and an agent.log
# there that no agent sat beside is someone else's.
for dir in $AGENT_DIRS; do
  for name in agent.log agent.log.1 rmm-remote rmm-remote.etag rmm-remote.new rmm-remote.log rmm-remote.log.1; do
    file="$dir/$name"
    if [ -f "$file" ] && [ ! -L "$file" ]; then
      rm -f "$file"
      echo "Removed $file"
    fi
  done
  if [ -d "$dir/rmm-remote.new.stage" ] && [ ! -L "$dir/rmm-remote.new.stage" ]; then
    rm -rf "$dir/rmm-remote.new.stage"
  fi
done
rmdir /usr/local/libexec/openrmm 2>/dev/null || true

# The service definitions: the agent is never run to remove them (see above),
# and a definition left behind would keep trying to start a missing binary at
# every boot.
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
  done
  # Privacy rows survive the application and would keep listing an allowed app
  # that no longer exists. Accessibility is system-wide, Screen Recording is the
  # console user's, so both contexts are reset — and it has to happen before the
  # bundle below is deleted: tccutil resolves the identifier through the
  # installed app and refuses one it cannot find. That is also why the retired
  # identifier is not reset here; its app has been gone since the rename.
  tccutil reset All "$REMOTE_LABEL" >/dev/null 2>&1 || true
  if [ "$CONSOLE_UID" != "0" ]; then
    launchctl asuser "$CONSOLE_UID" tccutil reset All "$REMOTE_LABEL" >/dev/null 2>&1 || true
  fi
  if [ -d "/Library/Application Support/OpenRMM" ]; then
    rm -rf "/Library/Application Support/OpenRMM"
    echo "Removed the remote-desktop bundle"
  fi
  # The bundle cannot write inside itself without breaking its signature, so the
  # sidecar logs to the console user's ~/Library/Logs instead.
  for log in /Users/*/Library/Logs/rmm-remote.log /Users/*/Library/Logs/rmm-remote.log.1; do
    if [ -f "$log" ] && [ ! -L "$log" ]; then
      rm -f "$log"
      echo "Removed $log"
    fi
  done
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
