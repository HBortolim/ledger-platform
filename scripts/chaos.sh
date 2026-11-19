#!/usr/bin/env bash
# Failure drills under live load (SPEC.md 10.5, 1.1 #5, 9.1-9.6). Needs `make up-chaos`.
#
#   scripts/chaos.sh [scenario-number ...]     # default: all five
#
# Each scenario starts a k6 background load, breaks something, prints what it observes while broken,
# restores it, and asserts recovery. It ends with a reconciliation pass: no money created or lost.
# Prints PASS/FAIL per check; exits non-zero if any check failed. It stops and starts containers, so
# run it on a dev stack only. Every scenario restores what it broke, also on Ctrl-C or failure.
#
# Env: CHAOS_OUT (default docs/results/m7-chaos): k6 outputs and per-scenario logs go there.
set -uo pipefail
cd "$(dirname "$0")/.."

C="docker compose -f docker-compose.yml -f docker-compose.observability.yml -f docker-compose.chaos.yml"
WALLET=http://localhost:8080
LEDGER=http://localhost:8081
PROJ=http://localhost:8082
PROM=http://localhost:9090
TOXI=http://localhost:8474
OUT="${CHAOS_OUT:-docs/results/m7-chaos}"
RUN="$(date -u +%H%M%S)"
mkdir -p "$OUT"

FAILS=0
LOAD_PID=""

say()   { printf '\n=== %s\n' "$*"; }
note()  { printf '    %s\n' "$*"; }
check() { # check "<what>" <command...>: PASS if the command succeeds
  local what="$1"; shift
  if "$@"; then printf '    PASS  %s\n' "$what"; else printf '    FAIL  %s\n' "$what"; FAILS=$((FAILS + 1)); fi
}

# --- environment -----------------------------------------------------------------------------

for t in curl jq python3 docker; do command -v "$t" >/dev/null || { echo "need $t" >&2; exit 2; }; done
curl -sf "$TOXI/proxies" >/dev/null || { echo "Toxiproxy not reachable on :8474. Run \`make up-chaos\` first." >&2; exit 2; }
curl -sf "$PROM/-/ready" >/dev/null || { echo "Prometheus not reachable on :9090. Run \`make up-chaos\` first." >&2; exit 2; }

restore() {
  [ -n "$LOAD_PID" ] && kill "$LOAD_PID" 2>/dev/null
  curl -s -X DELETE "$TOXI/proxies/ledger/toxics/latency_200" >/dev/null 2>&1
  $C start kafka ledger-service projection-service wallet-service >/dev/null 2>&1
}
trap restore EXIT
trap 'exit 130' INT TERM

wait_ready() { # wait_ready <base-url> [seconds]
  local i; for ((i = 0; i < ${2:-120}; i++)); do curl -sf "$1/health/ready" >/dev/null && return 0; sleep 1; done; return 1
}

