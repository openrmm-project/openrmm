#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

[[ $# -eq 1 ]] || die "usage: $0 /path/to/rmm-TIMESTAMP.dump[.age]"
archive=$1
[[ -f "$archive" && ! -L "$archive" ]] || die "backup must be a regular, non-symlink file: $archive"
verify_checksum "$archive"
source_archive=$archive

BACKUP_DIR=${BACKUP_DIR:-/var/backups/openrmm}
RESTORE_LOG_FILE=${RESTORE_LOG_FILE:-$BACKUP_DIR/restore.log}
RESTORE_WAIT_TIMEOUT=${RESTORE_WAIT_TIMEOUT:-180}
POSTGRES_USER=${POSTGRES_USER:-rmm}
POSTGRES_DB=${POSTGRES_DB:-rmm}
validate_identifier POSTGRES_USER "$POSTGRES_USER"
validate_identifier POSTGRES_DB "$POSTGRES_DB"
[[ "$RESTORE_WAIT_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die "RESTORE_WAIT_TIMEOUT must be a positive integer"

read -r -a writer_services <<< "${RESTORE_WRITER_SERVICES:-frontend server}"
((${#writer_services[@]} > 0)) || die "RESTORE_WRITER_SERVICES cannot be empty"
for service in "${writer_services[@]}"; do
  [[ "$service" =~ ^[A-Za-z0-9_-]+$ ]] || die "invalid writer service: $service"
done

mkdir -p "$BACKUP_DIR" "$(dirname "$RESTORE_LOG_FILE")"
chmod 0700 "$BACKUP_DIR"
touch "$RESTORE_LOG_FILE"
chmod 0600 "$RESTORE_LOG_FILE"

restore_log() {
  printf '%s event=%s archive=%s safety_backup=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$source_archive" "${safety_backup:-none}" >> "$RESTORE_LOG_FILE"
}

incoming=$(mktemp "${TMPDIR:-/tmp}/rmm-restore-incoming.XXXXXX")
rollback=$(mktemp "${TMPDIR:-/tmp}/rmm-restore-rollback.XXXXXX")
writers_stopped=false
database_replaced=false
safety_backup=

on_exit() {
  local rc=$? rollback_rc=0
  trap - EXIT
  set +e
  if ((rc != 0)); then
    if [[ "$database_replaced" == true && -s "$rollback" ]]; then
      restore_log rollback_started
      if replace_database "$rollback" && compose up -d --wait --wait-timeout "$RESTORE_WAIT_TIMEOUT" "${writer_services[@]}"; then
        writers_stopped=false
        restore_log rollback_succeeded
        printf 'restore failed; database automatically recovered from %s\n' "$safety_backup" >&2
      else
        rollback_rc=$?
        restore_log rollback_failed
        printf 'CRITICAL: restore and automatic rollback failed; writers remain stopped\n' >&2
      fi
    elif [[ "$writers_stopped" == true ]]; then
      if compose up -d --wait --wait-timeout "$RESTORE_WAIT_TIMEOUT" "${writer_services[@]}"; then
        writers_stopped=false
        restore_log writers_restarted
      else
        restore_log writers_restart_failed
      fi
    fi
  fi
  rm -f "$incoming" "$rollback"
  ((rollback_rc == 0)) || rc=$rollback_rc
  exit "$rc"
}
trap on_exit EXIT

materialize_archive "$archive" "$incoming"
verify_archive_format "$incoming"

if [[ "${RESTORE_CONFIRM:-}" != "$POSTGRES_DB" ]]; then
  [[ -t 0 ]] || die "set RESTORE_CONFIRM=$POSTGRES_DB for a non-interactive destructive restore"
  printf 'Type %s to replace the production database: ' "$POSTGRES_DB" >&2
  read -r confirmation
  [[ "$confirmation" == "$POSTGRES_DB" ]] || die "restore cancelled"
fi

# Writers stop before the safety backup so it captures a quiescent application
# state. Any failure before database replacement automatically restarts them.
writers_stopped=true
compose stop "${writer_services[@]}"

safety_backup=$("$OPS_DIR/backup.sh")
printf 'safety backup: %s\n' "$safety_backup" >&2
restore_log safety_backup_created
verify_checksum "$safety_backup"
materialize_archive "$safety_backup" "$rollback"
verify_archive_format "$rollback"

database_replaced=true
restore_log restore_started
replace_database "$incoming"
compose up -d --wait --wait-timeout "$RESTORE_WAIT_TIMEOUT" "${writer_services[@]}"
writers_stopped=false
database_replaced=false
restore_log restore_succeeded
printf 'restored and healthy: %s\n' "$archive"
