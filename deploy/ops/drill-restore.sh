#!/usr/bin/env bash
# Timed recovery drill — produces the RTO number.
#
# verify-backup.sh proves an archive is readable. This proves you can rebuild a
# working database from it and tells you how long that takes, which is the only
# honest way to fill in "RTO = X" in docs/19. It runs in an isolated Compose
# project against a scratch volume and never touches production.
#
#   deploy/ops/drill-restore.sh                       # newest logical dump
#   deploy/ops/drill-restore.sh /path/to/rmm-TS.dump.age
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

BACKUP_DIR=${BACKUP_DIR:-/var/backups/openrmm}
POSTGRES_USER=${POSTGRES_USER:-rmm}
POSTGRES_DB=${POSTGRES_DB:-rmm}
validate_identifier POSTGRES_USER "$POSTGRES_USER"
validate_identifier POSTGRES_DB "$POSTGRES_DB"
DRILL_LOG=${DRILL_LOG:-$BACKUP_DIR/drill.log}

archive=${1:-}
if [[ -z "$archive" ]]; then
  archive=$(find "$BACKUP_DIR" -maxdepth 1 -name 'rmm-*.dump*' ! -name '*.sha256' -type f -print 2>/dev/null | sort | tail -n1)
  [[ -n "$archive" ]] || die "no backup found in $BACKUP_DIR"
fi
[[ -f "$archive" ]] || die "backup not found: $archive"

image=${POSTGRES_IMAGE:-postgres:16-alpine}
container="rmm-drill-$$"
plain=$(mktemp "${TMPDIR:-/tmp}/rmm-drill.XXXXXX")
cleanup() {
  rm -f "$plain"
  docker rm --force "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

printf 'drill: %s\n' "$archive" >&2
start=$(date +%s)

# Everything a real recovery does, in order, so the clock covers the real work:
# decrypt, verify, provision a clean server, restore, and prove it answers.
verify_checksum "$archive"
materialize_archive "$archive" "$plain"

docker run -d --name "$container" \
  -e POSTGRES_PASSWORD=drill -e POSTGRES_USER="$POSTGRES_USER" -e POSTGRES_DB="$POSTGRES_DB" \
  "$image" >/dev/null

for _ in $(seq 1 120); do
  docker exec "$container" pg_isready -q -U "$POSTGRES_USER" -d "$POSTGRES_DB" && break
  sleep 1
done
docker exec "$container" pg_isready -q -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  || die "drill server never became ready"

docker exec -i "$container" pg_restore --exit-on-error --no-owner --no-acl \
  --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" < "$plain"

# A restore that leaves an empty schema still "succeeds"; assert real content.
agents=$(docker exec "$container" psql --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
  --no-psqlrc --tuples-only --no-align --command='SELECT count(*) FROM agents' | tr -d '[:space:]')
tables=$(docker exec "$container" psql --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" \
  --no-psqlrc --tuples-only --no-align \
  --command="SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" | tr -d '[:space:]')
[[ "$tables" -gt 0 ]] || die "restored database has no tables"

elapsed=$(( $(date +%s) - start ))
mkdir -p "$(dirname "$DRILL_LOG")"
printf '%s archive=%s elapsed_seconds=%s tables=%s agents=%s\n' \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$archive" "$elapsed" "$tables" "$agents" >> "$DRILL_LOG"
chmod 0600 "$DRILL_LOG"

printf 'drill OK in %ss (%s tables, %s agents) — logged to %s\n' "$elapsed" "$tables" "$agents" "$DRILL_LOG"
printf 'NOTE: this measures database recovery only. Full RTO also includes host\n' >&2
printf '      provisioning, secret restore and stack startup — time those once by hand.\n' >&2
