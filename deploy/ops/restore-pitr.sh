#!/usr/bin/env bash
# Destructive point-in-time recovery: restores a physical base backup and
# replays archived WAL up to a chosen instant.
#
# Use this when you need a recovery point closer than the last logical dump —
# a bad migration, a mistaken mass delete, a corrupt volume. For a clean
# single-database rollback, restore.sh is simpler and version-portable.
#
#   RESTORE_CONFIRM=rmm PITR_TARGET_TIME='2026-07-27 03:20:00+00' \
#     deploy/ops/restore-pitr.sh /var/backups/openrmm/base/base-TIMESTAMP.tar.gz.age
#
# Omit PITR_TARGET_TIME to replay everything available (recover as far forward
# as the archive allows).
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

[[ $# -eq 1 ]] || die "usage: $0 /path/to/base-TIMESTAMP.tar.gz[.age]"
archive=$1
[[ -f "$archive" && ! -L "$archive" ]] || die "base backup must be a regular file: $archive"
verify_checksum "$archive"

POSTGRES_DB=${POSTGRES_DB:-rmm}
POSTGRES_USER=${POSTGRES_USER:-rmm}
validate_identifier POSTGRES_DB "$POSTGRES_DB"
validate_identifier POSTGRES_USER "$POSTGRES_USER"
WAL_ARCHIVE_DIR=${WAL_ARCHIVE_DIR:-/var/lib/postgresql/wal-archive}
RESTORE_WAIT_TIMEOUT=${RESTORE_WAIT_TIMEOUT:-600}

if [[ -n "${PITR_TARGET_TIME:-}" ]]; then
  # Rejected early: a malformed target silently becomes "replay everything",
  # which is the opposite of what someone recovering from a bad write wants.
  [[ "$PITR_TARGET_TIME" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}[[:space:]T][0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]+)?([+-][0-9]{2}(:?[0-9]{2})?|Z)?$ ]] \
    || die "PITR_TARGET_TIME must look like '2026-07-27 03:20:00+00'"
fi

if [[ "${RESTORE_CONFIRM:-}" != "$POSTGRES_DB" ]]; then
  [[ -t 0 ]] || die "set RESTORE_CONFIRM=$POSTGRES_DB for a non-interactive destructive restore"
  printf 'Type %s to REPLACE the entire cluster from %s: ' "$POSTGRES_DB" "$(basename "$archive")" >&2
  read -r confirmation
  [[ "$confirmation" == "$POSTGRES_DB" ]] || die "restore cancelled"
fi

plain=$(mktemp "${TMPDIR:-/tmp}/rmm-base.XXXXXX")
trap 'rm -f "$plain"' EXIT
materialize_archive "$archive" "$plain"
gzip -t "$plain" 2>/dev/null || die "base backup failed gzip integrity check"

printf 'stopping the stack\n' >&2
compose down

# The WAL archive lives on its own volume and must survive this: it is what the
# replay reads. Only the data volume is replaced.
data_volume=$(compose config --format json | grep -o '"rmm_pgdata"' >/dev/null 2>&1 && echo "${COMPOSE_PROJECT_NAME:-openrmm}_rmm_pgdata" || echo "${COMPOSE_PROJECT_NAME:-openrmm}_rmm_pgdata")
wal_volume="${COMPOSE_PROJECT_NAME:-openrmm}_rmm_pgwal"
docker volume inspect "$wal_volume" >/dev/null || die "WAL archive volume $wal_volume not found — PITR cannot replay without it"

printf 'replacing the data volume %s\n' "$data_volume" >&2
docker volume rm --force "$data_volume" >/dev/null
docker volume create "$data_volume" >/dev/null

# Unpack and arm recovery inside a throwaway container that shares both volumes,
# so file ownership matches the postgres image's uid without assuming host uids.
image=$(postgres_image)

recovery_conf="restore_command = 'cp ${WAL_ARCHIVE_DIR}/%f %p'\nrecovery_target_action = 'promote'\n"
if [[ -n "${PITR_TARGET_TIME:-}" ]]; then
  recovery_conf+="recovery_target_time = '${PITR_TARGET_TIME}'\n"
fi

docker run --rm -i \
  -v "$data_volume":/var/lib/postgresql/data \
  -v "$wal_volume":"$WAL_ARCHIVE_DIR" \
  --entrypoint sh "$image" -c "
    set -e
    tar -C /var/lib/postgresql/data -xzf -
    printf '%b' \"$recovery_conf\" >> /var/lib/postgresql/data/postgresql.auto.conf
    touch /var/lib/postgresql/data/recovery.signal
    chown -R 70:70 /var/lib/postgresql/data
    chmod 0700 /var/lib/postgresql/data
  " < "$plain"

printf 'starting postgres for replay\n' >&2
compose up -d --wait --wait-timeout "$RESTORE_WAIT_TIMEOUT" postgres

# Recovery ends when the server leaves recovery mode; until then the app must
# stay down, or it will read a half-replayed database.
for _ in $(seq 1 "$RESTORE_WAIT_TIMEOUT"); do
  state=$(compose exec -T postgres psql --username="$POSTGRES_USER" --dbname=postgres \
    --no-psqlrc --tuples-only --no-align --command='SELECT pg_is_in_recovery()' 2>/dev/null | tr -d '[:space:]' || true)
  [[ "$state" == "f" ]] && break
  sleep 1
done
[[ "$state" == "f" ]] || die "cluster is still in recovery after ${RESTORE_WAIT_TIMEOUT}s — inspect: compose logs postgres"

printf 'starting the rest of the stack\n' >&2
compose up -d --wait --wait-timeout "$RESTORE_WAIT_TIMEOUT"
printf 'recovered from %s%s\n' "$archive" "${PITR_TARGET_TIME:+ to $PITR_TARGET_TIME}"
