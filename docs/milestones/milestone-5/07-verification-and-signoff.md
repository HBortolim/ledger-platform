# Task 07 — End-to-end verification & milestone sign-off

**Status:** Not started
**Owner:** Cross-service
**Depends on:** 01, 02, 03, 04, 05, 06 (all of them)
**Blocks:** Milestone 6 kickoff
**Spec reference:** [`SPEC.md` §11 — Milestone 5](../../SPEC.md) ("Demonstrable: run a transfer, open Jaeger, see the trace span from API ingress through projection write"), NFR-OBS-1..5, §10.3

---

## Goal

Milestone 5's acceptance bar is a single sentence: *run a transfer, open Jaeger, see the trace from API ingress through to the projection write.* This task proves that end to end, adds an automated regression guard so the trace chain can't silently break later, walks the NFR-OBS-1..5 checklist honestly, and closes out the milestone's documentation.

It is the gate — don't start it until Tasks 01–06 are individually merged and green.

## Step 1: The demonstrable

- [ ] Run: `make down && make up-obs`
- [ ] Execute the milestone overview's demo script, steps 1–2 (two wallets, fund the source, one transfer). Record the returned `transferId`.
- [ ] Open `http://localhost:16686`, service `wallet-service`, Find Traces, open the newest trace.
- [ ] Confirm **one** trace containing spans from **all three** services, in this order:

| # | Service | Span | From |
|---|---|---|---|
| 1 | wallet-service | `POST /transfers` (server) | Task 04 |
| 2 | wallet-service | HTTP client span to the ledger | Task 04 |
| 3 | ledger-service | `POST /ledger/postings` (server) | Task 03 |
| 4 | ledger-service | `outbox publish` (producer) | Task 05 |
| 5 | projection-service | `projection apply` (consumer) | Task 05 |

- [ ] Record the trace ID. Screenshot the waterfall — this is the single most valuable artifact this milestone produces.
- [ ] Confirm the visible gap between spans 3 and 4: that is real outbox lag, not a rendering artifact.

## Step 2: Automate the trace-chain check against Jaeger

A manual UI check can't stop a future refactor from silently splitting the trace in two. Jaeger's HTTP API makes this scriptable.

**Files:**
- Create: `scripts/verify-trace.sh`

```bash
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
```

- [ ] Create the script and `chmod +x scripts/verify-trace.sh`.
- [ ] Run it. Expected: `PASS`, listing all three services.
- [ ] Prove it has teeth: `docker compose -f docker-compose.yml -f docker-compose.observability.yml stop projection-service`, run another transfer, wait ~10s, re-run the script. It must **FAIL** on the missing `projection-service`. Restart the service afterwards.

## Step 3: Add the Kafka-header regression test to the E2E suite

Step 2 needs the observability stack; this one doesn't, so it can guard the propagation chain from the ordinary E2E suite.

**Files:**
- Modify: `tests/e2e/helpers_test.go` (return the matched record)
- Modify: `tests/e2e/transfer_e2e_test.go` (the new test, beside `TestE2E_HappyPath`)

First, the helper. `assertLedgerPostedObserved` (`helpers_test.go:241`) already consumes `ledger.posted.v1` and matches on `transactionId`, but discards the record. Change it to return the match:

```go
func assertLedgerPostedObserved(t *testing.T, transactionID uuid.UUID) *kgo.Record {
```

Capture the matching record inside the existing `fetches.EachRecord` closure and return it instead of returning bare. Existing call sites need **no** change — Go permits ignoring a return value when the call is used as a statement.

Then add the test, following the file's existing conventions (build tag `e2e`, the `signJWT`/`newWallet`/`doRequest` helpers, per-run random user UUIDs). It should:

1. Create two wallets, fund the source via the Ledger API, and execute one transfer — mirror `TestE2E_HappyPath`'s setup rather than writing a parallel one.
2. Take the record from `record := assertLedgerPostedObserved(t, transactionID)`.
3. Assert the record carries a **non-empty, well-formed** `traceparent` header:

```go
	// NFR-OBS-2: the Kafka hop must carry W3C trace context. Before M5 this
	// header was hardcoded empty (posting.go's "wired in M5" placeholder), so
	// an empty value here means the propagation chain has regressed and the
	// end-to-end trace is silently broken in two.
	var traceparent string
	for _, h := range record.Headers {
		if h.Key == "traceparent" {
			traceparent = string(h.Value)
		}
	}
	if traceparent == "" {
		t.Fatal("ledger.posted.v1 record carries no traceparent header (NFR-OBS-2)")
	}
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`).MatchString(traceparent) {
		t.Errorf("traceparent = %q, want a well-formed W3C traceparent", traceparent)
	}
```

4. Assert the payload's `traceparent` field mirrors the header (ADR-0013's stated contract).

- [ ] Write the test.
- [ ] Run: `make up && make test-e2e` — expect all E2E tests green, including the new one, **against the core-only stack**. This works because Task 01 sets `OTEL_EXPORTER_OTLP_ENDPOINT` in `docker-compose.yml` unconditionally: spans are created and context injected even when the exporter can't reach a collector. Note that dependency in your report — it is the one place the E2E suite now cares about an observability setting.

## Step 4: Walk the NFR-OBS checklist

Verify each requirement against reality and record the evidence. Mark honestly — a partially-met NFR recorded as such is worth more than a checkmark that doesn't survive scrutiny.

- [ ] **NFR-OBS-1** — logs are structured JSON on stdout with `trace_id`, `span_id`, `service`, `level`, `msg`. Check all three services:

```sh
for svc in wallet-service ledger-service projection-service; do
  echo "== $svc"
  docker compose logs --no-log-prefix "$svc" | tail -5 | \
    jq -e 'has("trace_id") and has("span_id") and has("service") and has("level") and has("msg")' >/dev/null \
    && echo "  shape OK" || echo "  SHAPE FAIL"
