#!/usr/bin/env bash
# OpenRMM agent installer (Linux/macOS).
set -euo pipefail

SERVER=""; TOKEN=""; UPDATE_PUBLIC_KEY=""; LAB_MODE=0; REQUIRE_TLS=0; RESET_IDENTITY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --server) [ $# -ge 2 ] || { echo "error: --server needs a value"; exit 2; }; SERVER="$2"; shift 2;;
    --token) [ $# -ge 2 ] || { echo "error: --token needs a value"; exit 2; }; TOKEN="$2"; shift 2;;
    --site) [ $# -ge 2 ] || { echo "error: --site needs a value"; exit 2; }; shift 2;;
    --update-public-key) [ $# -ge 2 ] || { echo "error: --update-public-key needs a value"; exit 2; }; UPDATE_PUBLIC_KEY="${UPDATE_PUBLIC_KEY:+$UPDATE_PUBLIC_KEY,}$2"; shift 2;;
    --require-tls) REQUIRE_TLS=1; shift;;
    --lab-mode) LAB_MODE=1; shift;;
    --reset-identity) RESET_IDENTITY=1; shift;;
    *) echo "error: unknown argument: $1"; exit 2;;
  esac
done
[ -n "$SERVER" ] || { echo "error: --server is required"; exit 1; }
[ -n "$TOKEN" ] || { echo "error: --token is required"; exit 1; }
SERVER="${SERVER%/}"

if [ "$REQUIRE_TLS" = "1" ]; then
  case "$SERVER" in https://*) ;; *) echo "error: --require-tls needs an https:// server URL"; exit 1;; esac
fi

if [ "$LAB_MODE" = "0" ]; then
  case "$SERVER" in https://*) ;; *) echo "error: production install requires an https:// server; use --lab-mode only in an isolated lab"; exit 1;; esac
  [ -n "$UPDATE_PUBLIC_KEY" ] || { echo "error: production install requires --update-public-key"; exit 1; }
fi
command -v curl >/dev/null || { echo "error: curl is required"; exit 1; }
command -v openssl >/dev/null || { echo "error: OpenSSL is required for checksum/signature verification"; exit 1; }
openssl version | grep -q '^OpenSSL 3\.' || { echo "error: OpenSSL 3.x is required for Ed25519 verification"; exit 1; }

# --update-public-key may be given several times (or comma/space separated). During
# a key rotation an operator pins the new key alongside the old one; a signature is
# accepted if it verifies under any pinned key, so the fleet converges without a
# reinstall. Split the list into an array of trusted keys.
PUBLIC_KEYS=()
if [ -n "$UPDATE_PUBLIC_KEY" ]; then
  _oldifs="$IFS"; IFS=', ;'
  set -f
  # shellcheck disable=SC2086
  for _k in $UPDATE_PUBLIC_KEY; do [ -n "$_k" ] && PUBLIC_KEYS+=("$_k"); done
  set +f
  IFS="$_oldifs"
fi

# write_pubkey_der <base64-key> <out-file>: wrap a raw Ed25519 key in DER SPKI.
write_pubkey_der() {
  printf '\060\052\060\005\006\003\053\145\160\003\041\000' > "$2"
  printf '%s' "$1" | openssl base64 -d -A >> "$2" 2>/dev/null || return 1
  [ "$(wc -c < "$2" | tr -d ' ')" = "44" ]
}

# verify_under_any_key <digest-file> <signature-file>: succeed if any pinned key verifies.
verify_under_any_key() {
  local _der="${TMP_DIR}/verify.der" _key
  for _key in "${PUBLIC_KEYS[@]}"; do
    write_pubkey_der "$_key" "$_der" || { echo "error: update public key is not a valid 32-byte base64 key"; exit 1; }
    if openssl pkeyutl -verify -pubin -inkey "$_der" -keyform DER -rawin -in "$1" -sigfile "$2" >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in linux|darwin) ;; *) echo "error: unsupported OS: $OS"; exit 1;; esac
