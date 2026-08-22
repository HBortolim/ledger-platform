#!/usr/bin/env bash
# Verify NFR-OBS-5: the most recent wallet-service trace spans all three
# services end to end. Requires the observability stack (make up-obs) and at
# least one transfer already executed.
set -euo pipefail

JAEGER_URL="${JAEGER_URL:-http://localhost:16686}"
EXPECTED_SERVICES=("wallet-service" "ledger-service" "projection-service")

trace=$(curl -sf "${JAEGER_URL}/api/traces?service=wallet-service&limit=1&lookback=1h") \
  || { echo "FAIL: cannot reach Jaeger at ${JAEGER_URL} — is 'make up-obs' running?" >&2; exit 1; }

count=$(printf '%s' "$trace" | jq '.data | length')
if [ "$count" -eq 0 ]; then
  echo "FAIL: no wallet-service traces in the last hour — run a transfer first" >&2
  exit 1
fi

trace_id=$(printf '%s' "$trace" | jq -r '.data[0].traceID')
services=$(printf '%s' "$trace" | jq -r '.data[0].processes | to_entries[].value.serviceName' | sort -u)

echo "trace ${trace_id} contains services:"
printf '  %s\n' $services

missing=0
for svc in "${EXPECTED_SERVICES[@]}"; do
  if ! printf '%s\n' $services | grep -qx "$svc"; then
    echo "FAIL: trace ${trace_id} has no spans from ${svc}" >&2
    missing=1
  fi
done

if [ "$missing" -ne 0 ]; then
  echo "FAIL: the end-to-end trace is broken — NFR-OBS-5 not satisfied" >&2
  exit 1
fi

echo "PASS: NFR-OBS-5 — one trace spans all three services (${trace_id})"
