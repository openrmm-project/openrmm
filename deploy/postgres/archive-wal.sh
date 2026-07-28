#!/bin/sh
# archive_command: runs INSIDE the postgres container, once per completed WAL
# segment. Postgres treats a non-zero exit as "not archived" and retries, so the
# only unforgivable behaviour here is exiting 0 without a durable copy.
set -eu

src=$1   # %p — path relative to PGDATA
name=$2  # %f — segment file name
dest_dir=${WAL_ARCHIVE_DIR:-/var/lib/postgresql/wal-archive}

[ -d "$dest_dir" ] || exit 1

# Never overwrite: a segment name reappearing with different content means a
# timeline problem, and silently clobbering it would corrupt the archive.
if [ -f "$dest_dir/$name" ]; then
  if cmp -s "$src" "$dest_dir/$name"; then
    exit 0   # already archived, identical — retry after a crash is fine
  fi
  echo "archive-wal: refusing to overwrite $name with different content" >&2
  exit 1
fi

tmp="$dest_dir/.$name.tmp.$$"
trap 'rm -f "$tmp"' EXIT

cp "$src" "$tmp"
# fsync the copy before publishing it under its final name, so a host crash
# cannot leave a truncated segment that looks complete.
if command -v sync >/dev/null 2>&1; then sync "$tmp" 2>/dev/null || sync; fi
mv "$tmp" "$dest_dir/$name"
trap - EXIT