ARCH="$(uname -m)"
case "$ARCH" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo "error: unsupported architecture: $ARCH"; exit 1;; esac
BIN="rmm-agent-${OS}-${ARCH}"
WS="$(printf '%s' "$SERVER" | sed -e 's#^https#wss#' -e 's#^http#ws#')"
DEST="/usr/local/bin/rmm-agent"
STATE_DIR="/etc/rmm-agent"; STATE="${STATE_DIR}/state.json"; HEALTH="${STATE_DIR}/health.json"
BACKUP="${DEST}.install-old"; SIGNATURE_PATH="${DEST}.sig"; SIGNATURE_BACKUP="${SIGNATURE_PATH}.install-old"
STATE_BACKUP="${STATE}.install-old"; OUTBOX_BACKUP="${STATE}.outbox.install-old"
TMP_DIR=""; TRANSACTION_STARTED=0; SWAPPED=0; COMMITTED=0; HAD_OLD=0; HAD_STATE=0; HAD_OUTBOX=0
SERVICE_EXISTED=0; SERVICE_KIND=""; SERVICE_PATH=""; SERVICE_BACKUP_DIR=""; SERVICE_WAS_ENABLED=0

detect_service_config() {
  for path in /etc/systemd/system/rmm-agent.service /lib/systemd/system/rmm-agent.service /usr/lib/systemd/system/rmm-agent.service; do
    if [ -f "$path" ]; then SERVICE_KIND=systemd; SERVICE_PATH="$path"; return 0; fi
  done
  if [ -f /Library/LaunchDaemons/rmm-agent.plist ]; then SERVICE_KIND=launchd; SERVICE_PATH=/Library/LaunchDaemons/rmm-agent.plist; return 0; fi
  if [ -f /etc/init/rmm-agent.conf ]; then SERVICE_KIND=upstart; SERVICE_PATH=/etc/init/rmm-agent.conf; return 0; fi
  if [ -f /etc/init.d/rmm-agent ]; then SERVICE_KIND=init; SERVICE_PATH=/etc/init.d/rmm-agent; return 0; fi
  return 1
}

snapshot_service_config() {
  SERVICE_BACKUP_DIR="${TMP_DIR}/service-config"
  mkdir -p "$SERVICE_BACKUP_DIR"
  cp -a "$SERVICE_PATH" "$SERVICE_BACKUP_DIR/"
  if [ "$SERVICE_KIND" = systemd ] && [ -d "${SERVICE_PATH}.d" ]; then
    cp -a "${SERVICE_PATH}.d" "$SERVICE_BACKUP_DIR/"
  fi
  if [ "$SERVICE_KIND" = systemd ] && systemctl is-enabled --quiet rmm-agent.service; then
    SERVICE_WAS_ENABLED=1
  fi
}

