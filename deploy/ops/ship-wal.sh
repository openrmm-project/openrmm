#!/usr/bin/env bash
# Copies the WAL archive offsite and prunes what is no longer needed.
#
# Two failure modes this guards against, both of which end with an unusable
# recovery: an archive that only exists on the host that just died, and an
# archive volume that fills the disk and stops Postgres from archiving at all.
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

BACKUP_DIR=${BACKUP_DIR:-/var/backups/openrmm}
BASEBACKUP_DIR=${BASEBACKUP_DIR:-$BACKUP_DIR/base}
WAL_ARCHIVE_DIR=${WAL_ARCHIVE_DIR:-/var/lib/postgresql/wal-archive}
WAL_PRUNE=${WAL_PRUNE:-true}

if [[ -n "${BACKUP_RCLONE_DEST:-}" ]]; then
  require_command rclone
  remote=${BACKUP_RCLONE_DEST%/}
  # Stream from inside the container: the archive lives on a named volume the
  # host does not necessarily have a path for.
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/rmm-wal.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  compose exec -T postgres tar -C "$WAL_ARCHIVE_DIR" -cf - . | tar -C "$tmp" -xf -
  rclone copy "$tmp" "$remote/wal" --ignore-existing
  printf 'shipped %s segments\n' "$(find "$tmp" -type f ! -name '.*' | wc -l)"
elif [[ "${BACKUP_OFFSITE_REQUIRED:-false}" == true ]]; then
  die "BACKUP_OFFSITE_REQUIRED=true but BACKUP_RCLONE_DEST is unset"
fi

# Prune only against the OLDEST base backup still retained: pruning to the newest
# would silently destroy the ability to recover to any point before it.
if [[ "$WAL_PRUNE" == true ]]; then
  oldest_start=""
  while IFS= read -r marker; do
    [[ -n "$marker" ]] || continue
    oldest_start=$(tr -d '[:space:]' < "$marker")
    break
  done < <(find "$BASEBACKUP_DIR" -name '*.startwal' -type f -print 2>/dev/null | sort)

  if [[ -z "$oldest_start" ]]; then
    printf 'no base backup marker found; skipping prune (WAL is useless without one)\n' >&2
    exit 0
  fi
  [[ "$oldest_start" =~ ^[0-9A-F]{24}$ ]] || die "invalid start-WAL marker: $oldest_start"
  compose exec -T postgres pg_archivecleanup "$WAL_ARCHIVE_DIR" "$oldest_start"
  printf 'pruned WAL older than %s\n' "$oldest_start"
fi
