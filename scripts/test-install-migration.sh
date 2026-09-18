#!/usr/bin/env bash
# Exercises install.sh against a stand-in agent: the upgrade path on every
# platform, and on macOS the move out of /usr/local/bin.
#
# The real agent needs a server to enrol against and a health report to be
# considered installed, which CI has no way to provide. What is under test here
# is the installer's own transaction — where it puts the binary, what it does
# with the previous service definition, and whether it refuses a directory it
# should not install into — so the agent is replaced by a script that answers
# the same flags.
#
# Installs into the real system paths, so run it only on a throwaway machine
# (a CI runner). Needs root.
set -euo pipefail

[ "$(id -u)" = "0" ] || { echo "error: run as root"; exit 1; }

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; esac
BIN="rmm-agent-${OS}-${ARCH}"
WORK="$(mktemp -d)"
SERVED="${WORK}/dl"
PORT=8731
STATE_DIR=/etc/rmm-agent

if [ "$OS" = "darwin" ]; then
  NEW_DEST=/usr/local/libexec/openrmm/rmm-agent
  LEGACY_DEST=/usr/local/bin/rmm-agent
  SERVICE_PATH=/Library/LaunchDaemons/rmm-agent.plist
else
  NEW_DEST=/usr/local/bin/rmm-agent
  LEGACY_DEST=""
  SERVICE_PATH=/etc/systemd/system/rmm-agent.service
fi

cleanup() {
  [ -z "${SERVER_PID:-}" ] || kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# write_stub_agent <path> <version>: a stand-in that answers every flag install.sh
# uses, and whose "service" is the definition file the installer looks for.
write_stub_agent() {
  cat > "$1" <<STUB
#!/usr/bin/env bash
set -eu
version="$2"
action=""
health=""
previous=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -print-version) echo "\$version"; exit 0;;
    -check-upgrade-from) previous="\$2"; shift 2;;
    -action) action="\$2"; shift 2;;
    -health-file) health="\$2"; shift 2;;
    *) shift;;
  esac
done
if [ -n "\$previous" ]; then exit 0; fi   # never a downgrade, for this test
case "\$action" in
  install) printf 'stub service for %s\n' "\$version" > "$SERVICE_PATH";;
  uninstall) rm -f "$SERVICE_PATH";;
  start)
    [ -z "\$health" ] || printf '{"version":"%s","agent_id":"stub"}' "\$version" > "\$health"
    [ -n "\$health" ] || printf '{"version":"%s","agent_id":"stub"}' "\$version" > "${STATE_DIR}/health.json"
    ;;
  stop) ;;
esac
exit 0
STUB
  chmod 0755 "$1"
}

echo "== preparing a published build of the stand-in agent =="
mkdir -p "$SERVED"
write_stub_agent "${SERVED}/${BIN}" "9.9.9"
(cd "$SERVED" && (sha256sum "$BIN" 2>/dev/null || shasum -a 256 "$BIN") > SHA256SUMS)
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$WORK" >/dev/null 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:${PORT}/dl/SHA256SUMS" >/dev/null 2>&1 && break
  sleep 0.2
done

echo "== an endpoint installed at the previous location =="
if [ -n "$LEGACY_DEST" ]; then
  mkdir -p "$(dirname "$LEGACY_DEST")" "$STATE_DIR"
  write_stub_agent "$LEGACY_DEST" "9.9.8"
  printf 'stub service for 9.9.8\n' > "$SERVICE_PATH"
else
  mkdir -p "$(dirname "$NEW_DEST")" "$STATE_DIR"
  write_stub_agent "$NEW_DEST" "9.9.8"
  printf 'stub service for 9.9.8\n' > "$SERVICE_PATH"
fi
# Enrolment state the install must carry across, so the endpoint comes back as
# the same device rather than a new one.
printf '{"agent_id":"stub-device","agent_token":"stub-token"}' > "${STATE_DIR}/state.json"
chmod 0600 "${STATE_DIR}/state.json"

echo "== running the installer =="
bash "${REPO_ROOT}/deploy/public/install.sh" \
  --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode

echo "== checking the result =="
fail=0
# check <description> <command...>: the command decides, so no $? is carried
# between statements where a later one could overwrite it.
check() {
  description="$1"; shift
  if "$@"; then echo "  ok: $description"; else echo "  FAIL: $description"; fail=1; fi
}
published_version_installed() { "$NEW_DEST" -print-version | grep -qx '9.9.9'; }
enrolment_survived() { grep -q 'stub-device' "${STATE_DIR}/state.json"; }
install_dir_owner() {
  if [ "$OS" = darwin ]; then stat -f%u "$(dirname "$NEW_DEST")"; else stat -c%u "$(dirname "$NEW_DEST")"; fi
}
install_dir_is_root() { [ "$(install_dir_owner)" = "0" ]; }

check "agent installed at $NEW_DEST" test -x "$NEW_DEST"
check "the published version is the one installed" published_version_installed
check "enrolment state survived the install" enrolment_survived
check "service definition written" test -f "$SERVICE_PATH"
if [ -n "$LEGACY_DEST" ]; then
  check "the binary at the previous location is gone" test ! -e "$LEGACY_DEST"
fi
check "the install directory belongs to root" install_dir_is_root

echo "== the installer must refuse a directory somebody else owns =="
# Only meaningful where the installer owns its directory outright; on Linux it
# installs into /usr/local/bin, which is shared with everything else on the host.
if [ "$OS" = "darwin" ]; then
  nobody_uid="$(id -u nobody 2>/dev/null || echo 65534)"
  chown "$nobody_uid" "$(dirname "$NEW_DEST")"
  if bash "${REPO_ROOT}/deploy/public/install.sh" \
      --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode >"${WORK}/refused.log" 2>&1; then
    echo "  FAIL: the installer accepted a directory owned by uid $nobody_uid"
    fail=1
  else
    check "refused with an ownership error" grep -q 'not root' "${WORK}/refused.log"
  fi
  chown 0 "$(dirname "$NEW_DEST")"
fi

echo "== uninstalling =="
bash "${REPO_ROOT}/deploy/public/uninstall.sh" >/dev/null
check "uninstall removed the agent" test ! -e "$NEW_DEST"
check "uninstall removed the service definition" test ! -e "$SERVICE_PATH"
check "uninstall removed the enrolment state" test ! -e "$STATE_DIR"

[ "$fail" = "0" ] || { echo "installer migration test FAILED"; exit 1; }
echo "installer migration test passed"
