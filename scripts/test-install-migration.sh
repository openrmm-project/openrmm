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
# The first install asks for the ad-hoc remote-desktop bundle. The choice only
# means something on macOS; elsewhere the same command line must be accepted and
# ignored, so a mixed fleet can share one.
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
  --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode --macos-bundle adhoc

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
if [ "$OS" = "darwin" ]; then
  check "the remote-desktop bundle choice was recorded" grep -qx adhoc "${STATE_DIR}/remote-bundle"
else
  check "no remote-desktop bundle choice outside macOS" test ! -e "${STATE_DIR}/remote-bundle"
fi
# Refused before anything is touched, so a typo cannot quietly install the
# variant the operator was trying to avoid.
bundle_typo_refused() {
  ! bash "${REPO_ROOT}/deploy/public/install.sh" \
      --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode --macos-bundle adhock \
      >"${WORK}/typo.log" 2>&1 &&
    grep -q "must be 'signed' or 'adhoc'" "${WORK}/typo.log"
}
check "an unknown bundle variant is refused" bundle_typo_refused

echo "== a failed install must hand the endpoint back =="
# The failure that matters in practice: the replacement is in place and the
# previous service definition has already been removed when something goes
# wrong. What the endpoint must not be left with is no agent and no service.
write_stub_agent "${SERVED}/${BIN}" "9.9.10"
# A start that fails is the realistic form of "never became healthy" without
# waiting out the installer's health timeout.
sed -i.bak 's/^  start)$/  start) exit 1;;\nstart_unused)/' "${SERVED}/${BIN}"
(cd "$SERVED" && (sha256sum "$BIN" 2>/dev/null || shasum -a 256 "$BIN") > SHA256SUMS)
if bash "${REPO_ROOT}/deploy/public/install.sh" \
    --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode >"${WORK}/failed.log" 2>&1; then
  echo "  FAIL: an install whose agent cannot start reported success"
  fail=1
else
  check "the previous agent is still installed" test -x "$NEW_DEST"
  check "it is the version that was working" published_version_installed
  check "the service definition is back" test -f "$SERVICE_PATH"
  check "the enrolment state is intact" enrolment_survived
fi

echo "== the installer must take back a directory somebody else owns =="
# Only meaningful where the installer owns its directory outright; on Linux it
# installs into /usr/local/bin, which is shared with everything else on the host.
#
# The installer does not refuse a squatted directory: running as root it claims
# it back (chown 0, chmod 755) and only refuses when that repair does not hold.
# What is asserted here is the end state, because that is the security property
# -- the agent must never execute from a directory another account controls.
if [ "$OS" = "darwin" ]; then
  # By name, not by id: macOS gives nobody the uid -2, which BSD chown reads as
  # 4294967294 and rejects as an illegal user name. The name works on both.
  chown nobody "$(dirname "$NEW_DEST")"
  chmod 777 "$(dirname "$NEW_DEST")"
  # The served build is still the one that cannot start, left by the case above,
  # so this install is expected to fail. The repair happens before any of that
  # and is not undone by the rollback, which is exactly what makes it testable
  # without republishing a healthy build.
  bash "${REPO_ROOT}/deploy/public/install.sh" \
    --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode >"${WORK}/refused.log" 2>&1 || true
  check "the install directory was taken back by root" install_dir_is_root
  check "and is no longer writable by anyone else" \
    test "$(stat -f%Lp "$(dirname "$NEW_DEST")")" = 755
  chown 0 "$(dirname "$NEW_DEST")"
fi

echo "== the installer must refuse a directory reached through somebody else's =="
# A planted symbolic link: the agent's directory now leads into a directory
# another account controls. Taking ownership of the link's target would not
# help — that account can still rename it away — so the install must stop.
if [ "$OS" = "darwin" ]; then
  owned="$(dirname "$NEW_DEST")"
  squat="${WORK}/home"
  mkdir -p "${squat}/openrmm"
  chown -R nobody "$squat"
  mv "$owned" "${owned}.real"
  ln -s "${squat}/openrmm" "$owned"
  if bash "${REPO_ROOT}/deploy/public/install.sh" \
      --server "http://127.0.0.1:${PORT}" --token stub-token --lab-mode >"${WORK}/symlink.log" 2>&1; then
    echo "  FAIL: the installer followed a link into another account's directory"
    fail=1
  else
    check "refused because of the directory above it" grep -q 'above' "${WORK}/symlink.log"
  fi
  check "nothing was installed there" test ! -e "${squat}/openrmm/rmm-agent"
  check "and it was not taken over by root" test "$(stat -f%u "${squat}/openrmm")" != 0
  rm -f "$owned"
  mv "${owned}.real" "$owned"
fi

echo "== uninstalling =="
# What an agent leaves beside itself once it has run for a while: its log, a
# rollback copy from a self-update, and what fetching the remote sidecar
# leaves (the sidecar itself and its log only on Linux; on macOS the sidecar
# is the bundle).
AGENT_DIR="$(dirname "$NEW_DEST")"
LEFTOVERS="agent.log agent.log.1 rmm-agent.old rmm-remote.etag rmm-remote.new"
[ "$OS" = "darwin" ] || LEFTOVERS="$LEFTOVERS rmm-remote rmm-remote.log"
for name in $LEFTOVERS; do printf 'x' > "${AGENT_DIR}/${name}"; done
mkdir -p "${AGENT_DIR}/rmm-remote.new.stage"
# Not ours: only removed when an agent sat beside it, so this must survive.
if [ "$OS" = "darwin" ]; then printf 'x' > /usr/local/bin/agent.log; fi
bash "${REPO_ROOT}/deploy/public/uninstall.sh" >/dev/null
leftovers_gone() {
  for name in $LEFTOVERS rmm-remote.new.stage; do
    [ ! -e "${AGENT_DIR}/${name}" ] || { echo "    still there: ${AGENT_DIR}/${name}"; return 1; }
  done
}
check "uninstall removed the agent's log, rollback copy and sidecar files" leftovers_gone
if [ "$OS" = "darwin" ]; then
  check "and with nothing left in it, the agent's directory" test ! -e "$AGENT_DIR"
  check "an agent.log in /usr/local/bin with no agent beside it was left alone" test -e /usr/local/bin/agent.log
  rm -f /usr/local/bin/agent.log
fi
check "uninstall removed the agent" test ! -e "$NEW_DEST"
check "uninstall removed the service definition" test ! -e "$SERVICE_PATH"
check "uninstall removed the enrolment state" test ! -e "$STATE_DIR"

[ "$fail" = "0" ] || { echo "installer migration test FAILED"; exit 1; }
echo "installer migration test passed"
