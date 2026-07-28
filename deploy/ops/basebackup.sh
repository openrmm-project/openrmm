#!/usr/bin/env bash
# Physical base backup (pg_basebackup) — the anchor point for PITR.
#
# This is NOT a replacement for backup.sh. The two answer different questions:
#   backup.sh     logical dump, portable across major versions, restores one
#                 database, cannot replay WAL.
#   basebackup.sh byte-level copy of the cluster, tied to this major version,
#                 and the ONLY thing archived WAL can be replayed on top of.
# Keep both: the dump survives a corrupt cluster, the base backup gives you a
# recovery point measured in minutes instead of a day.
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

BACKUP_DIR=${BACKUP_DIR:-/var/backups/openrmm}
BASEBACKUP_DIR=${BASEBACKUP_DIR:-$BACKUP_DIR/base}
BACKUP_REQUIRE_ENCRYPTION=${BACKUP_REQUIRE_ENCRYPTION:-true}
BACKUP_OFFSITE_REQUIRED=${BACKUP_OFFSITE_REQUIRED:-false}
POSTGRES_USER=${POSTGRES_USER:-rmm}
validate_identifier POSTGRES_USER "$POSTGRES_USER"

if [[ "$BACKUP_REQUIRE_ENCRYPTION" == true && -z "${BACKUP_AGE_RECIPIENT:-}" ]]; then
  die "BACKUP_AGE_RECIPIENT is required when BACKUP_REQUIRE_ENCRYPTION=true"
fi
if [[ "$BACKUP_OFFSITE_REQUIRED" == true && -z "${BACKUP_RCLONE_DEST:-}" ]]; then
  die "BACKUP_RCLONE_DEST is required when BACKUP_OFFSITE_REQUIRED=true"
fi

mkdir -p -- "$BASEBACKUP_DIR"
chmod 0700 "$BASEBACKUP_DIR"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
plain=$(mktemp "$BASEBACKUP_DIR/.base-${timestamp}.XXXXXX")
cleanup() { rm -f "$plain" "${encrypted_tmp:-}"; }
trap cleanup EXIT

# -X none: WAL is not bundled into the tar. The archive is the source of truth
# for WAL, and bundling would hide a broken archive_command until recovery day.
# --checkpoint=fast avoids waiting out the checkpoint interval.
compose exec -T postgres pg_basebackup \
  --username="$POSTGRES_USER" --format=tar --gzip --compress=6 \
  --wal-method=none --checkpoint=fast --pgdata=- > "$plain"

[[ -s "$plain" ]] || die "pg_basebackup produced an empty archive"
# A gzip tar that does not list is a backup you only discover is bad in a crisis.
gzip -t "$plain" 2>/dev/null || die "base backup failed gzip integrity check"

if [[ -n "${BACKUP_AGE_RECIPIENT:-}" ]]; then
  require_command age
  output="$BASEBACKUP_DIR/base-${timestamp}.tar.gz.age"
  encrypted_tmp="${output}.tmp.$$"
  age --recipient "$BACKUP_AGE_RECIPIENT" --output "$encrypted_tmp" "$plain"
  mv "$encrypted_tmp" "$output"
  encrypted_tmp=
else
  output="$BASEBACKUP_DIR/base-${timestamp}.tar.gz"
  mv "$plain" "$output"
  plain=
fi

chmod 0600 "$output"
write_checksum "$output"

# Record where recovery must start replaying from. ship-wal.sh prunes against
# this file, and without it pruning cannot know which segments are still needed.
compose exec -T postgres psql --username="$POSTGRES_USER" --dbname=postgres \
  --no-psqlrc --tuples-only --no-align \
  --command="SELECT pg_walfile_name(pg_current_wal_lsn())" \
  | tr -d '[:space:]' > "${output}.startwal"
chmod 0600 "${output}.startwal"

if [[ -n "${BACKUP_RCLONE_DEST:-}" ]]; then
  require_command rclone
  remote=${BACKUP_RCLONE_DEST%/}
  for file in "$output" "${output}.sha256" "${output}.startwal"; do
    rclone copyto "$file" "$remote/base/$(basename "$file")"
  done
fi

printf '%s\n' "$output"
