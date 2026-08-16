# M3 Task 07 — Metrics (Wallet + Projection) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the SPEC.md §7.3-required Prometheus metrics for the Wallet Service and Projection Service, at the exact names/labels the table specifies, so the M5 Grafana dashboard has something real to query against.

**Architecture:** Wallet Service gets a new `OncePerRequestFilter` (`MetricsFilter`) that records `wallet_requests_total` / `wallet_request_duration_seconds` for every dispatched request, plus a `MeterRegistry`-backed counter wired into `IdempotencyService.begin` for `wallet_idempotency_hits_total`. Projection Service already has `internal/metrics/metrics.go` wired into the Task 02 consumer loop (`projection_events_processed_total`, `projection_lag_seconds` + `max_projection_lag_seconds`, `projection_consumer_lag`) — the only real gap there is a counting-unit bug in `projection_events_processed_total`, fixed in Task 3 below.

**Tech Stack:** Java 26 / Spring Boot 4 / Micrometer + Prometheus registry (Wallet Service); Go 1.22 / `prometheus/client_golang` promauto (Projection Service).

## Global Constraints

- Metric names and label keys must be byte-exact per SPEC.md §7.3 — no substitute names, no dropped/renamed labels.
- The `endpoint` label on wallet HTTP metrics MUST be the route pattern (e.g. `/wallets/{walletId}/balance`), never the raw request path — raw paths are unbounded cardinality.
- `projection_lag_seconds` stays a histogram across all wallets (no per-wallet label) with a companion `max_projection_lag_seconds` gauge — this is already correctly implemented; do not add a per-wallet label to it.
- Do not remove or rename Micrometer's default `http_server_requests_*` metrics — the required `wallet_*` names are additive.
- Dashboards, alerts, and tracing are out of scope (M5). This task only makes the metrics exist and hold correct values.

---

## Context this plan assumes you know

- `services/wallet-service/src/main/java/com/ledger/wallet/api/filter/JwtAuthFilter.java` is the existing example of an `OncePerRequestFilter` registered as a plain `@Component` (Spring Boot auto-registers `Filter` beans as servlet filters at default order).
- `services/wallet-service/src/main/java/com/ledger/wallet/application/idempotency/IdempotencyService.java` has a single public entry point, `begin(UUID userId, String key, String fingerprint)`, returning a sealed `IdempotencyResult` (`New`, `Replay`, `Mismatch`, `InProgress`). All four call paths inside `begin`/`awaitCompletion` funnel through this one return, so wrapping `begin` itself is sufficient to observe every outcome exactly once.
- `services/projection-service/internal/consumer/apply.go`'s `applyEvent` currently returns `(applied, skipped int, err error)` counted **per ledger entry**, and `ledger_posted.go`'s `applyRecord` increments `metrics.EventsProcessedTotal` once per entry via `for range applied { ... Inc() }`. This is inconsistent with the `error` outcome, which is already incremented once per Kafka message (a poison message has no entries to count) — see decision below.
- `services/projection-service/tests/consumer_test.go`'s `TestConsumer_DuplicateDelivery_IsNoOp` currently asserts `skipped >= skippedBefore+2` for a redelivered 2-entry transfer — this assertion encodes the per-entry bug and must change to `+1`.

**Decision carried into this plan:** `projection_events_processed_total` counts once per `LEDGER_POSTED` **event** (i.e., per Kafka record), for all three `result` values (`applied`/`skipped`/`error`) — not once per ledger entry. Rationale: (1) internal consistency — `error` is already event-scoped, so `applied`/`skipped` must be too, or `sum by (result) (...)` is meaningless; (2) it composes with `projection_consumer_lag`, which is offset/event-scoped, letting throughput and lag be compared directly. Because all of one event's entries commit atomically in a single DB transaction (`applyEvent`), an event is never partially applied across attempts: on first delivery every entry is newly applied → `applied`; on redelivery every entry was already applied by the prior commit → `skipped`. Classifying the whole event by "was at least one entry newly applied" is therefore exact, not an approximation.

---

## Task 1: Wallet — `wallet_requests_total` / `wallet_request_duration_seconds`

**Files:**
- Create: `services/wallet-service/src/main/java/com/ledger/wallet/api/filter/MetricsFilter.java`
- Test: `services/wallet-service/src/test/java/com/ledger/wallet/api/filter/MetricsFilterIT.java`

