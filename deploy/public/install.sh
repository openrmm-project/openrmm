#!/usr/bin/env bash
# OpenRMM agent installer (Linux/macOS).
# Usage:
#   curl -fsSL http://SERVER/install.sh | sudo bash -s -- --server http://SERVER --token <ENROLL> [--site "Name"] [--require-tls]
set -euo pipefail

SERVER=""; TOKEN=""; SITE=""; REQUIRE_TLS=0
while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER="$2"; shift 2;;
    --token)  TOKEN="$2";  shift 2;;
    --site)   SITE="$2";   shift 2;;
    --require-tls) REQUIRE_TLS=1; shift;;
    *) echo "unknown arg: $1"; shift;;
  esac
done
[ -z "$SERVER" ] && { echo "error: --server is required"; exit 1; }
[ -z "$TOKEN" ]  && { echo "error: --token is required";  exit 1; }

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"      # linux | darwin
ARCH="$(uname -m)"
case "$ARCH" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo "unsupported arch: $ARCH"; exit 1;; esac
BIN="rmm-agent-${OS}-${ARCH}"
WS="$(printf '%s' "$SERVER" | sed -e 's#^https#wss#' -e 's#^http#ws#')"
DEST="/usr/local/bin/rmm-agent"
STATE_DIR="/etc/rmm-agent"; STATE="${STATE_DIR}/state.json"
AGENT_TLS_ARGS=()
if [ "$REQUIRE_TLS" = "1" ]; then
  case "$WS" in
    wss://*) AGENT_TLS_ARGS=(-require-tls);;
    *) echo "error: --require-tls needs an https:// server URL"; exit 1;;
  esac
fi

echo "OpenRMM agent installer"
echo "  server : $SERVER"
echo "  binary : $BIN"
mkdir -p "$STATE_DIR"

echo "Downloading agent..."
# Download to a temp file and move it into place atomically. A direct write over
# $DEST fails with "Text file busy" (curl: 23) when an agent is already running
# from that path (reinstall / update); rename replaces the directory entry while
# the running process keeps its open inode.
curl -fsSL "${SERVER}/dl/${BIN}" -o "${DEST}.new"
chmod +x "${DEST}.new"
mv -f "${DEST}.new" "$DEST"

echo "Installing/updating the system service..."
if "$DEST" -server "${WS}/ws/agent" -state "$STATE" -enroll-token "$TOKEN" "${AGENT_TLS_ARGS[@]}" -action install 2>/dev/null; then
  "$DEST" -action start 2>/dev/null || true
  echo "Service installed and started."
elif "$DEST" -action restart 2>/dev/null; then
  # Already installed: the binary was swapped above; restart to load the new one.
  echo "Existing service updated and restarted."
else
  echo "Service manager unavailable; starting agent in the background instead."
  pkill -f "$DEST" 2>/dev/null || true
  nohup "$DEST" -server "${WS}/ws/agent" -state "$STATE" -enroll-token "$TOKEN" "${AGENT_TLS_ARGS[@]}" >/var/log/rmm-agent.log 2>&1 &
  echo "Agent started (logs: /var/log/rmm-agent.log)."
fi
echo "Done. The device should appear under Devices shortly."
