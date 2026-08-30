# Task 06 — Grafana `Transfers Overview` dashboard

**Status:** Complete
**Owner:** Infrastructure
**Depends on:** 01 (Prometheus scraping, Grafana running). Independent of Tasks 02–05 — the metrics it graphs already exist.
**Blocks:** 07
**Spec reference:** [`SPEC.md` NFR-OBS-4](../../SPEC.md), §7.4 (required metrics), overview decision #6

---

## Goal

`infrastructure/observability/grafana/` exists and is completely empty, while `docker-compose.observability.yml` already mounts it at `/etc/grafana/provisioning`. This task fills it: a provisioned Prometheus datasource, a dashboard provider, and the `Transfers Overview` dashboard NFR-OBS-4 names, with its six required panels.

NFR-OBS-4 verbatim: *"The Grafana dashboard `Transfers Overview` shows: transfer RPS, p50/p95/p99 latency, error rate by code, projection lag, Kafka consumer lag, outbox depth."*

Every metric these panels need already exists and is already scraped — this task writes no application code.

## Metric names you're graphing

Confirm these against a live Prometheus before writing queries (`http://localhost:9090`, Graph tab, autocomplete). Micrometer's naming transforms are the easiest thing to get wrong here.

| Panel | Metric | Notes |
|---|---|---|
| Transfer RPS | `wallet_requests_total` | Micrometer counter `wallet_requests` → Prometheus appends `_total`. Labels: `endpoint`, `method`, `status`. |
| Latency p50/p95/p99 | `wallet_request_duration_seconds_bucket` | Timer `wallet_request_duration`; buckets exist because `MetricsFilter` calls `.publishPercentileHistogram()`. |
| Error rate | `wallet_requests_total{status=~"4..\|5.."}` | See overview decision #6 on what "by code" means here. |
| Projection lag | `projection_lag_seconds_bucket`, `max_projection_lag_seconds` | Histogram + companion gauge. |
| Consumer lag | `projection_consumer_lag` | Labels: `topic`, `partition`. |
| Outbox depth | `ledger_outbox_depth` | Gauge, sampled each worker tick. |

## Step 1: Provision the datasource

**Files:**
- Create: `infrastructure/observability/grafana/datasources/prometheus.yml`

```yaml
apiVersion: 1

datasources:
  - name: Prometheus
    type: prometheus
    uid: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
    editable: false
```

The fixed `uid: prometheus` matters — the dashboard JSON in Step 3 references it, and letting Grafana generate a random UID would break every panel.

- [x] Create the file.

## Step 2: Provision the dashboard provider

**Files:**
- Create: `infrastructure/observability/grafana/dashboards/dashboards.yml`

```yaml
apiVersion: 1

providers:
  - name: ledger-platform
    orgId: 1
    type: file
    disableDeletion: false
    updateIntervalSeconds: 30
    allowUiUpdates: true
    options:
      path: /etc/grafana/provisioning/dashboards
      foldersFromFilesStructure: false
```

Grafana loads every `.json` file under `options.path`, so the dashboard from Step 3 sits beside this file. `allowUiUpdates: true` lets a reviewer tweak panels live without Grafana reverting them mid-demo.

- [x] Create the file.

## Step 3: Write the dashboard

**Files:**
- Create: `infrastructure/observability/grafana/dashboards/transfers-overview.json`