**Interfaces:**
- Consumes: Spring's auto-configured `io.micrometer.core.instrument.MeterRegistry` bean (already present via the `spring-boot-starter-micrometer-metrics` dependency and `management.metrics.export.prometheus.enabled: true` in `application.yml`); `org.springframework.web.servlet.HandlerMapping.BEST_MATCHING_PATTERN_ATTRIBUTE` request attribute set by `DispatcherServlet` before a matched controller runs.
- Produces: a `Counter` named `wallet_requests` (renders as `wallet_requests_total` under Prometheus's counter-naming convention) tagged `endpoint`, `method`, `status`; a `Timer` named `wallet_request_duration` (renders as `wallet_request_duration_seconds{...}` bucket/sum/count series) tagged `endpoint`, `method`. No other task in this plan depends on this filter's internals — only on the metric names existing.

- [ ] **Step 1: Write the failing integration test**

```java
// services/wallet-service/src/test/java/com/ledger/wallet/api/filter/MetricsFilterIT.java
package com.ledger.wallet.api.filter;

import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.MediaType;

import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

class MetricsFilterIT extends BaseIntegrationTest {

    @Autowired
    private MeterRegistry meterRegistry;

    @Test
    void createWallet_recordsRequestCounterAndDurationWithRoutePatternLabel() throws Exception {
        UUID ownerId = UUID.randomUUID();

        mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(ownerId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"currency\":\"BRL\"}"))
                .andExpect(status().isCreated());

        double count = meterRegistry
                .counter("wallet_requests", "endpoint", "/wallets", "method", "POST", "status", "201")
                .count();
        assertThat(count).isGreaterThanOrEqualTo(1.0);

        Timer timer = meterRegistry.find("wallet_request_duration")
                .tag("endpoint", "/wallets")
                .tag("method", "POST")
                .timer();
        assertThat(timer).isNotNull();
        assertThat(timer.count()).isGreaterThanOrEqualTo(1);
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/wallet-service && ./mvnw test -Dtest=MetricsFilterIT`
Expected: FAIL — `MetricsFilter` class does not exist yet (compilation error), or if a stub is created without recording, the counter lookup returns `0.0`/the timer is `null`.

- [ ] **Step 3: Write the implementation**

```java
// services/wallet-service/src/main/java/com/ledger/wallet/api/filter/MetricsFilter.java
package com.ledger.wallet.api.filter;

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.stereotype.Component;
import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.servlet.HandlerMapping;

import java.io.IOException;
import java.util.concurrent.TimeUnit;

/**
 * SPEC.md §7.3: wallet_requests_total / wallet_request_duration_seconds. Registered as a plain
 * servlet filter (default order), so it wraps the dispatch to the controller and observes the
 * final response status set by GlobalExceptionHandler — but runs after the Spring Security chain
 * (JwtAuthFilter), so a request rejected there never reaches this filter.
 */
@Component
public class MetricsFilter extends OncePerRequestFilter {

    private final MeterRegistry meterRegistry;

    public MetricsFilter(MeterRegistry meterRegistry) {
        this.meterRegistry = meterRegistry;
    }

    @Override
    protected void doFilterInternal(
            HttpServletRequest request,
            HttpServletResponse response,
            FilterChain chain
    ) throws ServletException, IOException {
        long startNanos = System.nanoTime();
        try {
            chain.doFilter(request, response);
        } finally {
            String endpoint = endpointLabel(request);
            String method = request.getMethod();
            String status = String.valueOf(response.getStatus());

            meterRegistry.counter("wallet_requests", "endpoint", endpoint, "method", method, "status", status)
                    .increment();
            Timer.builder("wallet_request_duration")
                    .tag("endpoint", endpoint)
                    .tag("method", method)
                    .publishPercentileHistogram()
                    .register(meterRegistry)
                    .record(System.nanoTime() - startNanos, TimeUnit.NANOSECONDS);
        }
    }

    private String endpointLabel(HttpServletRequest request) {
        Object pattern = request.getAttribute(HandlerMapping.BEST_MATCHING_PATTERN_ATTRIBUTE);
        return pattern != null ? pattern.toString() : "UNMATCHED";
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/wallet-service && ./mvnw test -Dtest=MetricsFilterIT`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/wallet-service/src/main/java/com/ledger/wallet/api/filter/MetricsFilter.java \
        services/wallet-service/src/test/java/com/ledger/wallet/api/filter/MetricsFilterIT.java
git commit -m "feat(wallet-service): add wallet_requests_total / wallet_request_duration_seconds metrics"
```

---

## Task 2: Wallet — `wallet_idempotency_hits_total`

**Files:**
- Modify: `services/wallet-service/src/main/java/com/ledger/wallet/application/idempotency/IdempotencyService.java`
- Test: `services/wallet-service/src/test/java/com/ledger/wallet/application/idempotency/IdempotencyServiceTest.java`
- Test: `services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java` (extend one existing test for end-to-end proof)

**Interfaces:**
- Consumes: `io.micrometer.core.instrument.MeterRegistry` (Spring-provided in production; `io.micrometer.core.instrument.simple.SimpleMeterRegistry` — a real, in-memory Micrometer registry, not a mock — in the existing package-private test constructor's default).
- Produces: a `Counter` named `wallet_idempotency_hits_total` (declared with the `_total` suffix already in the name — Prometheus's counter-naming convention only appends `_total` when the name doesn't already end with it, so this renders unchanged) tagged `result` ∈ {`new`, `replay`, `mismatch`}. `InProgress` results are deliberately NOT counted — SPEC.md's M3 Task 07 doc lists only `replay`/`mismatch`/`new` as the label's values.
- The existing package-private 4-arg constructor `IdempotencyService(IdempotencyRepository, Duration, Duration, Duration)` keeps working unchanged (delegates to a fresh `SimpleMeterRegistry`), so none of the ~12 existing call sites in `IdempotencyServiceTest` need to change.

- [ ] **Step 1: Write the failing unit tests**

Add to `services/wallet-service/src/test/java/com/ledger/wallet/application/idempotency/IdempotencyServiceTest.java`, just after the existing `import` block (add one new import) and as new `@Test` methods appended at the end of the class, before the final closing brace:

```java
// new import, alongside the existing ones at the top of the file:
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
```

```java
    @Test
    void begin_new_incrementsIdempotencyHitsCounterWithNewResult() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        SimpleMeterRegistry meterRegistry = new SimpleMeterRegistry();
        IdempotencyService service = new IdempotencyService(repository, TTL, MAX_WAIT, POLL_INTERVAL, meterRegistry);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "metrics-key-new", FINGERPRINT);

        assertThat(meterRegistry.counter("wallet_idempotency_hits_total", "result", "new").count())
                .isEqualTo(1.0);
    }

    @Test
    void begin_replay_incrementsIdempotencyHitsCounterWithReplayResult() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        SimpleMeterRegistry meterRegistry = new SimpleMeterRegistry();
        IdempotencyService service = new IdempotencyService(repository, TTL, MAX_WAIT, POLL_INTERVAL, meterRegistry);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "metrics-key-replay", FINGERPRINT);
        service.complete(userId, "metrics-key-replay", 201, "{}");
        service.begin(userId, "metrics-key-replay", FINGERPRINT);

        assertThat(meterRegistry.counter("wallet_idempotency_hits_total", "result", "replay").count())
                .isEqualTo(1.0);
    }

    @Test
    void begin_mismatch_incrementsIdempotencyHitsCounterWithMismatchResult() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        SimpleMeterRegistry meterRegistry = new SimpleMeterRegistry();
        IdempotencyService service = new IdempotencyService(repository, TTL, MAX_WAIT, POLL_INTERVAL, meterRegistry);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "metrics-key-mismatch", FINGERPRINT);
        service.begin(userId, "metrics-key-mismatch", OTHER_FINGERPRINT);

        assertThat(meterRegistry.counter("wallet_idempotency_hits_total", "result", "mismatch").count())
                .isEqualTo(1.0);
    }

    @Test
    void begin_inProgress_doesNotIncrementIdempotencyHitsCounter() {
        InMemoryIdempotencyRepository repository = new InMemoryIdempotencyRepository();
        SimpleMeterRegistry meterRegistry = new SimpleMeterRegistry();
        IdempotencyService service = new IdempotencyService(repository, TTL, MAX_WAIT, POLL_INTERVAL, meterRegistry);
        UUID userId = UUID.randomUUID();

        service.begin(userId, "metrics-key-inprogress", FINGERPRINT);
        service.begin(userId, "metrics-key-inprogress", FINGERPRINT); // times out -> InProgress

        assertThat(meterRegistry.find("wallet_idempotency_hits_total").tag("result", "in_progress").counter())
                .isNull();
    }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd services/wallet-service && ./mvnw test -Dtest=IdempotencyServiceTest`
Expected: FAIL — compilation error, since the 5-arg `IdempotencyService(..., MeterRegistry)` constructor doesn't exist yet.

- [ ] **Step 3: Modify `IdempotencyService.java`**

Replace the constructors block (lines 30–40 of the current file):

```java
    private final IdempotencyRepository repository;
    private final Duration ttl;
    private final Duration maxWait;
    private final Duration pollInterval;

    @Autowired
    public IdempotencyService(IdempotencyRepository repository, IdempotencyProperties properties) {
        this(repository, Duration.ofHours(properties.ttlHours()), DEFAULT_MAX_WAIT, DEFAULT_POLL_INTERVAL);
    }

    IdempotencyService(IdempotencyRepository repository, Duration ttl, Duration maxWait, Duration pollInterval) {
        this.repository = repository;
        this.ttl = ttl;
        this.maxWait = maxWait;
        this.pollInterval = pollInterval;
    }
