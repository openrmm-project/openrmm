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
# Checked before anything is written. Without it the install failed halfway
# through with a bare "mkdir: /etc/rmm-agent: Permission denied"; install.ps1
# has always refused up front, and this is the same contract.
[ "$(id -u)" = "0" ] || { echo "error: run as root (e.g. sudo bash $0 ...)"; exit 1; }

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

# Ed25519 verification needs OpenSSL 3; SHA-256 does not. Requiring it up front
# refused to run on a stock macOS, which ships LibreSSL as /usr/bin/openssl —
# even in lab mode, where no key is pinned and no signature is ever checked. A
# production install always pins a key, so this still cannot be skipped there.
# Tested through the variable rather than ${#PUBLIC_KEYS[@]}: macOS ships bash
# 3.2, where an empty array expansion under `set -u` is an unbound variable.
if [ -n "$UPDATE_PUBLIC_KEY" ]; then
  openssl version | grep -qE '^OpenSSL ([3-9]|[1-9][0-9])\.' || {
    echo "error: OpenSSL 3.x or newer is required to verify the update signature"
    echo "       (found: $(openssl version)); install it, or omit --update-public-key in a lab"
    exit 1
  }
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
# A service running as root must not execute a binary from a directory somebody
# else can write to. On macOS /usr/local is routinely handed to the logged-in
# admin by Homebrew, which would let a process without root replace the agent
# launchd runs as root — so the agent lives in a directory this installer owns.
# Linux keeps /usr/local/bin, which is root-owned there; the guard below checks
# it rather than assuming it.
LEGACY_DEST=""
if [ "$OS" = "darwin" ]; then
  DEST_DIR="/usr/local/libexec/openrmm"
  LEGACY_DEST="/usr/local/bin/rmm-agent"
else
  DEST_DIR="/usr/local/bin"
fi
DEST="${DEST_DIR}/rmm-agent"
MIGRATE_FROM=""
STATE_DIR="/etc/rmm-agent"; STATE="${STATE_DIR}/state.json"; HEALTH="${STATE_DIR}/health.json"
BACKUP="${DEST}.install-old"; SIGNATURE_PATH="${DEST}.sig"; SIGNATURE_BACKUP="${SIGNATURE_PATH}.install-old"
STATE_BACKUP="${STATE}.install-old"; OUTBOX_BACKUP="${STATE}.outbox.install-old"
TMP_DIR=""; TRANSACTION_STARTED=0; SWAPPED=0; COMMITTED=0; HAD_OLD=0; HAD_STATE=0; HAD_OUTBOX=0
SERVICE_EXISTED=0; SERVICE_KIND=""; SERVICE_PATH=""; SERVICE_BACKUP_DIR=""; SERVICE_WAS_ENABLED=0