```json
{
  "uid": "transfers-overview",
  "title": "Transfers Overview",
  "tags": ["ledger-platform", "nfr-obs-4"],
  "timezone": "browser",
  "schemaVersion": 39,
  "version": 1,
  "refresh": "10s",
  "time": { "from": "now-15m", "to": "now" },
  "panels": [
    {
      "id": 1,
      "type": "timeseries",
      "title": "Transfer RPS",
      "description": "POST /transfers throughput at the wallet edge.",
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 0 },
      "fieldConfig": {
        "defaults": { "unit": "reqps", "custom": { "fillOpacity": 10 } },
        "overrides": []
      },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum(rate(wallet_requests_total{endpoint=\"/transfers\",method=\"POST\"}[1m]))",
          "legendFormat": "transfers/s"
        }
      ]
    },
    {
      "id": 2,
      "type": "timeseries",
      "title": "Transfer latency p50 / p95 / p99",
      "description": "NFR-PERF-1 targets: p50 < 80ms, p95 < 200ms, p99 < 500ms.",
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 0 },
      "fieldConfig": {
        "defaults": {
          "unit": "s",
          "custom": { "fillOpacity": 0 },
          "thresholds": {
            "mode": "absolute",
            "steps": [
              { "color": "green", "value": null },
              { "color": "red", "value": 0.5 }
            ]
          }
        },
        "overrides": []
      },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.50, sum by (le) (rate(wallet_request_duration_seconds_bucket{endpoint=\"/transfers\"}[5m])))",
          "legendFormat": "p50"
        },
        {
          "refId": "B",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.95, sum by (le) (rate(wallet_request_duration_seconds_bucket{endpoint=\"/transfers\"}[5m])))",
          "legendFormat": "p95"
        },
        {
          "refId": "C",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.99, sum by (le) (rate(wallet_request_duration_seconds_bucket{endpoint=\"/transfers\"}[5m])))",
          "legendFormat": "p99"
        }
      ]
    },
    {
      "id": 3,
      "type": "timeseries",
      "title": "Error rate by status code",
      "description": "4xx/5xx responses on /transfers. See milestone-5 overview decision #6 on why this is HTTP status rather than domain error code.",
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 8 },
      "fieldConfig": {
        "defaults": { "unit": "reqps", "custom": { "fillOpacity": 20, "stacking": { "mode": "normal" } } },
        "overrides": []
      },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum by (status) (rate(wallet_requests_total{endpoint=\"/transfers\",status=~\"4..|5..\"}[5m]))",
          "legendFormat": "{{status}}"
        }
      ]
    },
    {
      "id": 4,
      "type": "timeseries",
      "title": "Projection lag",
      "description": "NFR-PERF-3 / NFR-CONS-2: p95 < 2s, p99 < 5s.",
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 8 },
      "fieldConfig": {
        "defaults": { "unit": "s", "custom": { "fillOpacity": 0 } },
        "overrides": []
      },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.95, sum by (le) (rate(projection_lag_seconds_bucket[5m])))",
          "legendFormat": "p95 lag"
        },
        {
          "refId": "B",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.99, sum by (le) (rate(projection_lag_seconds_bucket[5m])))",
          "legendFormat": "p99 lag"
        },
        {
          "refId": "C",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "max_projection_lag_seconds",
          "legendFormat": "last observed"
        }
      ]
    },
    {
      "id": 5,
      "type": "timeseries",
      "title": "Kafka consumer lag",
      "description": "Uncommitted records per topic/partition for the projection consumer group.",
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 16 },
      "fieldConfig": {
        "defaults": { "unit": "short", "custom": { "fillOpacity": 10 } },
        "overrides": []
      },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum by (topic, partition) (projection_consumer_lag)",
          "legendFormat": "{{topic}} p{{partition}}"
        }
      ]
    },
    {
      "id": 6,
      "type": "timeseries",
      "title": "Outbox depth",
      "description": "Unpublished ledger_db.outbox rows. SPEC.md §9.5 alerts at > 1000 -- a climbing line here means Kafka is unreachable and events are queueing.",
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 16 },
      "fieldConfig": {
        "defaults": {
          "unit": "short",
          "custom": { "fillOpacity": 10 },
          "thresholds": {
            "mode": "absolute",
            "steps": [
              { "color": "green", "value": null },
              { "color": "red", "value": 1000 }
            ]
          }
        },
        "overrides": []
      },
      "targets": [
        {
          "refId": "A",
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "ledger_outbox_depth",
          "legendFormat": "unpublished rows"
        }
      ]
    }
  ]
}
```

- [x] Create the file.
- [x] Validate it parses before trying to load it: `jq empty infrastructure/observability/grafana/dashboards/transfers-overview.json` — silence means valid.

## Step 4: Load it and generate traffic

- [x] Run: `make down && make up-obs`
- [x] Open `http://localhost:3000` (admin/admin). The dashboard should appear in the dashboard list as **Transfers Overview** without any manual import.
- [x] Confirm the datasource provisioned: Connections → Data sources → **Prometheus**, marked default, and its "Test" button returns success.
- [x] Generate traffic so the panels have something to draw. Either run the existing k6 profile if k6 is installed:

```sh
make load
```

or, with no k6, a plain loop — create two wallets and fund the source first (milestone overview demo, steps 1–2), then:

```sh
for i in $(seq 1 60); do
  curl -s -o /dev/null -X POST http://localhost:8080/transfers \
    -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: $(uuidgen)" \
    -H "Content-Type: application/json" \
    -d "{\"sourceWalletId\":\"$SRC\",\"destinationWalletId\":\"$DST\",\"amount\":\"1.00\"}"
  sleep 1
done
```

Include a few deliberate failures so the error panel isn't permanently empty — e.g. repeat some requests with an absurd amount to trigger `422 INSUFFICIENT_FUNDS`.

## Step 5: Verify every panel

Check each one individually. A panel showing "No data" is a failed panel, not a cosmetic issue — chase down whether the metric name, the label selector, or the scrape is wrong.

- [x] **Transfer RPS** — non-zero while the loop runs.
- [x] **Latency p50/p95/p99** — three distinct series. If flat/empty, confirm `wallet_request_duration_seconds_bucket` exists in Prometheus; its absence would mean `.publishPercentileHistogram()` isn't taking effect.
- [x] **Error rate by status code** — shows the 422s.
- [x] **Projection lag** — non-zero; sub-second on a healthy local stack.
- [x] **Kafka consumer lag** — present, typically near 0.
- [x] **Outbox depth** — present, typically 0 or a low number. To prove the panel is live rather than just flat: `docker compose -f docker-compose.yml -f docker-compose.observability.yml stop kafka`, run a few transfers, watch the line climb, then `start kafka` and watch it drain. This is also a preview of the §9.5 failure-mode demo.
- [ ] Screenshot the populated dashboard for `docs/results/` (see Task 07). **Deferred:** Task 07 was executed by an agent without browser/GUI access; no screenshot was captured and `docs/results/` does not exist in this repo. Equivalent verification value was captured via the Grafana HTTP API instead — see Task 07 Step 6's deferral note and Implementation Record, and the milestone overview's "Screenshots" note.

## Step 6: Commit

```bash
git add infrastructure/observability/grafana
git commit -m "feat(observability): provision the Transfers Overview Grafana dashboard

NFR-OBS-4's six panels -- transfer RPS, p50/p95/p99 latency, error rate,
projection lag, Kafka consumer lag, outbox depth -- over metrics that already
existed since M2/M3, plus datasource and dashboard-provider provisioning so
the dashboard appears on a fresh make up-obs with no manual import."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| Fresh `make up-obs` | dashboard auto-appears; no manual import needed |
| Prometheus datasource | provisioned, default, "Test" succeeds |
| All six panels | render real data under load; none show "No data" |
| Outbox depth panel | visibly climbs when Kafka is stopped and drains when restarted |
| `jq empty` on the dashboard JSON | valid |

## Done when

A fresh `make up-obs` presents `Transfers Overview` with all six NFR-OBS-4 panels populated under load, with nothing imported by hand.

## Notes

- **Open question for review (overview decision #6):** NFR-OBS-4 says "error rate by *code*". This dashboard reads that as HTTP status code, because that's the label `wallet_requests_total` actually carries. Reading it as *domain* code (`INSUFFICIENT_FUNDS`, `IDEMPOTENCY_KEY_MISMATCH`, …) would need a new label on a metric this milestone's Global Constraints freeze. Flag it for the milestone reviewer rather than deciding unilaterally; if they want domain codes, it's a small M6 follow-up.
- Keep the dashboard's `uid` stable at `transfers-overview` — a changed UID orphans any link a reviewer has saved.
- Don't add alert rules. §9.5 mentions an outbox-depth alert, but alerting infrastructure is nowhere in M5's scope; the threshold colouring on the panel conveys the same intent for a demo.
- If a panel needs `rate()` windows tuned for a short demo (60s of traffic is thin for a `[5m]` window), prefer shortening the window over widening the dashboard's time range — the reviewer will be looking at a few minutes of data at most.
