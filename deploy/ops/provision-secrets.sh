#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

OPS_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SECRETS_DIR=${SECRETS_DIR:-$OPS_DIR/../secrets}

[[ ! -L "$SECRETS_DIR" ]] || {
  printf 'error: secrets directory cannot be a symlink: %s\n' "$SECRETS_DIR" >&2
  exit 1
}

command -v openssl >/dev/null 2>&1 || {
  printf 'error: required command not found: openssl\n' >&2
  exit 1
}

mkdir -p "$SECRETS_DIR"
chmod 0700 "$SECRETS_DIR"

create_secret() {
  local target=$1 temp
  if [[ -e "$target" || -L "$target" ]]; then
    printf 'preserved existing secret: %s\n' "$target"
    return
  fi
  temp=$(mktemp "$SECRETS_DIR/.secret.XXXXXX")
  if ! openssl rand -hex 32 > "$temp"; then
    rm -f "$temp"
    return 1
  fi
  chmod 0444 "$temp"
  mv "$temp" "$target"
  printf 'created secret: %s\n' "$target"
}

create_secret "$SECRETS_DIR/postgres_password"
create_secret "$SECRETS_DIR/admin_token"
create_secret "$SECRETS_DIR/settings_encryption_key"
