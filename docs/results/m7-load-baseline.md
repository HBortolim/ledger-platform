# Local load baseline (Milestone 7)

Run on 2026-11-19 (UTC), against a fresh `make up-obs`, one profile after another with a one-minute pause.
This is a **local baseline**, not the verdict: it ran on a developer laptop in a Docker VM, with the observability
overlay on and other applications open (host load average ~4 on 8 cores). The numbers say what this stack does
here, not what the NFR targets demand of a deployment. Environment: [`m7-load-environment.txt`](m7-load-environment.txt).

Every run finished with the ledger's reconciliation clean (invariant and money supply), 0 failed requests and 0
dropped iterations. k6 exit code 0 on all three.

## Results against the NFR-PERF targets

| NFR | Target | Measured | Met |
|---|---|---|---|
| PERF-1: Wallet `POST /transfers`, 50 RPS | p50 < 80 ms, p95 < 200 ms, p99 < 500 ms | p50 11.6 ms, p95 28.2 ms, p99 70.9 ms (Prometheus); k6 p95 30.0 ms, p99 78.0 ms | Yes |
| PERF-2: ledger posting | p95 < 50 ms, p99 < 150 ms | at 50 RPS p95 9.4 ms, p99 26.4 ms; at 100 TPS p95 9.8 ms, p99 33.1 ms | Yes, with a caveat (below) |
| PERF-3: projection lag | p95 < 2 s, p99 < 5 s | at 50 RPS p95 0.22 s, p99 0.24 s; at 100 TPS p95 0.23 s, p99 0.29 s; spike p95 0.54 s, p99 0.94 s | Yes |
| PERF-4: throughput | >= 100 TPS for 5 min, error rate < 0.1% | 100.9 requests/s (99.5 transfers/s) for 5 min, 0 of 30,402 requests failed | Yes |

**PERF-2 caveat.** `ledger_posting_duration_seconds` times the repository's `Post` call inside the ledger, not the
HTTP request to `POST /ledger/postings`, which NFR-PERF-2 names. The HTTP handler adds JSON decoding and the
network hop, so the metric understates the endpoint latency by a small amount. The Wallet Service's end-to-end
figure (PERF-1, which includes the ledger call) is 28 ms at p95, so the ledger endpoint cannot be near its 50 ms
limit, but the PERF-2 row is "met" on the metric as defined, not on the endpoint.

**Quantile precision.** Prometheus quantiles are interpolated within histogram buckets. The wallet histogram is
fine-grained around these values (bounds 22.4, 28.0, 33.6, ... 67.1, 89.5 ms), so p95 and p99 are good to a few
ms. The ledger histogram is coarse (bounds 5, 10, 25, 50, 100, 250 ms), so its p95 of 9.4 ms means "in the 5 to 10 ms
bucket" and the p99 of 26 to 33 ms means "in the 25 to 50 ms bucket". Projection lag bounds are 0.05, 0.1, 0.25,
0.5, 1, 2 s, so 0.24 s and 0.29 s mean "in the 0.1 to 0.25 s / 0.25 to 0.5 s buckets".

## Per profile

Client side is k6; server side is Prometheus over the same window (`m7-load-server-side.txt`).

| | steady (50 RPS, 5 min) | throughput (100 RPS, 5 min) | spike (10 to 200 RPS) |
|---|---|---|---|
| k6 iterations | 15,001 (49.5/s) | 30,001 (99.5/s) | 18,149 (150/s average) |
| k6 requests failed | 0 of 15,402 | 0 of 30,402 | 0 of 18,550 |
| k6 dropped iterations | 0 | 0 | see note |
| k6 transfer latency | p95 30.0 ms, p99 78.0 ms | (not gated) | max 314.7 ms (gate: < 5000 ms) |
| Wallet transfer p50 / p95 / p99 (Prometheus) | 11.6 / 28.2 / 70.9 ms | 10.3 / 30.7 / 73.4 ms | 10.0 / 38.4 / 96.9 ms |
| Ledger post p50 / p95 / p99 | 3.0 / 9.4 / 26.4 ms | 3.1 / 9.8 / 33.1 ms | 3.2 / 15.1 / 41.1 ms |
| Projection lag p50 / p95 / p99 | 0.07 / 0.22 / 0.24 s | 0.08 / 0.23 / 0.29 s | 0.14 / 0.54 / 0.94 s |
| Reconciliation at end | clean | clean | clean |
| Gates | all held | all held | all held |

Raw k6 output: `load/steady-20261119T011829Z.*`, `load/throughput-20261119T012443Z.*`, `load/spike-20261119T013038Z.*`.
Grafana per run: [`m7-load-steady-grafana.png`](m7-load-steady-grafana.png),
[`m7-load-throughput-grafana.png`](m7-load-throughput-grafana.png), [`m7-load-spike-grafana.png`](m7-load-spike-grafana.png).

Spike note: the spike profile's `dropped_iterations` is not gated (SPEC §10.6 gates it on `max` and invariants), and
the earlier 14 November smoke run dropped 5 iterations at the 200 RPS peak. In this run the summary reports no
dropped-iteration line, i.e. none.

## What bounds throughput here

From the 8-second `docker stats` samples (`m7-load-*-docker-stats.txt`), peak CPU per container:

| | steady | throughput | spike |
|---|---|---|---|
| kafka | 159% | 150% | 171% |
| wallet-service | 324% (JVM start-up and JIT; 114% in the next run) | 114% | 90% |
| postgres | 47% | 86% | 69% |
| ledger-service | 16% | 32% | 25% |
| projection-service | 13% | 23% | 21% |

At 100 TPS no container is near saturation of the VM's 8 CPUs, memory stays under 1 GB per container (Kafka
about 1 GB, the Java service about 400 MB, against a 4 GB VM), and latency barely moved between 50 and 100 RPS
(p95 28 to 31 ms). The system was not pushed to its limit by these profiles. Kafka is the largest CPU consumer,
mostly its own JVM and the 12-partition topic. This baseline does not find the saturation point; it shows
headroom above 100 TPS and a spike to 200 RPS with a worst request of 315 ms.

## Misses and investigations

None of the four NFR-PERF rows missed, so nothing was tuned. Two things from the earlier smoke runs are explained:

- **Dropped iterations and a high p95 in the 30 s smoke runs (Task 01).** Not reproduced. They happened in short
  runs that included JVM warm-up, with other containers running; a 60 s run on a fresh stack still showed p95 164 ms
  (warm-up), while the 5-minute runs settle at p95 30 ms. Short runs under about a minute measure warm-up, not the
  steady state.
- **Sequence-gap parking.** `projection_seq_gaps_total` increased by about 1,400 per 5 minutes at 100 TPS (the
  "new gaps" series in the throughput screenshot) while `projection_pending_entries` stayed at 0. Entries for a wallet
  arrive out of order across partitions often under concurrency, are parked and applied once the missing one arrives
  (ADR-0017). It costs nothing visible here (lag p99 under 0.3 s) but is the thing to watch if lag ever grows.

## Not established

- The saturation point (where latency or errors start to rise). That needs a ramp past 200 RPS.
- Behaviour on a deployed environment (decision 3 in the milestone overview): the authoritative NFR numbers come
  from the later cloud run. The harness is portable: `BASE_URL`, `LEDGER_URL`, tokens as env vars.
- Idle-host conditions. Other applications were running.