```

with:

```java
    private final IdempotencyRepository repository;
    private final Duration ttl;
    private final Duration maxWait;
    private final Duration pollInterval;
    private final MeterRegistry meterRegistry;

    @Autowired
    public IdempotencyService(IdempotencyRepository repository, IdempotencyProperties properties, MeterRegistry meterRegistry) {
        this(repository, Duration.ofHours(properties.ttlHours()), DEFAULT_MAX_WAIT, DEFAULT_POLL_INTERVAL, meterRegistry);
    }

    // Existing 4-arg test constructor kept intact so IdempotencyServiceTest's ~12 call sites don't
    // need to change: it gets a fresh, real (not mocked) in-memory registry it never has to assert on.
    IdempotencyService(IdempotencyRepository repository, Duration ttl, Duration maxWait, Duration pollInterval) {
        this(repository, ttl, maxWait, pollInterval, new SimpleMeterRegistry());
    }

    IdempotencyService(IdempotencyRepository repository, Duration ttl, Duration maxWait, Duration pollInterval, MeterRegistry meterRegistry) {
        this.repository = repository;
        this.ttl = ttl;
        this.maxWait = maxWait;
        this.pollInterval = pollInterval;
        this.meterRegistry = meterRegistry;
    }
```

Add two imports at the top of the file, alongside the existing `org.springframework.dao.DuplicateKeyException` import:

```java
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
```

Replace the `begin` method (current lines 42–62):

```java
    public IdempotencyResult begin(UUID userId, String key, String fingerprint) {
        Optional<IdempotencyRecord> existing = repository.find(userId, key);
        if (existing.isEmpty()) {
            return attemptInsert(userId, key, fingerprint, InsertMode.FRESH);
        }

        IdempotencyRecord record = existing.get();
        if (isStale(record)) {
            return attemptInsert(userId, key, fingerprint, InsertMode.REPLACE_STALE);
        }

        if (!record.requestFingerprint().equals(fingerprint)) {
            return new IdempotencyResult.Mismatch();
        }

        return switch (record.status()) {
            case COMPLETED -> new IdempotencyResult.Replay(record.responseStatus(), record.responseBody());
            case PENDING -> awaitCompletion(userId, key);
            case FAILED -> throw new IllegalStateException("unreachable: FAILED records are always stale");
        };
    }
