#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

[[ $# -eq 1 ]] || die "usage: $0 /path/to/rmm-TIMESTAMP.dump[.age]"
archive=$1
[[ -f "$archive" && ! -L "$archive" ]] || die "backup must be a regular, non-symlink file: $archive"
verify_checksum "$archive"

plain=$(mktemp "${TMPDIR:-/tmp}/rmm-verify.XXXXXX")
verify_db="rmm_verify_$(date -u +%s)_$$"
POSTGRES_USER=${POSTGRES_USER:-rmm}
validate_identifier POSTGRES_USER "$POSTGRES_USER"
database_created=false
cleanup() {
  rm -f "$plain"
  if [[ "$database_created" == true ]]; then
    compose exec -T postgres dropdb --if-exists --force --username="$POSTGRES_USER" "$verify_db" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

materialize_archive "$archive" "$plain"
verify_archive_format "$plain"

if [[ "${VERIFY_FULL_RESTORE:-true}" == true ]]; then
  compose exec -T postgres createdb --username="$POSTGRES_USER" --owner="$POSTGRES_USER" --template=template0 "$verify_db"
  database_created=true
  compose exec -T postgres pg_restore --exit-on-error --no-owner --no-acl \
    --username="$POSTGRES_USER" --dbname="$verify_db" < "$plain" >/dev/null
  compose exec -T postgres psql --username="$POSTGRES_USER" --dbname="$verify_db" \
    --no-psqlrc --tuples-only --command='SELECT 1' >/dev/null
fi

printf 'verified: %s\n' "$archive"