done
```

- [ ] **NFR-OBS-2** — `traceparent` propagated over HTTP and as a Kafka header. Evidence: Task 04's outbound-header IT, Step 3's E2E test, and the `kafka-console-consumer --property print.headers=true` output.
- [ ] **NFR-OBS-3** — `/metrics` in Prometheus format on each service, with the §7.4 metric set. Evidence: all three Prometheus targets `UP`; spot-check one metric per service in the Prometheus UI. (This was already satisfied before M5 — confirm, don't rebuild.)
- [ ] **NFR-OBS-4** — the `Transfers Overview` dashboard with all six panels populated. Evidence: Task 06.
- [ ] **NFR-OBS-5** — a single transfer traceable end to end in Jaeger. Evidence: Steps 1–2.

## Step 5: Confirm nothing regressed

- [ ] `make test` from the repo root — full unit + integration suite across all three services, green, **with no observability stack running**.
- [ ] `make up && make test-e2e` — E2E green against the core-only stack.
- [ ] `make down && make up` — core stack alone comes up healthy inside the §1.1 60-second bar. Time it: `time (make up && sleep 0 )` is not meaningful; instead watch `docker compose ps` until all health-checked services report `healthy` and note the elapsed wall time.
- [ ] Confirm no observability container is required for any of the above.

## Step 6: Capture artifacts

**Files:**
- Create: `docs/results/m5-jaeger-end-to-end-trace.png` (Step 1's waterfall screenshot)
- Create: `docs/results/m5-grafana-transfers-overview.png` (Task 06's populated dashboard)

`SPEC.md` §12 lists `docs/results/` for exactly this. These two images are what a reviewer looks at first, before reading a line of code.

- [ ] Save both screenshots. Confirm the trace screenshot legibly shows all three service names and the outbox gap.

## Step 7: Close out documentation

- [ ] Update `docs/milestones/milestone-5/00-overview.md`: `Status: Complete`, and check off every item in "Definition of done" — verifying each against actual evidence, not from memory.
- [ ] Set `Status: Complete` on Tasks 01–06's own docs and check off their step boxes, if the implementing task didn't already. (M4 shipped with two task docs still reading "Not started" — don't repeat that.)
- [ ] Confirm ADR-0012 (Task 01) and ADR-0013 (Task 05) both exist in `docs/decisions/` and that their content matches what was actually built, not what was planned.
- [ ] Check whether `SPEC.md` §11 tracks per-milestone status inline. As of M4 it does not — plain prose bullets, no markers. If that's still true, make no change and say so; don't invent a convention.
- [ ] Fill in this task's Implementation Record below.

## Step 8: Commit

```bash
git add scripts/verify-trace.sh tests/e2e docs/results docs/milestones/milestone-5
git commit -m "test(observability): verify the end-to-end trace and close out Milestone 5

Adds scripts/verify-trace.sh (asserts one trace spans all three services via
the Jaeger API) and an E2E regression test asserting a well-formed traceparent
on the ledger.posted.v1 Kafka header, so the propagation chain can't silently
break. Captures the Jaeger waterfall and Grafana dashboard in docs/results/."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| Jaeger, one transfer | a single trace with spans from all three services, ending at the projection write |
| `scripts/verify-trace.sh` | `PASS`; **and** demonstrably FAILs with the projection service stopped |
| E2E traceparent test | green against the core-only stack |
| `make test` | full suite green, no observability stack running |
| `make test-e2e` | green |
| NFR-OBS-1..5 | each verified with recorded evidence |
| `docs/results/` | both screenshots present and legible |
| Milestone 00-overview | Definition of done fully checked off |

## Done when

`scripts/verify-trace.sh` passes against a live stack, the E2E suite is green without one, all five NFR-OBS requirements are verified with evidence, and the milestone docs reflect the actual final state.

## Notes

- If any NFR-OBS requirement turns out **partially** met, say so explicitly in the Implementation Record and open it as a follow-up rather than checking the box. A milestone that honestly reports 4.5/5 is more useful than one that claims 5/5 and falls over in review.
- Step 2's script deliberately queries the *most recent* trace rather than a specific ID — it's a smoke check for CI or a demo, not a precise assertion about one request. Its failure mode is a false negative if the stack is idle, which is why it errors clearly on "no traces in the last hour".
- Trace-ID continuity through to the projection service depends on the projection consumer having actually processed the event by the time you look. On a healthy local stack that's sub-second, but if the trace looks truncated, check `projection_consumer_lag` before assuming the propagation broke.
- Don't chase perfect span naming or attribute conventions here. Semantic-convention polish (`messaging.*` attribute completeness, span-name conventions) is worth a follow-up ticket, not a milestone gate.

## Implementation record

_(Fill in after Steps 1–7: the verified trace ID, the NFR-OBS-1..5 evidence table, timing for the core-stack startup check, anything found partially met, and confirmation of the final `make test` / `make test-e2e` runs.)_