```

with:

```java
    public IdempotencyResult begin(UUID userId, String key, String fingerprint) {
        IdempotencyResult result = doBegin(userId, key, fingerprint);
        recordHit(result);
        return result;
    }

    private IdempotencyResult doBegin(UUID userId, String key, String fingerprint) {
        Optional<IdempotencyRecord> existing = repository.find(userId, key);
        if (existing.isEmpty()) {
            return attemptInsert(userId, key, fingerprint, InsertMode.FRESH);
        }

        IdempotencyRecord record = existing.get();
        if (isStale(record)) {
            return attemptInsert(userId, key, fingerprint, InsertMode.REPLACE_STALE);
        }

        if (!record.requestFingerprint().equals(fingerprint)) {
            return new IdempotencyResult.Mismatch();
        }

        return switch (record.status()) {
            case COMPLETED -> new IdempotencyResult.Replay(record.responseStatus(), record.responseBody());
            case PENDING -> awaitCompletion(userId, key);
            case FAILED -> throw new IllegalStateException("unreachable: FAILED records are always stale");
        };
    }

    /** SPEC.md §7.3: wallet_idempotency_hits_total{result=new|replay|mismatch}. InProgress is not a
     * "hit" against a cached response, so it's deliberately left uncounted. */
    private void recordHit(IdempotencyResult result) {
        String label = switch (result) {
            case IdempotencyResult.New ignored -> "new";
            case IdempotencyResult.Replay ignored -> "replay";
            case IdempotencyResult.Mismatch ignored -> "mismatch";
            case IdempotencyResult.InProgress ignored -> null;
        };
        if (label == null) {
            return;
        }
        meterRegistry.counter("wallet_idempotency_hits_total", "result", label).increment();
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd services/wallet-service && ./mvnw test -Dtest=IdempotencyServiceTest`
Expected: PASS (all previously-existing tests too — the 4-arg constructor's behavior is unchanged)

- [ ] **Step 5: Extend `TransferControllerIT`'s mismatch test for end-to-end proof**

In `services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java`, add one import:

```java
import io.micrometer.core.instrument.MeterRegistry;
import org.springframework.beans.factory.annotation.Autowired;
```

Add a field just below the `LEDGER` static `WireMockServer` field:

```java
    @Autowired
    private MeterRegistry meterRegistry;
```

Replace the `sameKeyDifferentBody_returns422IdempotencyKeyMismatch` test body's final assertion block:

```java
        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "999.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("IDEMPOTENCY_KEY_MISMATCH"));
    }
