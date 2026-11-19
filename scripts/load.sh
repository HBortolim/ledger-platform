#!/usr/bin/env bash
# Runs a k6 transfer load profile (SPEC.md §10.6, NFR-PERF-4) from Docker.
#
#   scripts/load.sh <steady|throughput|spike>
#
# Local (default): needs `make up`. k6 runs on the compose network and talks to the services by
# name, so Docker Desktop's host port-forwarding is not in the measurements. Tokens are minted
# with scripts/generate-jwt.sh.
#
# Remote: set BASE_URL (the Wallet Service) and, for the end-of-run invariant check, LEDGER_URL
# (the Ledger Service's internal admin API; skipped with a warning if unset). USER_TOKEN and
# ADMIN_TOKEN are used as given, or minted from JWT_PRIVATE_KEY_PATH if unset.
#
# Overridable: RATE, DURATION, MAX_VUS, POOL_SIZE, FUNDING, AMOUNT (see infrastructure/load/lib/pool.js)
# and LOAD_RESULTS_DIR (default docs/results/load). A run writes <profile>-<UTC time>.txt (the
# console output) and .json (k6's --summary-export) there.
set -euo pipefail

profile="${1:-}"
case "$profile" in
  steady|throughput|spike) ;;
  *) echo "usage: $0 <steady|throughput|spike>" >&2; exit 2 ;;
esac

cd "$(dirname "$0")/.."
script="/scripts/transfer_${profile}.js"
results_dir="${LOAD_RESULTS_DIR:-docs/results/load}"
mkdir -p "$results_dir"
# Absolute: a relative path with no leading ./ would be read by compose as a named volume.
results_dir="$(cd "$results_dir" && pwd)"
export LOAD_RESULTS_DIR="$results_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
out="${results_dir}/${profile}-${stamp}"

mint() { ./scripts/generate-jwt.sh "$1" "$2" 2>/dev/null; }
: "${USER_TOKEN:=$(mint "$(uuidgen)" user)}"
: "${ADMIN_TOKEN:=$(mint "$(uuidgen)" admin)}"

if [ -n "${BASE_URL:-}" ]; then
  mode=remote
  ready_url="${BASE_URL}/health/ready"
else
  mode=local
  ready_url="http://localhost:8080/health/ready"
fi
if ! curl -sf "$ready_url" >/dev/null; then
  echo "Wallet Service is not ready at ${ready_url}$([ $mode = local ] && echo ' (run `make up` first)')" >&2
  exit 1
fi

env_flags=(-e "USER_TOKEN=${USER_TOKEN}" -e "ADMIN_TOKEN=${ADMIN_TOKEN}")
for v in RATE DURATION MAX_VUS POOL_SIZE FUNDING AMOUNT; do
  [ -n "${!v:-}" ] && env_flags+=(-e "${v}=${!v}")
done

if [ $mode = local ]; then
  env_flags+=(-e BASE_URL=http://wallet-service:8080 -e LEDGER_URL=http://ledger-service:8081)
  run=(docker compose --profile load run --rm -T k6)
else
  env_flags+=(-e "BASE_URL=${BASE_URL}")
  [ -n "${LEDGER_URL:-}" ] && env_flags+=(-e "LEDGER_URL=${LEDGER_URL}")
  run=(docker run --rm -i
    -v "$PWD/infrastructure/load:/scripts:ro" -v "${results_dir}:/results" grafana/k6:1.3.0)
fi

echo ">> ${profile} (${mode}) -> ${out}.{txt,json}"
set +e
"${run[@]}" run "${env_flags[@]}" --summary-export="/results/${profile}-${stamp}.json" "$script" 2>&1 | tee "${out}.txt"
status=${PIPESTATUS[0]}
set -e
case "$status" in
  0)  echo ">> k6 exit 0: every threshold held" ;;
  99) echo ">> k6 exit 99: a threshold was crossed" ;;
  *)  echo ">> k6 exit ${status}: the run itself failed (setup, script error or runner), not a threshold" ;;
esac
exit "$status"