# assert_root_owned refuses a directory that is not root's, or that root's group
# or anyone else can write to. Either would let a non-root account replace a
# binary that runs as root, which no signature check can catch: verification
# happens at download, and this is a swap afterwards.
assert_root_owned() {
  dir="$1"
  # BSD and GNU stat share no format flags, and GNU's -f means something else
  # entirely (filesystem info), so the platform picks the syntax.
  if [ "$OS" = "darwin" ]; then
    owner="$(stat -f '%u' "$dir")" && perms="$(stat -f '%Lp' "$dir")" || {
      echo "error: cannot read ownership of $dir"; exit 1;
    }
  else
    owner="$(stat -c '%u' "$dir")" && perms="$(stat -c '%a' "$dir")" || {
      echo "error: cannot read ownership of $dir"; exit 1;
    }
  fi
  if [ "$owner" != "0" ]; then
    echo "error: $dir is owned by uid $owner, not root."
    echo "       The agent runs as root and must not execute from a directory"
    echo "       another account controls. Fix it with:"
    echo "         sudo chown root \"$dir\" && sudo chmod 755 \"$dir\""
    exit 1
  fi
  if [ $(( 8#$perms & 8#022 )) -ne 0 ]; then
    echo "error: $dir is writable by group or others (mode $perms)."
    echo "       Fix it with: sudo chmod 755 \"$dir\""
    exit 1
  fi
}

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
    elif [ -n "$MIGRATE_FROM" ] && [ -x "$MIGRATE_FROM" ]; then
      # The move never completed: the previous install is still on disk, so its
      # service definition goes back and it takes the endpoint over again.
      [ "$SERVICE_EXISTED" = "0" ] || restore_service_config
      "$MIGRATE_FROM" -action start >/dev/null 2>&1 || true
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
# A clean macOS has no /usr/local/bin — it is Homebrew that usually creates it.
# Without this the transactional swap failed at the very last step, with mv
# reporting "No such file or directory" about a source file that plainly existed.
mkdir -p "$DEST_DIR"
# Created by root, but an existing directory keeps whatever ownership it had —
# including a directory somebody pre-created to sit in the agent's path.
chown 0 "$DEST_DIR" 2>/dev/null || true
chmod 755 "$DEST_DIR" 2>/dev/null || true
assert_root_owned "$DEST_DIR"
assert_root_owned "$STATE_DIR"
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
  if [ "$sig_version" != "rmm-ed25519-sha256-v1" ] || [ -z "$sig_base64" ] || [ -n "${extra:-}" ]; then
    echo "error: invalid SHA256SUMS signature format"; exit 1
  fi
  printf '%s' "$sig_base64" | openssl base64 -d -A > "${TMP_DIR}/signature.bin" 2>/dev/null || {
    echo "error: signature is not valid base64"; exit 1;
  }
  openssl dgst -sha256 -binary "$MANIFEST" > "${TMP_DIR}/manifest.digest"
  verify_under_any_key "${TMP_DIR}/manifest.digest" "${TMP_DIR}/signature.bin" || {
    echo "error: Ed25519 manifest signature verification failed (no pinned key matched)"; exit 1;
  }
  curl -fsSL "${SERVER}/dl/${BIN}.sig" -o "$ARTIFACT_SIG_TEXT"
  read -r artifact_sig_version artifact_sig_base64 artifact_extra < "$ARTIFACT_SIG_TEXT" || true
  if [ "$artifact_sig_version" != "rmm-ed25519-sha256-v1" ] || [ -z "$artifact_sig_base64" ] || [ -n "${artifact_extra:-}" ]; then
    echo "error: invalid artifact signature format"; exit 1
  fi
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
elif [ -n "$LEGACY_DEST" ] && [ -x "$LEGACY_DEST" ]; then
  # An endpoint installed before the agent moved out of /usr/local/bin. Its
  # service is stopped now and its definition is only removed once the new
  # install is healthy, so a failure here leaves the old one to be restarted.
  current_version="$("$LEGACY_DEST" -print-version)" || { echo "error: installed agent version could not be read"; exit 1; }
  "$CANDIDATE" -check-upgrade-from "$current_version" || { echo "error: refusing agent downgrade"; exit 1; }
  MIGRATE_FROM="$LEGACY_DEST"
  echo "Moving the agent from $LEGACY_DEST to $DEST..."
  if detect_service_config; then
    SERVICE_EXISTED=1
    "$MIGRATE_FROM" -action stop >/dev/null 2>&1 || true
    snapshot_service_config
  fi
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
# The legacy service is removed only here, right before its replacement is
# written: until this point a failure can still hand the endpoint back to it.
if [ -n "$MIGRATE_FROM" ] && [ "$SERVICE_EXISTED" = "1" ]; then
  "$MIGRATE_FROM" -action uninstall >/dev/null 2>&1 ||
    { echo "error: could not remove the previous service definition"; exit 1; }
fi
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
if [ -n "$MIGRATE_FROM" ]; then
  rm -f "$MIGRATE_FROM" "${MIGRATE_FROM}.sig" "${MIGRATE_FROM}.update-etag"
  echo "Removed the previous agent at $MIGRATE_FROM."
fi
# macOS: desktop sessions run from a per-user LaunchAgent, not from the daemon.
# TCC grants are per user, so the sidecar must run AS the console user for an
# operator's "Allow" to mean anything; and TCC judges by the RESPONSIBLE process,
# so no Apple-signed helper (sudo, su) may sit in between, or tccd refuses to
# prompt at all. launchd starting the job in the user's session is the only way
# to satisfy both. The daemon fills in the relay URL and ticket through
# remote-session.json and kickstarts this label.
if [ "$OS" = "darwin" ]; then
  REMOTE_AGENT_PLIST=/Library/LaunchAgents/io.openrmm.remote.plist
  # Reinstall over a build that used the previous bundle identifier: drop its
  # job and its privacy rows, which would otherwise keep listing an allowed
  # application that nothing runs under any more.
  LEGACY_AGENT_PLIST=/Library/LaunchAgents/com.openrmm.remote.plist
  if [ -f "$LEGACY_AGENT_PLIST" ]; then
    LEGACY_UID="$(stat -f "%u" /dev/console 2>/dev/null || echo 0)"
    [ "$LEGACY_UID" != "0" ] &&
      launchctl bootout "gui/$LEGACY_UID/com.openrmm.remote" >/dev/null 2>&1 || true
    rm -f "$LEGACY_AGENT_PLIST"
    tccutil reset All com.openrmm.remote >/dev/null 2>&1 || true
    [ "$LEGACY_UID" != "0" ] &&
      launchctl asuser "$LEGACY_UID" tccutil reset All com.openrmm.remote >/dev/null 2>&1 || true
  fi
  cat > "$REMOTE_AGENT_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.openrmm.remote</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Library/Application Support/OpenRMM/RMMRemote.app/Contents/MacOS/rmm-remote</string>
    <string>-session</string>
    <string>${STATE_DIR}/remote-session.json</string>
  </array>
  <key>RunAtLoad</key><false/>
  <key>KeepAlive</key><false/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
PLIST
  chown root:wheel "$REMOTE_AGENT_PLIST"
  chmod 644 "$REMOTE_AGENT_PLIST"
  # Load it into the console user's session now; it is loaded automatically at
  # every later login. Failure is not fatal: nobody may be logged in yet.
  CONSOLE_UID="$(stat -f "%u" /dev/console 2>/dev/null || echo 0)"
  if [ "$CONSOLE_UID" != "0" ]; then
    launchctl bootout "gui/$CONSOLE_UID/io.openrmm.remote" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/$CONSOLE_UID" "$REMOTE_AGENT_PLIST" >/dev/null 2>&1 ||
      echo "note: could not load the remote LaunchAgent now; it loads at the next login"
  fi
  echo "Remote desktop LaunchAgent installed at $REMOTE_AGENT_PLIST"
fi

echo "Agent installed, connected, and healthy."