```

with:

```java
        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "999.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("IDEMPOTENCY_KEY_MISMATCH"));

        assertThat(meterRegistry.counter("wallet_idempotency_hits_total", "result", "mismatch").count())
                .isGreaterThanOrEqualTo(1.0);
        assertThat(meterRegistry.counter("wallet_idempotency_hits_total", "result", "new").count())
                .isGreaterThanOrEqualTo(1.0);
    }
```

Run: `cd services/wallet-service && ./mvnw test -Dtest=TransferControllerIT`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add services/wallet-service/src/main/java/com/ledger/wallet/application/idempotency/IdempotencyService.java \
        services/wallet-service/src/test/java/com/ledger/wallet/application/idempotency/IdempotencyServiceTest.java \
        services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java
git commit -m "feat(wallet-service): add wallet_idempotency_hits_total metric to IdempotencyService.begin"
```

---

## Task 3: Projection — count `projection_events_processed_total` per event, not per entry

**Files:**
- Modify: `services/projection-service/internal/consumer/apply.go`
- Modify: `services/projection-service/internal/consumer/ledger_posted.go`
- Test: `services/projection-service/tests/consumer_test.go`

**Interfaces:**
- Consumes: nothing new (already-existing `metrics.EventsProcessedTotal *prometheus.CounterVec` from `internal/metrics/metrics.go`).
- Produces: `applyEvent` changes its return signature from `(applied, skipped int, err error)` to `(result string, err error)` where `result` ∈ {`"applied"`, `"skipped"`} — this is the only caller-visible change, and `ledger_posted.go`'s `applyRecord` is the only caller.

- [ ] **Step 1: Update the existing integration test's assertion to the new (correct) semantics**

In `services/projection-service/tests/consumer_test.go`, in `TestConsumer_DuplicateDelivery_IsNoOp`, change:

```go
	tickUntil(t, second, func() bool {
		return testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("skipped")) >= skippedBefore+2
	})
```

to:

```go
	tickUntil(t, second, func() bool {
		return testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("skipped")) >= skippedBefore+1
	})
```

- [ ] **Step 2: Add a precise assertion that fails against the current per-entry implementation**

The `tickUntil(..., >= skippedBefore+1)` condition alone won't distinguish old from new behavior (`+2 >= +1` is also true). Add a second, exact assertion directly after the `tickUntil` call in `TestConsumer_DuplicateDelivery_IsNoOp`:

