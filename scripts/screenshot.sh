#!/usr/bin/env bash
# Screenshot a page on the compose network with headless Chromium, so nothing is needed on the host
# (no browser, no npm project). Used for the Grafana dashboard and Jaeger trace screenshots in
# docs/results/ (M7 Task 03).
#
# Usage: scripts/screenshot.sh <url> <out.png> [wait-ms]
#   <url>     as seen FROM THE COMPOSE NETWORK: http://grafana:3000/..., http://jaeger:16686/...
#   <out.png> written under docs/results/ (a bare filename is placed there; a path with a
#             directory must stay inside the repository)
#   [wait-ms] settle time before the capture (default 6000): Grafana panels and Jaeger spans render
#             after the page load event
#
# Examples (stack started with `make up-obs`; Grafana allows anonymous viewing there):
#   scripts/screenshot.sh 'http://grafana:3000/d/transfers-overview?kiosk&from=now-15m&to=now' m7-grafana.png 8000
#   scripts/screenshot.sh "http://jaeger:16686/trace/$TRACE_ID" m5-jaeger-end-to-end-trace.png
#
# Env: VIEWPORT (default "1600, 1000"; a tall one such as "1600, 2300" shows a long Grafana dashboard)
#      NET_FROM_SERVICE (optional: join this compose service's network namespace instead of the network)
#      COMPOSE_NETWORK (default: <compose project>_default, i.e. ledger-platform_default)
#      PLAYWRIGHT_VERSION (default 1.56.1, must match the image tag below)
set -euo pipefail

URL="${1:?usage: $0 <url> <out.png> [wait-ms]}"
OUT="${2:?usage: $0 <url> <out.png> [wait-ms]}"
WAIT_MS="${3:-6000}"

PLAYWRIGHT_VERSION="${PLAYWRIGHT_VERSION:-1.56.1}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NETWORK="${COMPOSE_NETWORK:-$(basename "$ROOT")_default}"

case "$OUT" in
  */*) OUT_REL="$OUT" ;;
  *)   OUT_REL="docs/results/$OUT" ;;
esac
mkdir -p "$ROOT/$(dirname "$OUT_REL")"

if [ -n "${NET_FROM_SERVICE:-}" ]; then
  cid="$(docker ps -q --filter "label=com.docker.compose.service=${NET_FROM_SERVICE}" \
    --filter "label=com.docker.compose.project=$(basename "$ROOT")" | head -1)"
  [ -n "$cid" ] || { echo "no running compose service '${NET_FROM_SERVICE}' (make up-obs?)" >&2; exit 1; }
  net=(--network "container:${cid}")
else
  if ! docker network inspect "$NETWORK" >/dev/null 2>&1; then
    echo "compose network '$NETWORK' not found. Is the stack up (make up-obs)? Set COMPOSE_NETWORK to override." >&2
    exit 1
  fi
  net=(--network "$NETWORK")
fi

# --ipc=host: Chromium needs a larger /dev/shm than Docker's 64 MB default (Playwright's guidance).
# --lang: the image's locale is `en-US@posix`, which Chromium rejects (Grafana then fails to start).
# The repo is mounted at /work and the capture is written straight into it.
docker run --rm --ipc=host \
  "${net[@]}" \
  -v "$ROOT:/work" -w /tmp \
  "mcr.microsoft.com/playwright:v${PLAYWRIGHT_VERSION}-noble" \
  npx -y "playwright@${PLAYWRIGHT_VERSION}" screenshot \
    --viewport-size "${VIEWPORT:-1600, 1000}" \
    --lang en-US \
    --wait-for-timeout "$WAIT_MS" \
    --full-page \
    "$URL" "/work/$OUT_REL"

echo "wrote $OUT_REL"
