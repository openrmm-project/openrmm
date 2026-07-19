#!/usr/bin/env bash
set -Eeuo pipefail

OPS_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
DEPLOY_DIR=$(cd -- "$OPS_DIR/.." && pwd)
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.prod.yml}
ENV_FILE=${ENV_FILE:-$DEPLOY_DIR/.env}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

validate_identifier() {
  [[ "$2" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "$1 must be a SQL identifier"
}

# Read simple Compose-style KEY=VALUE entries without evaluating shell code.
load_env_file() {
  [[ -f "$ENV_FILE" ]] || die "environment file not found: $ENV_FILE"
  local line key value
  while IFS= read -r line || [[ -n "$line" ]]; do
    line=${line%$'\r'}
    [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
    [[ "$line" == *"="* ]] || continue
    key=${line%%=*}
    value=${line#*=}
    key=${key//[[:space:]]/}
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    if [[ "$value" =~ ^\"(.*)\"$ || "$value" =~ ^\'(.*)\'$ ]]; then
      value=${BASH_REMATCH[1]}
    fi
    if [[ -z "${!key+x}" ]]; then
      printf -v "$key" '%s' "$value"
      export "${key?}"
    fi
  done < "$ENV_FILE"
}

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

checksum_value() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | { read -r hash _; printf '%s\n' "$hash"; }
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | { read -r hash _; printf '%s\n' "$hash"; }
  else
    die "required command not found: sha256sum or shasum"
  fi
}

write_checksum() {
  printf '%s  %s\n' "$(checksum_value "$1")" "$(basename "$1")" > "${1}.sha256"
  chmod 0600 "${1}.sha256"
}

verify_checksum() {
  local archive=$1 sidecar="${1}.sha256" expected actual
  [[ -f "$sidecar" ]] || die "checksum file not found: $sidecar"
  read -r expected _ < "$sidecar"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "invalid checksum file: $sidecar"
  actual=$(checksum_value "$archive")
  [[ "$actual" == "$expected" ]] || die "checksum mismatch: $archive"
}

materialize_archive() {
  local source=$1 destination=$2
  if [[ "$source" == *.age ]]; then
    require_command age
    [[ -n "${BACKUP_AGE_IDENTITY_FILE:-}" ]] || die "BACKUP_AGE_IDENTITY_FILE is required for encrypted archives"
    [[ -r "$BACKUP_AGE_IDENTITY_FILE" ]] || die "age identity is not readable: $BACKUP_AGE_IDENTITY_FILE"
    age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" "$source" > "$destination"
  else
    cp "$source" "$destination"
  fi
  chmod 0600 "$destination"
}

verify_archive_format() {
  local archive=$1
  compose exec -T postgres pg_restore --list < "$archive" >/dev/null
}

replace_database() {
  local archive=$1 user=${POSTGRES_USER:-rmm} database=${POSTGRES_DB:-rmm}
  validate_identifier POSTGRES_USER "$user"
  validate_identifier POSTGRES_DB "$database"
  compose exec -T postgres dropdb --if-exists --force --username="$user" "$database" &&
    compose exec -T postgres createdb --username="$user" --owner="$user" --template=template0 "$database" &&
    compose exec -T postgres pg_restore --exit-on-error --no-owner --no-acl \
      --username="$user" --dbname="$database" < "$archive"
}
