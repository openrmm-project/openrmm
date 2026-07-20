#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

BACKUP_DIR=${BACKUP_DIR:-/var/backups/openrmm}
BACKUP_REQUIRE_ENCRYPTION=${BACKUP_REQUIRE_ENCRYPTION:-true}
BACKUP_OFFSITE_REQUIRED=${BACKUP_OFFSITE_REQUIRED:-false}
POSTGRES_USER=${POSTGRES_USER:-rmm}
POSTGRES_DB=${POSTGRES_DB:-rmm}
validate_identifier POSTGRES_USER "$POSTGRES_USER"
validate_identifier POSTGRES_DB "$POSTGRES_DB"

if [[ "$BACKUP_REQUIRE_ENCRYPTION" == true && -z "${BACKUP_AGE_RECIPIENT:-}" ]]; then
  die "BACKUP_AGE_RECIPIENT is required when BACKUP_REQUIRE_ENCRYPTION=true"
fi
if [[ "$BACKUP_OFFSITE_REQUIRED" == true && -z "${BACKUP_RCLONE_DEST:-}" ]]; then
  die "BACKUP_RCLONE_DEST is required when BACKUP_OFFSITE_REQUIRED=true"
fi

mkdir -p -- "$BACKUP_DIR"
chmod 0700 "$BACKUP_DIR"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
plain=$(mktemp "$BACKUP_DIR/.rmm-${timestamp}.XXXXXX")
suffix=${plain##*.}
cleanup() { rm -f "$plain" "${encrypted_tmp:-}"; }
trap cleanup EXIT

compose exec -T postgres pg_dump \
  --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" --format=custom --compress=9 \
  --no-owner --no-acl > "$plain"
verify_archive_format "$plain"

if [[ -n "${BACKUP_AGE_RECIPIENT:-}" ]]; then
  require_command age
  output="$BACKUP_DIR/rmm-${timestamp}-${suffix}.dump.age"
  encrypted_tmp="${output}.tmp.$$"
  age --recipient "$BACKUP_AGE_RECIPIENT" --output "$encrypted_tmp" "$plain"
  mv "$encrypted_tmp" "$output"
  encrypted_tmp=
else
  output="$BACKUP_DIR/rmm-${timestamp}-${suffix}.dump"
  mv "$plain" "$output"
  plain=
fi

chmod 0600 "$output"
write_checksum "$output"

if [[ -n "${BACKUP_RCLONE_DEST:-}" ]]; then
  require_command rclone
  remote=${BACKUP_RCLONE_DEST%/}
  rclone copyto "$output" "$remote/$(basename "$output")"
  rclone copyto "${output}.sha256" "$remote/$(basename "${output}.sha256")"
fi

printf '%s\n' "$output"
