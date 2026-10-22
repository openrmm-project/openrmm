#!/usr/bin/env bash
# Seeds the warm standby: creates the replication role and slot on the primary,
# then takes a base backup into the standby volume with recovery configured.
# Run once; after this the standby follows the primary on its own.
#
#   STANDBY_REPL_PASSWORD_FILE=./secrets/replication_password \
#     deploy/ops/standby-bootstrap.sh
set -Eeuo pipefail
umask 077

# shellcheck source=deploy/ops/_common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/_common.sh"
load_env_file
require_command docker

POSTGRES_USER=${POSTGRES_USER:-rmm}
validate_identifier POSTGRES_USER "$POSTGRES_USER"
SLOT=${STANDBY_SLOT:-standby1}
validate_identifier STANDBY_SLOT "$SLOT"
PROJECT=${COMPOSE_PROJECT_NAME:-openrmm}
STANDBY_VOLUME="${PROJECT}_rmm_pgdata_standby"
PRIMARY_HOST=${STANDBY_PRIMARY_HOST:-postgres}

password_file=${STANDBY_REPL_PASSWORD_FILE:-$DEPLOY_DIR/secrets/replication_password}
[[ -r "$password_file" ]] || die "replication password file not readable: $password_file
create one with: openssl rand -base64 33 > $password_file && chmod 600 $password_file"
password=$(tr -d '\r\n' < "$password_file")
[[ ${#password} -ge 16 ]] || die "replication password must be at least 16 characters"

image=$(postgres_image)
network="${PROJECT}_db"

printf 'creating replication role and slot on the primary\n' >&2
# REPLICATION but no table privileges: a leaked standby credential can stream
# WAL, not read application rows directly.
compose exec -T postgres psql --username="$POSTGRES_USER" --dbname=postgres --no-psqlrc \
  --set=pw="$password" --command \
  "DO \$\$ BEGIN
     IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='replicator') THEN
       EXECUTE format('CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD %L', :'pw');
     ELSE
       EXECUTE format('ALTER ROLE replicator WITH REPLICATION LOGIN PASSWORD %L', :'pw');
     END IF;
   END \$\$;" >/dev/null

compose exec -T postgres psql --username="$POSTGRES_USER" --dbname=postgres --no-psqlrc --tuples-only \
  --command "SELECT pg_create_physical_replication_slot('$SLOT') WHERE NOT EXISTS
             (SELECT 1 FROM pg_replication_slots WHERE slot_name='$SLOT')" >/dev/null

if docker volume inspect "$STANDBY_VOLUME" >/dev/null 2>&1; then
  contents=$(docker run --rm -v "$STANDBY_VOLUME":/d --entrypoint sh "$image" -c 'ls -A /d | head -n1')
  [[ -z "$contents" ]] || die "standby volume $STANDBY_VOLUME is not empty; remove it first to re-seed"
else
  docker volume create "$STANDBY_VOLUME" >/dev/null
fi

printf 'taking base backup into %s\n' "$STANDBY_VOLUME" >&2
# --write-recovery-conf writes primary_conninfo and standby.signal, which is what
# makes the copy follow the primary instead of starting as an independent server.
docker run --rm --network "$network" --user 70:70 \
  -e PGPASSWORD="$password" \
  -v "$STANDBY_VOLUME":/var/lib/postgresql/data \
  --entrypoint pg_basebackup "$image" \
  --host="$PRIMARY_HOST" --username=replicator --pgdata=/var/lib/postgresql/data \
  --wal-method=stream --write-recovery-conf --slot="$SLOT" --checkpoint=fast

printf 'standby seeded. Start it with:\n' >&2
printf '  docker compose -f %s -f %s up -d postgres-standby\n' \
  "$(basename "$COMPOSE_FILE")" "docker-compose.standby.yml" >&2
printf 'Then confirm streaming:\n' >&2
printf "  compose exec postgres psql -U %s -d postgres -c \"SELECT client_addr, state, replay_lag FROM pg_stat_replication\"\n" \
  "$POSTGRES_USER" >&2
