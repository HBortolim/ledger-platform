#!/usr/bin/env bash
# Verify NFR-OBS-5: a transfer is traceable end to end across all three
# services. Requires the observability stack (make up-obs) and at least one
# transfer already executed.
set -euo pipefail

JAEGER_URL="${JAEGER_URL:-http://localhost:16686}"
TIMEOUT="${VERIFY_TRACE_TIMEOUT:-30}"
EXPECTED_SERVICES=("wallet-service" "ledger-service" "projection-service")

# Pick the newest trace that is actually a transfer. Selecting purely by
# recency picks up health-check, actuator-scrape, and scheduled-task traces,
# which have no downstream spans by design.
JQ_SELECT_TRANSFER='
  [ .data[]
    | select(any(.spans[]; .operationName | ascii_downcase | contains("/transfers")))
  ]
  | max_by([.spans[].startTime] | min) // empty
'

deadline=$((SECONDS + TIMEOUT))
trace=""
trace_id=""
services=""
missing=""

while :; do
  response=$(curl -sf "${JAEGER_URL}/api/traces?service=wallet-service&limit=50&lookback=1h") || {
    echo "FAIL: cannot reach Jaeger at ${JAEGER_URL} — is 'make up-obs' running?" >&2
    exit 1
  }
  trace=$(printf '%s' "$response" | jq -c "$JQ_SELECT_TRANSFER")

  if [ -n "$trace" ]; then
    trace_id=$(printf '%s' "$trace" | jq -r '.traceID')
    services=$(printf '%s' "$trace" | jq -r '.processes | to_entries[].value.serviceName' | sort -u)
    missing=""
    for svc in "${EXPECTED_SERVICES[@]}"; do
      printf '%s\n' "$services" | grep -qx "$svc" || missing="${missing}${svc} "
    done
    if [ -z "$missing" ]; then
      echo "PASS: NFR-OBS-5 — transfer trace ${trace_id} spans all three services"
      exit 0
    fi
  fi

  # Spans arrive via the OTel batch exporter, so a fresh transfer's downstream
  # spans land a few seconds after the response. Retry before declaring failure.
  [ "$SECONDS" -lt "$deadline" ] || break
  sleep 2
done

if [ -z "$trace" ]; then
  echo "FAIL: no transfer trace found in the last hour — run a transfer first" >&2
  exit 1
fi

echo "trace ${trace_id} contains services:" >&2
printf '  %s\n' $services >&2
for svc in $missing; do
  echo "FAIL: trace ${trace_id} has no spans from ${svc}" >&2
done
echo "FAIL: the end-to-end trace is broken — NFR-OBS-5 not satisfied" >&2
exit 1