```go
	if got := testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("skipped")); got != skippedBefore+1 {
		t.Errorf("EventsProcessedTotal{result=skipped} after one redelivered 2-entry event = %v, want exactly %v (one increment per event, not per entry)", got, skippedBefore+1)
	}
```

Run: `cd services/projection-service && go test ./tests/... -run TestConsumer_DuplicateDelivery_IsNoOp -v`
Expected: FAIL — `got = skippedBefore+2, want skippedBefore+1`

- [ ] **Step 3: Modify `apply.go`'s `applyEvent`**

Replace (current lines 67–98):

```go
// applyEvent applies every entry in event within one DB transaction, records
// the consumer's last-processed offset in the same transaction, and commits.
// applied/skipped count entries, not events: a redelivered two-entry
// transfer reports skipped=2, applied=0.
func applyEvent(ctx context.Context, pool *pgxpool.Pool, event ledgerPostedEvent, group, topic string, partition int32, offset int64) (applied, skipped int, err error) {
	dbtx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	for _, e := range event.Entries {
		ok, err := applyEntry(ctx, dbtx, e.AccountID, e.EntryID, signedDelta(e.EntryType, e.Amount))
		if err != nil {
			return 0, 0, err
		}
		if ok {
			applied++
		} else {
			skipped++
		}
	}

	if err := upsertOffset(ctx, dbtx, group, topic, partition, offset); err != nil {
		return 0, 0, err
	}

	if err := dbtx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit projection apply: %w", err)
	}
	return applied, skipped, nil
}
```

with:

```go
// applyEvent applies every entry in event within one DB transaction, records
// the consumer's last-processed offset in the same transaction, and commits.
// The whole event is classified as a single outcome, "applied" or "skipped" —
// matching the granularity of metrics.EventsProcessedTotal's "error" label,
// which is already per-message (a poison message has no entries to count).
// Because every entry in one event commits together in this one transaction,
// an event is never partially applied across delivery attempts: on first
// delivery every entry is newly applied ("applied"); on redelivery every
// entry was already applied by the prior commit ("skipped").
func applyEvent(ctx context.Context, pool *pgxpool.Pool, event ledgerPostedEvent, group, topic string, partition int32, offset int64) (result string, err error) {
	dbtx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) //nolint:errcheck

	anyApplied := false
	for _, e := range event.Entries {
		ok, err := applyEntry(ctx, dbtx, e.AccountID, e.EntryID, signedDelta(e.EntryType, e.Amount))
		if err != nil {
			return "", err
		}
		if ok {
			anyApplied = true
		}
	}

	if err := upsertOffset(ctx, dbtx, group, topic, partition, offset); err != nil {
		return "", err
	}

	if err := dbtx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit projection apply: %w", err)
	}
	if anyApplied {
		return "applied", nil
	}
	return "skipped", nil
}
```

- [ ] **Step 4: Modify `ledger_posted.go`'s call site**

Replace (current lines 124–134):

```go
	applied, skipped, err := applyEvent(ctx, c.pool, event, c.groupID, r.Topic, r.Partition, r.Offset)
	if err != nil {
		log.Printf("projection consumer: apply transaction %s failed, will retry: %v", event.TransactionID, err)
		return
	}
	for range applied {
		metrics.EventsProcessedTotal.WithLabelValues("applied").Inc()
	}
	for range skipped {
		metrics.EventsProcessedTotal.WithLabelValues("skipped").Inc()
	}
```

with:

```go
	result, err := applyEvent(ctx, c.pool, event, c.groupID, r.Topic, r.Partition, r.Offset)
	if err != nil {
		log.Printf("projection consumer: apply transaction %s failed, will retry: %v", event.TransactionID, err)
		return
	}
	metrics.EventsProcessedTotal.WithLabelValues(result).Inc()
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd services/projection-service && go test ./tests/... -run TestConsumer_DuplicateDelivery_IsNoOp -v`
Expected: PASS

- [ ] **Step 6: Run the full projection-service test suite to check for regressions**