restore_service_config() {
  rm -f "$SERVICE_PATH"
  [ "$SERVICE_KIND" != systemd ] || rm -rf "${SERVICE_PATH}.d"
  cp -a "$SERVICE_BACKUP_DIR/$(basename "$SERVICE_PATH")" "$SERVICE_PATH"
  if [ "$SERVICE_KIND" = systemd ] && [ -d "$SERVICE_BACKUP_DIR/$(basename "$SERVICE_PATH").d" ]; then
    cp -a "$SERVICE_BACKUP_DIR/$(basename "$SERVICE_PATH").d" "${SERVICE_PATH}.d"
  fi
  if [ "$SERVICE_KIND" = systemd ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [ "$SERVICE_WAS_ENABLED" = "1" ]; then
      systemctl enable rmm-agent.service >/dev/null 2>&1 || true
    else
      systemctl disable rmm-agent.service >/dev/null 2>&1 || true
    fi
  elif [ "$SERVICE_KIND" = launchd ]; then
    launchctl bootstrap system "$SERVICE_PATH" >/dev/null 2>&1 || launchctl load -w "$SERVICE_PATH" >/dev/null 2>&1 || true
  fi
}

rollback() {
  status=$?
  if [ "$COMMITTED" = "0" ] && [ "$TRANSACTION_STARTED" = "1" ]; then
    echo "Install did not become healthy; rolling back."
    if [ -x "$DEST" ]; then
      "$DEST" -action stop >/dev/null 2>&1 || true
      "$DEST" -action uninstall >/dev/null 2>&1 || true
    fi
    if [ -f "$BACKUP" ]; then
      rm -f "$DEST"
      mv -f "$BACKUP" "$DEST"
    elif [ "$HAD_OLD" = "0" ] && [ "$SWAPPED" = "1" ]; then
      rm -f "$DEST"
    fi
    rm -f "$SIGNATURE_PATH"
    [ ! -f "$SIGNATURE_BACKUP" ] || mv -f "$SIGNATURE_BACKUP" "$SIGNATURE_PATH"
    [ "$HAD_STATE" = "0" ] || { rm -f "$STATE"; mv -f "$STATE_BACKUP" "$STATE"; }
    [ "$HAD_OUTBOX" = "0" ] || { rm -f "${STATE}.outbox"; mv -f "$OUTBOX_BACKUP" "${STATE}.outbox"; }
    if [ "$SERVICE_EXISTED" = "1" ] && [ -x "$DEST" ]; then
      restore_service_config
      "$DEST" -action start >/dev/null 2>&1 || true
    fi
  fi
  [ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
  if [ "$COMMITTED" = "1" ]; then
    return 0
  fi
  return "$status"
}
trap rollback EXIT

mkdir -p "$STATE_DIR"
[ ! -e "$BACKUP" ] || { echo "error: unresolved installer backup exists at $BACKUP"; exit 1; }
[ ! -e "$SIGNATURE_BACKUP" ] || { echo "error: unresolved signature backup exists at $SIGNATURE_BACKUP"; exit 1; }
[ ! -e "$STATE_BACKUP" ] || { echo "error: unresolved state backup exists at $STATE_BACKUP"; exit 1; }
[ ! -e "$OUTBOX_BACKUP" ] || { echo "error: unresolved outbox backup exists at $OUTBOX_BACKUP"; exit 1; }
[ ! -e "${DEST}.update-pending" ] || { echo "error: a self-update transaction is still pending"; exit 1; }
TMP_DIR="$(mktemp -d "${STATE_DIR}/install.XXXXXX")"
CANDIDATE="${TMP_DIR}/${BIN}"
MANIFEST="${TMP_DIR}/SHA256SUMS"
SIG_TEXT="${TMP_DIR}/SHA256SUMS.sig"
ARTIFACT_SIG_TEXT="${TMP_DIR}/${BIN}.sig"

echo "Downloading and verifying ${BIN}..."
curl -fsSL "${SERVER}/dl/${BIN}" -o "$CANDIDATE"
curl -fsSL "${SERVER}/dl/SHA256SUMS" -o "$MANIFEST"
expected="$(awk -v name="$BIN" '$2 == name { print $1; count++ } END { if (count != 1) exit 1 }' "$MANIFEST")" || {
  echo "error: manifest does not contain exactly one entry for $BIN"; exit 1;
}
actual="$(openssl dgst -sha256 "$CANDIDATE" | awk '{print $NF}')"
[ "$actual" = "$expected" ] || { echo "error: SHA-256 mismatch for $BIN"; exit 1; }

if [ -n "$UPDATE_PUBLIC_KEY" ]; then
  # Validate every pinned key up front so a typo fails clearly, not as a mismatch.
  for _k in "${PUBLIC_KEYS[@]}"; do
    write_pubkey_der "$_k" "${TMP_DIR}/keycheck.der" || { echo "error: update public key is not a valid 32-byte base64 key"; exit 1; }
  done
  curl -fsSL "${SERVER}/dl/SHA256SUMS.sig" -o "$SIG_TEXT"
  read -r sig_version sig_base64 extra < "$SIG_TEXT" || true
  [ "$sig_version" = "rmm-ed25519-sha256-v1" ] && [ -n "$sig_base64" ] && [ -z "${extra:-}" ] || {
    echo "error: invalid SHA256SUMS signature format"; exit 1;
  }
  printf '%s' "$sig_base64" | openssl base64 -d -A > "${TMP_DIR}/signature.bin" 2>/dev/null || {
    echo "error: signature is not valid base64"; exit 1;
  }
  openssl dgst -sha256 -binary "$MANIFEST" > "${TMP_DIR}/manifest.digest"
  verify_under_any_key "${TMP_DIR}/manifest.digest" "${TMP_DIR}/signature.bin" || {
    echo "error: Ed25519 manifest signature verification failed (no pinned key matched)"; exit 1;
  }
  curl -fsSL "${SERVER}/dl/${BIN}.sig" -o "$ARTIFACT_SIG_TEXT"
  read -r artifact_sig_version artifact_sig_base64 artifact_extra < "$ARTIFACT_SIG_TEXT" || true
  [ "$artifact_sig_version" = "rmm-ed25519-sha256-v1" ] && [ -n "$artifact_sig_base64" ] && [ -z "${artifact_extra:-}" ] || {
    echo "error: invalid artifact signature format"; exit 1;
  }
  printf '%s' "$artifact_sig_base64" | openssl base64 -d -A > "${TMP_DIR}/artifact-signature.bin" 2>/dev/null || {
    echo "error: artifact signature is not valid base64"; exit 1;
  }
  openssl dgst -sha256 -binary "$CANDIDATE" > "${TMP_DIR}/artifact.digest"
  verify_under_any_key "${TMP_DIR}/artifact.digest" "${TMP_DIR}/artifact-signature.bin" || {
    echo "error: Ed25519 artifact signature verification failed (no pinned key matched)"; exit 1;
  }
else
  echo "WARNING: lab mode without an update key verifies only the downloaded checksum manifest."
fi
chmod 0755 "$CANDIDATE"

if [ -x "$DEST" ]; then
  current_version="$($DEST -print-version)" || { echo "error: installed agent version could not be read"; exit 1; }
  "$CANDIDATE" -check-upgrade-from "$current_version" || { echo "error: refusing agent downgrade"; exit 1; }
  HAD_OLD=1
fi

echo "Installing agent transactionally..."
TRANSACTION_STARTED=1
if [ "$HAD_OLD" = "1" ]; then
  if detect_service_config; then
    SERVICE_EXISTED=1
    "$DEST" -action stop >/dev/null 2>&1 || { echo "error: could not stop existing service"; exit 1; }
    snapshot_service_config
  elif "$DEST" -action stop >/dev/null 2>&1; then
    echo "error: existing service configuration is not in a supported location; refusing to alter it"
    exit 1
  fi
fi
if [ -f "$STATE" ]; then
  cp -p "$STATE" "$STATE_BACKUP"
  HAD_STATE=1
fi
if [ -f "${STATE}.outbox" ]; then
  cp -p "${STATE}.outbox" "$OUTBOX_BACKUP"
  HAD_OUTBOX=1
fi
if [ "$HAD_OLD" = "1" ]; then
  [ "$SERVICE_EXISTED" = "0" ] || "$DEST" -action uninstall >/dev/null 2>&1 || { echo "error: could not remove existing service definition"; exit 1; }
  mv "$DEST" "$BACKUP"
  [ ! -f "$SIGNATURE_PATH" ] || mv "$SIGNATURE_PATH" "$SIGNATURE_BACKUP"
  SWAPPED=1
fi
mv "$CANDIDATE" "$DEST"
[ ! -f "$ARTIFACT_SIG_TEXT" ] || cp "$ARTIFACT_SIG_TEXT" "$SIGNATURE_PATH"
SWAPPED=1

if [ "$RESET_IDENTITY" = "1" ]; then
  rm -f "$STATE" "${STATE}.outbox"
fi

security_args=(-require-tls -update-verify strict -update-public-key "$UPDATE_PUBLIC_KEY")
if [ "$LAB_MODE" = "1" ]; then
  security_args=(-lab-mode -update-verify off)
  [ -z "$UPDATE_PUBLIC_KEY" ] || security_args=(-lab-mode -update-verify strict -update-public-key "$UPDATE_PUBLIC_KEY")
fi
if [ ! -f "$STATE" ]; then
  "$DEST" -server "${WS}/ws/agent" -state "$STATE" -enroll-token "$TOKEN" "${security_args[@]}" -enroll-only
fi

rm -f "$HEALTH"
install_args=(-server "${WS}/ws/agent" -state "$STATE" -health-file "$HEALTH" "${security_args[@]}")
"$DEST" "${install_args[@]}" -action install
"$DEST" -action start

echo "Waiting for an authenticated agent connection..."
healthy=0
for _ in $(seq 1 60); do
  if [ -f "$HEALTH" ] && awk -v version="$("$DEST" -print-version)" 'index($0, "\"version\":\"" version "\"") { found=1 } END { exit !found }' "$HEALTH"; then
    healthy=1
    break
  fi
  sleep 1
done
[ "$healthy" = "1" ] || { echo "error: agent did not report a healthy authenticated connection"; exit 1; }

COMMITTED=1
rm -f "$BACKUP" "$SIGNATURE_BACKUP" "$STATE_BACKUP" "$OUTBOX_BACKUP"
echo "Agent installed, connected, and healthy."
