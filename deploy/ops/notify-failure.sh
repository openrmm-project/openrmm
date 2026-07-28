#!/usr/bin/env bash
# Reports a failed unit (invoked from OnFailure=). Sends a webhook when
# OPS_ALERT_WEBHOOK is set, and always writes to the journal so the failure is
# recoverable from the host even when the network path is the thing that broke.
set -Eeuo pipefail
umask 077

unit=${1:-unknown.unit}
host=$(hostname -f 2>/dev/null || hostname)
when=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# Last lines of the failed unit give the operator the reason without an ssh trip.
detail=$(journalctl --unit "$unit" --lines 20 --no-pager --output cat 2>/dev/null | tail -c 1500 || true)

printf 'ALERT openrmm unit=%s host=%s at=%s\n%s\n' "$unit" "$host" "$when" "$detail" >&2

if [[ -n "${OPS_ALERT_WEBHOOK:-}" ]]; then
  payload=$(printf '%s' "$detail" | python3 -c 'import json,sys; print(json.dumps({"text": sys.stdin.read()}))' 2>/dev/null \
    || printf '{"text": "see journalctl -u %s"}' "$unit")
  # Never let a broken alert path fail the notifier itself; the journal line above
  # is the durable record.
  curl --silent --show-error --fail --max-time 15 \
    --header 'Content-Type: application/json' \
    --data "$(printf '%s' "$payload" | sed "s/^{\"text\": \"/{\"text\": \"[openrmm $host] $unit failed at $when — /")" \
    "$OPS_ALERT_WEBHOOK" >/dev/null || printf 'alert webhook delivery failed for %s\n' "$unit" >&2
fi