Run: `cd services/projection-service && go build ./... && go test ./...`
Expected: PASS — `go build` catches any other caller of `applyEvent`'s old 3-return signature (there should be none besides `ledger_posted.go`); `go test ./internal/...` (unit) runs without Docker, `go test ./tests/...` needs Docker for Testcontainers-backed Postgres/Kafka.

- [ ] **Step 7: Commit**

```bash
git add services/projection-service/internal/consumer/apply.go \
        services/projection-service/internal/consumer/ledger_posted.go \
        services/projection-service/tests/consumer_test.go
git commit -m "fix(projection-service): count projection_events_processed_total per event, not per entry"
```

---

## Task 4: End-to-end verification against the running stack

**Files:** none — this task only runs commands and inspects output, per the milestone doc's own acceptance criteria table.

- [ ] **Step 1: Bring up the stack**

Run: `make down && make up`
Expected: all services report healthy within 60s (per SPEC.md §1.1).

- [ ] **Step 2: Run the M3 demo script's steps 1–3** (from `docs/milestones/milestone-3/00-overview.md`'s "Demo script" section) to create two wallets, seed the source wallet via a direct ledger deposit posting, and post one transfer with an `Idempotency-Key`.

- [ ] **Step 3: Curl the Wallet Service's Prometheus endpoint and grep for the required series**

Run:
```sh
curl -s http://localhost:8080/actuator/prometheus | grep -E '^wallet_requests_total\{|^wallet_request_duration_seconds_(bucket|sum|count)\{|^wallet_idempotency_hits_total\{'
```
Expected: at least one `wallet_requests_total{endpoint="/transfers",method="POST",status="201",...}` line with value `>= 1`; `wallet_request_duration_seconds_count` and `_sum` present for `endpoint="/transfers"`; `wallet_idempotency_hits_total{result="new"}` present.

- [ ] **Step 4: Re-run the same transfer request with the same `Idempotency-Key` (replay), then again with a different amount (mismatch), then re-curl**

Run:
```sh
curl -s http://localhost:8080/actuator/prometheus | grep -E '^wallet_idempotency_hits_total\{'
```
Expected: `{result="new"}`, `{result="replay"}`, and `{result="mismatch"}` each present with value `>= 1`.

- [ ] **Step 5: Curl the Projection Service's metrics endpoint after the transfer above has been consumed**

Run:
```sh
curl -s http://localhost:8082/metrics | grep -E '^projection_events_processed_total\{|^projection_lag_seconds_(bucket|sum|count)|^max_projection_lag_seconds |^projection_consumer_lag\{'
```
Expected: `projection_events_processed_total{result="applied"}` incremented by exactly `1` for the one transfer just posted (not `2`); `projection_lag_seconds_count` has at least one observation; `max_projection_lag_seconds` is a small positive number; `projection_consumer_lag{topic="ledger.posted.v1",...}` is present (value near `0` once caught up).

- [ ] **Step 6: Re-deliver a duplicate event to confirm the `skipped` path**

This is already exercised by `TestConsumer_DuplicateDelivery_IsNoOp` (Task 3); no separate manual step needed unless you want to watch it live — if so, re-run the same `LEDGER_POSTED` Kafka message (e.g. via `kafkactl` or the `tests/e2e` suite once Task 08 exists) and re-curl for `projection_events_processed_total{result="skipped"}`.

- [ ] **Step 7: Tear down**

Run: `make down`

No commit for this task — it's verification only. If any step's `curl` output doesn't match, return to the relevant task above and fix before considering Task 07 done.

---

## Definition of done (cross-check against the milestone doc)

- [ ] `wallet_requests_total{endpoint,method,status}` and `wallet_request_duration_seconds{endpoint,method}` exist and are populated by real traffic (Task 1).
- [ ] `wallet_idempotency_hits_total{result=new|replay|mismatch}` exists and is populated by `IdempotencyService.begin` (Task 2).
- [ ] `projection_events_processed_total{result=applied|skipped|error}` counts once per Kafka event across all three outcomes (Task 3).
- [ ] `projection_lag_seconds` (histogram) + `max_projection_lag_seconds` (gauge) + `projection_consumer_lag{topic,partition}` (gauge) — already correct, reconfirmed in Task 4.
- [ ] All metric names are byte-exact per SPEC.md §7.3 (Task 4, `grep` against live output).
- [ ] No dashboards, alerts, or tracing added — out of scope for M3 Task 07.