metric() { # metric <base-url> <path> <metric-name-regex>: sum of matching sample values (0 if none)
  curl -s "$1$2" | awk -v re="^$3" '$0 ~ re { s += $NF } END { printf "%.0f\n", s }'
}
outbox_depth() { metric "$LEDGER" /metrics 'ledger_outbox_depth'; }
pending_entries() { metric "$PROJ" /metrics 'projection_pending_entries'; }
consumer_lag() { metric "$PROJ" /metrics 'projection_consumer_lag'; }
transfer_status() { # transfer_status <regex on status>: POST /transfers requests with such a status so far
  curl -s "$WALLET/actuator/prometheus" | awk -v re="$1" '
    /^wallet_requests_total\{endpoint="\/transfers"/ { match($0, /status="[0-9]+"/); st = substr($0, RSTART + 8, RLENGTH - 9); if (st ~ re) s += $NF }
    END { printf "%.0f\n", s }'
}
five_xx_not_503() { transfer_status '^5(0[0-24-9]|[1-9][0-9])$'; }
prom_q() { curl -s --data-urlencode "query=$1" "$PROM/api/v1/query" | jq -r '.data.result[0].value[1] // "NaN"'; }

# --- helpers: identities, wallets, transfers ---------------------------------------------------

USER_ID="$(uuidgen | tr 'A-Z' 'a-z')"
TOKEN="$(./scripts/generate-jwt.sh "$USER_ID" user 2>/dev/null)"
ADMIN="$(./scripts/generate-jwt.sh "$(uuidgen)" admin 2>/dev/null)"

new_wallet() { # new_wallet <funding>
  local id
  id="$(curl -s -X POST "$WALLET/wallets" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"currency":"BRL"}' | jq -r .walletId)"
  curl -s -o /dev/null -X POST "$WALLET/deposits" -H "Authorization: Bearer $ADMIN" -H "Idempotency-Key: $(uuidgen)" \
    -H 'Content-Type: application/json' -d "{\"destinationWalletId\":\"$id\",\"amount\":\"$1\"}"
  echo "$id"
}

probe() { # probe <src> <dst>: one transfer; prints "<status> <retry-after|-> <seconds>"
  curl -s -o /dev/null -D - -w 'T %{http_code} %{time_total}\n' -X POST "$WALLET/transfers" \
    -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" -H 'Content-Type: application/json' \
    -d "{\"sourceWalletId\":\"$1\",\"destinationWalletId\":\"$2\",\"amount\":\"0.01\"}" --max-time 15 \
  | awk 'tolower($1) == "retry-after:" { ra = $2 + 0 } /^T / { code = $2; t = $3 } END { print code, (ra == "" ? "-" : ra), t }' | tr -d '\r'
}

stale_of() { # stale_of <wallet>: the "stale" flag of GET /balance, polled for up to 20 s because the Wallet
  # Service only reports stale once the newest unprojected entry is more than 5 s old (NFR-CONS-2's grace)
  local i v=false
  for ((i = 0; i < 20; i++)); do
    v="$(curl -s "$WALLET/wallets/$1/balance" -H "Authorization: Bearer $TOKEN" | jq -r '.stale')"
    [ "$v" = true ] && break; sleep 1
  done
  echo "$v"
}

reconcile() { curl -s -X POST "$LEDGER/admin/reconciliation/run"; }
reconcile_clean() { # polls: projection findings ignore wallets that moved in the last 10 s
  local i r
  for ((i = 0; i < ${1:-40}; i++)); do
    r="$(reconcile)"
    [ "$(echo "$r" | jq -r '.clean')" = true ] && { REPORT="$r"; return 0; }
    sleep 3
  done
  REPORT="$r"; return 1
}

# --- background load (the steady k6 profile at a low rate) --------------------------------

load_start() { # load_start <rate> <seconds> <tag>
  RATE="$1" DURATION="${2}s" POOL_SIZE=40 LOAD_RESULTS_DIR="$PWD/$OUT/k6" \
    scripts/load.sh steady >"$OUT/k6-$3.log" 2>&1 &
  LOAD_PID=$!
  local i a b
  for ((i = 0; i < 90; i++)); do # setup (pool creation) first, then traffic
    a="$(transfer_status '^201$')"; sleep 2; b="$(transfer_status '^201$')"
    [ $((b - a)) -ge 10 ] && return 0
  done
  note "background load never got going: see $OUT/k6-$3.log"; return 1
}
load_wait() { wait "$LOAD_PID" 2>/dev/null; LOAD_PID=""; }

PA="" PB=""
ensure_probe_wallets() { [ -n "$PA" ] || { PA="$(new_wallet 100000.00)"; PB="$(new_wallet 0.00)"; }; }

# --- scenarios ----------------------------------------------------------------------------------

s1() {
  say "1. Ledger killed mid-transfer (SPEC 9.3, NFR-AVAIL-2/4)"
  note "inject: kill ledger-service for 20 s under 20 RPS.  expect: 503 + Retry-After, never 500; no partial transaction"
  ensure_probe_wallets
  load_start 20 90 s1 || FAILS=$((FAILS + 1))
  local e0; e0="$(five_xx_not_503)"; local s503_0; s503_0="$(transfer_status '^503$')"
  $C kill ledger-service >/dev/null 2>&1; sleep 4
  read -r code ra t < <(probe "$PA" "$PB")
  note "while down: POST /transfers -> $code, Retry-After: $ra (${t}s)"
  check "503 with a Retry-After header while the ledger is down" test "$code" = 503 -a "$ra" != -
  sleep 15; $C start ledger-service >/dev/null 2>&1; wait_ready "$LEDGER" 60
  note "ledger back"; load_wait
  note "503s served during the drill: $(( $(transfer_status '^503$') - s503_0 ))"
  check "no 5xx other than 503 during the drill (NFR-AVAIL-4)" test "$(five_xx_not_503)" = "$e0"
  read -r code ra t < <(probe "$PA" "$PB")
  check "transfers succeed again (201) after recovery" test "$code" = 201
  check "reconciliation clean: no partial transaction" reconcile_clean
}

s2() {
  say "2. Kafka killed (SPEC 9.5, NFR-AVAIL-5)"
  local rate=40 cap=300
  note "inject: stop kafka under ${rate} RPS until OutboxBacklog fires (depth > 1000 for 1 m, plus rule evaluation every 60 s), at most ${cap} s"
  note "expect: outbox > 1000, OutboxBacklog firing, balances stale"
  ensure_probe_wallets
  load_start "$rate" $((cap + 120)) s2 || FAILS=$((FAILS + 1))
  local e0; e0="$(five_xx_not_503)"
  $C stop kafka >/dev/null 2>&1
  local i d peak=0 fired=no staleflag="" t0=$SECONDS
  for ((i = 0; i < cap; i += 5)); do
    sleep 5; d="$(outbox_depth)"; [ "$d" -gt "$peak" ] && peak=$d
    [ $((i % 20)) -eq 0 ] && note "t+$((SECONDS - t0))s  outbox depth $d"
    if [ "$(curl -s "$PROM/api/v1/alerts" | jq -r '[.data.alerts[] | select(.labels.alertname=="OutboxBacklog" and .state=="firing")] | length')" -gt 0 ]; then
      fired=yes; note "t+$((SECONDS - t0))s  OutboxBacklog is FIRING (depth $d)"; break
    fi
  done
  probe "$PA" "$PB" >/dev/null; staleflag="$(stale_of "$PB")"
  note "peak outbox depth $peak; OutboxBacklog firing: $fired; GET /balance stale: ${staleflag:-unknown}"
  check "outbox depth passed 1000" test "$peak" -gt 1000
  check "OutboxBacklog alert fired" test "$fired" = yes
  check "GET /balance reported stale=true while events were stuck" test "$staleflag" = true
  $C start kafka >/dev/null 2>&1; note "kafka restarted"
  local j; for ((j = 0; j < 120; j++)); do [ "$(outbox_depth)" = 0 ] && break; sleep 2; done
  check "outbox drained to 0" test "$(outbox_depth)" = 0
  load_wait
  local k; for ((k = 0; k < 60; k++)); do [ "$(pending_entries)" = 0 ] && [ "$(consumer_lag)" = 0 ] && break; sleep 2; done
  check "projection caught up (lag 0, no parked entries)" test "$(pending_entries)" = 0 -a "$(consumer_lag)" = 0
  check "no 5xx other than 503 during the drill" test "$(five_xx_not_503)" = "$e0"
  check "reconciliation clean after the drain" reconcile_clean
}

s3() {
  say "3. Projection killed (SPEC 9.6, NFR-AVAIL-3)"
  note "inject: kill projection-service for ~30 s under 20 RPS.  expect: balances go stale; afterwards lag 0 and projection matches the ledger"
  ensure_probe_wallets
  load_start 20 100 s3 || FAILS=$((FAILS + 1))
  local e0; e0="$(five_xx_not_503)"
  $C kill projection-service >/dev/null 2>&1; sleep 3
  probe "$PA" "$PB" >/dev/null
  local st; st="$(stale_of "$PB")"
  note "while down: GET /balance stale = $st"
  check "GET /balance reported stale=true while the projection was down" test "$st" = true
  check "transfers kept working (no 5xx) while the projection was down" test "$(five_xx_not_503)" = "$e0"
  sleep 5; $C start projection-service >/dev/null 2>&1; wait_ready "$PROJ" 60
  local i lag peak=0; for ((i = 0; i < 6; i++)); do lag="$(consumer_lag)"; [ "$lag" -gt "$peak" ] && peak=$lag; sleep 2; done
  note "projection back; peak consumer lag sampled after restart: $peak"
  load_wait
  local k; for ((k = 0; k < 60; k++)); do [ "$(pending_entries)" = 0 ] && [ "$(consumer_lag)" = 0 ] && break; sleep 2; done
  check "lag 0 and no parked entries after catch-up" test "$(pending_entries)" = 0 -a "$(consumer_lag)" = 0
  check "projection matches the ledger (no lost or doubled event)" reconcile_clean
}

# Scenario 4's client. burst: send distinct keys as fast as it can until the service dies.
# replay: resend every key with the same body until each resolves.
idem_client() { python3 - "$@" <<'PY'
import sys, json, threading, time, urllib.request, urllib.error
mode, base, token, src, dst, prefix, keyfile = sys.argv[1:8]
body = json.dumps({"sourceWalletId": src, "destinationWalletId": dst, "amount": "0.01"}).encode()
lock = threading.Lock(); results = {}
def send(key):
    req = urllib.request.Request(base + "/transfers", data=body, method="POST", headers={
        "Authorization": "Bearer " + token, "Idempotency-Key": key, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as r: return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read() or b"{}")
        except Exception: return e.code, {}
    except Exception: return 0, {}      # connection refused / reset / timeout: outcome unknown
if mode == "burst":
    stop = threading.Event(); counter = [0]; errors = [0]
    def worker():
        while not stop.is_set() and counter[0] < 4000:
            with lock: i = counter[0]; counter[0] += 1
            key = f"{prefix}-{i}"; st, _ = send(key)
            with lock:
                results[key] = st
                if st == 0:
                    errors[0] += 1
                    if errors[0] >= 5: stop.set()
    ts = [threading.Thread(target=worker) for _ in range(20)]
    [t.start() for t in ts]; [t.join() for t in ts]
    json.dump(results, open(keyfile, "w"))
    ok = sum(1 for s in results.values() if s == 201); bad = sum(1 for s in results.values() if s == 0)
    print(f"burst: {len(results)} keys sent, {ok} answered 201, {bad} got a connection error (outcome unknown)")
else:
    keys = list(json.load(open(keyfile)).keys()); done = {}; in_progress = [0]; t0 = time.time()
    def worker(chunk):
        for key in chunk:
            while time.time() - t0 < 300:
                st, js = send(key)
                if st == 201: done[key] = js.get("transferId"); break
                if st == 409: in_progress[0] += 1
                time.sleep(3)
    ts = [threading.Thread(target=worker, args=(keys[i::20],)) for i in range(20)]
    [t.start() for t in ts]; [t.join() for t in ts]
    json.dump(done, open(keyfile + ".done", "w"))
    print(f"replay: {len(done)}/{len(keys)} keys resolved to 201; {in_progress[0]} replies were 409 IN_PROGRESS (waiting for the janitor); took {time.time() - t0:.0f}s")
PY
}

s4() {
  say "4. Wallet killed mid-transfer (SPEC 9.1/9.2, 1.1 #5)"
  note "inject: a burst of transfers with fixed Idempotency-Keys, kill wallet-service mid-burst, restart, replay every key"
  note "expect: every key ends as one 201; exactly one ledger transaction (two entries) per key; none doubled"
  local a b keys="$OUT/s4-keys-$RUN.json"
  a="$(new_wallet 100000.00)"; b="$(new_wallet 0.00)"
  idem_client burst "$WALLET" "$TOKEN" "$a" "$b" "chaos-$RUN" "$keys" &
  local cpid=$!
  sleep 1.5; $C kill wallet-service >/dev/null 2>&1; wait "$cpid"
  sleep 3; $C start wallet-service >/dev/null 2>&1; wait_ready "$WALLET" 120; note "wallet back; replaying (a PENDING key stays 409 until the 60 s janitor frees it)"
  idem_client replay "$WALLET" "$TOKEN" "$a" "$b" "chaos-$RUN" "$keys"
  local n resolved; n="$(jq 'length' "$keys")"; resolved="$(jq 'length' "$keys.done")"
  check "every key resolved to 201 after the replay" test "$n" = "$resolved"
  # Independent of the service: derive each transaction id the way ADR-0006 says (UUIDv5 of "userId:key").
  local ids; ids="$(python3 - "$USER_ID" "$keys" <<'PY'
import sys, json, uuid
ns = uuid.UUID("331b3e69-c5f0-425b-b7c0-0d5ef39ae323")
print(",".join(str(uuid.uuid5(ns, f"{sys.argv[1]}:{k}")) for k in json.load(open(sys.argv[2]))))
PY
)"
  local txs entries
  txs="$(echo "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = ANY('{$ids}'::uuid[]);" | $C exec -T postgres psql -U ledger -d ledger -tA)"
  entries="$(echo "SELECT count(*) FROM ledger_db.ledger_entries WHERE transaction_id = ANY('{$ids}'::uuid[]);" | $C exec -T postgres psql -U ledger -d ledger -tA)"
  note "keys: $n   ledger transactions for those keys: $txs   entries: $entries"
  check "exactly one ledger transaction per idempotency key" test "$txs" = "$n"
  check "exactly two entries per key (nothing posted twice)" test "$entries" = $((n * 2))
  check "the transferId each key returned is the derived id" test "$(jq -r '.[]' "$keys.done" | sort | tr '\n' ,)" = "$(echo "$ids" | tr ',' '\n' | sort | tr '\n' ,)"
  check "reconciliation clean" reconcile_clean
}

s5() {
  say "5. +200 ms Wallet -> Ledger (SPEC 10.5)"
  note "inject: Toxiproxy latency toxic 200 ms on the Wallet->Ledger link for ~60 s under 20 RPS.  expect: p95 > 200 ms observed, no errors"
  ensure_probe_wallets
  load_start 20 130 s5 || FAILS=$((FAILS + 1))
  local e0; e0="$(five_xx_not_503)"; local s503_0; s503_0="$(transfer_status '^503$')"
  curl -s -X POST "$TOXI/proxies/ledger/toxics" -d '{"name":"latency_200","type":"latency","stream":"downstream","attributes":{"latency":200}}' >/dev/null
  sleep 45
  local p95 slow
  p95="$(prom_q 'histogram_quantile(0.95, sum by (le) (increase(wallet_request_duration_seconds_bucket{endpoint="/transfers"}[30s])))')"
  read -r code ra t < <(probe "$PA" "$PB")
  note "with +200 ms: transfer p95 (Prometheus, last 30 s) = ${p95}s; a single probe took ${t}s (status $code)"
  check "NFR-PERF-1 violation is observed: p95 above 200 ms" awk -v p="$p95" 'BEGIN { exit !(p + 0 > 0.2) }'
  check "no errors while slow (ledger timeout is 5 s)" test "$(five_xx_not_503)" = "$e0" -a "$(transfer_status '^503$')" = "$s503_0"
  curl -s -X DELETE "$TOXI/proxies/ledger/toxics/latency_200" >/dev/null; note "latency removed"
  sleep 50
  p95="$(prom_q 'histogram_quantile(0.95, sum by (le) (increase(wallet_request_duration_seconds_bucket{endpoint="/transfers"}[30s])))')"
  note "after recovery: transfer p95 = ${p95}s"
  check "p95 back under 200 ms" awk -v p="$p95" 'BEGIN { exit !(p + 0 < 0.2) }'
  load_wait
  check "reconciliation clean" reconcile_clean
}

# --- run ----------------------------------------------------------------------------------------

SELECTED=("$@"); [ ${#SELECTED[@]} -eq 0 ] && SELECTED=(1 2 3 4 5)
echo "chaos drills, run $RUN, scenarios: ${SELECTED[*]}"
for n in "${SELECTED[@]}"; do "s$n"; done

say "Final state: no money created or lost"
reconcile_clean 60
echo "$REPORT" | jq -c '{clean, invariant: .invariant.clean, moneySupplyClean: .moneySupply.clean, ledgerTotal: .moneySupply.ledgerTotal, cachedTotal: .moneySupply.cachedTotal, projection: .projection.clean}'
check "reconciliation clean (invariant, money supply, projection)" test "$(echo "$REPORT" | jq -r .clean)" = true
check "no parked projection entries" test "$(pending_entries)" = 0
check "outbox empty" test "$(outbox_depth)" = 0
echo
if [ "$FAILS" -eq 0 ]; then echo "ALL CHECKS PASSED"; else echo "$FAILS CHECK(S) FAILED"; exit 1; fi
