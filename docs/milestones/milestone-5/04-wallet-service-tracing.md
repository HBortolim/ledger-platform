# Task 04 — Wallet Service tracing and outbound propagation

**Status:** Not started
**Owner:** Wallet Service
**Depends on:** 01 (a collector to export to). Independent of Tasks 02/03 — this is pure Java and can run in parallel with them.
**Blocks:** 05 (the ledger's captured span context must have a wallet-side parent, or the "end-to-end" trace starts in the wrong place)
**Spec reference:** [`SPEC.md` NFR-OBS-1, NFR-OBS-2, NFR-OBS-5](../../SPEC.md)

---

## Goal

The Wallet Service already has the `spring-boot-starter-opentelemetry` dependency and a JSON log pattern with `%X{traceId}`/`%X{spanId}` slots — but nothing is configured, so no spans are produced and those slots always render empty. This task turns tracing on and makes the outbound call to the Ledger Service carry a W3C `traceparent`, which is what stitches the wallet and ledger halves of the trace together.

## The `RestClient.builder()` trap

This is the whole reason the task is more than a config change.

`LedgerClientConfig` builds its client from the **static factory**:

```java
return RestClient.builder()          // <-- static: no instrumentation
        .baseUrl(baseUrl)
        .requestFactory(requestFactory)
        .build();
```

Spring's observation instrumentation — the thing that injects `traceparent` on outbound requests — is applied to the **auto-configured `RestClient.Builder` bean**, not to the static factory. A client built the current way produces no client spans and sends no trace context, so the ledger's span would start a brand-new trace and NFR-OBS-5's single end-to-end trace would silently become two disconnected ones.

The fix is to inject the builder bean. The static helper stays for tests, which don't need instrumentation.

## Step 1: Configure tracing

**Files:**
- Modify: `services/wallet-service/src/main/resources/application.yml`

Add a `tracing` block under `management`, and an `otlp.tracing` sibling to the existing `otlp.metrics`:

```yaml
management:
  endpoints:
    web:
      exposure:
        include: health,prometheus,info
  endpoint:
    health:
      probes:
        enabled: true
  metrics:
    export:
      prometheus:
        enabled: true
  tracing:
    sampling:
      # Always-on: a demo that drops the reviewer's one transfer is worthless
      # (milestone-5 overview, Global Constraints). Production would use a
      # parent-based ratio sampler.
      probability: ${MANAGEMENT_TRACING_SAMPLING_PROBABILITY:1.0}
  otlp:
    tracing:
      # Spring's OTLP exporter speaks http/protobuf, so this targets the
      # collector's 4318 receiver -- not 4317, which is the gRPC port the Go
      # services use.
      endpoint: ${MANAGEMENT_OTLP_TRACING_ENDPOINT:}
    metrics:
      export:
        enabled: ${OTLP_METRICS_ENABLED:false}
        url: ${OTEL_EXPORTER_OTLP_METRICS_ENDPOINT:http://localhost:4318/v1/metrics}
```

Two deliberate details:

- The tracing endpoint **defaults to empty**, not to a localhost URL. Task 01 sets `MANAGEMENT_OTLP_TRACING_ENDPOINT` in `docker-compose.yml`; outside compose (local runs, tests) the exporter stays unconfigured and nothing tries to reach a collector that isn't there — overview decision #3.
- Delete the now-redundant `otel.exporter.otlp.endpoint` block at the bottom of the file if nothing else reads it. It points at `:4317` (gRPC), which is the wrong port for Spring's http/protobuf exporter and would mislead the next reader. Confirm with `grep -rn "otel.exporter" services/wallet-service/src` before removing.

- [ ] Make the edits.

## Step 2: Fix the RestClient wiring

**Files:**
- Modify: `services/wallet-service/src/main/java/com/ledger/wallet/infrastructure/ledger/LedgerClientConfig.java`

```java
package com.ledger.wallet.infrastructure.ledger;

import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.client.JdkClientHttpRequestFactory;
import org.springframework.web.client.RestClient;

import java.net.http.HttpClient;
import java.time.Duration;

@Configuration
@EnableConfigurationProperties(LedgerServiceProperties.class)
public class LedgerClientConfig {

    private static final Duration CONNECT_TIMEOUT = Duration.ofSeconds(2);
    private static final Duration READ_TIMEOUT = Duration.ofSeconds(5);

    /**
     * Built from the auto-configured {@link RestClient.Builder} bean rather than the
     * {@code RestClient.builder()} static factory: only the bean carries Spring's
     * observation instrumentation, which is what injects the W3C {@code traceparent}
     * header on the outbound call. Building from the static factory produces a client
     * that works perfectly but emits no client span and propagates no trace context —
     * splitting what NFR-OBS-5 requires to be one end-to-end trace into two.
     */
    @Bean
    public RestClient ledgerRestClient(RestClient.Builder builder, LedgerServiceProperties props) {
        return builder
                .baseUrl(props.url())
                .requestFactory(jdkRequestFactory(CONNECT_TIMEOUT, READ_TIMEOUT))
                .build();
    }

    /**
     * Package-visible so tests can build a client against a WireMock server with short
     * timeouts, without duplicating the request-factory wiring. Tests do not need — and
     * should not depend on — observation instrumentation, so the static factory is correct here.
     */
    static RestClient buildRestClient(String baseUrl, Duration connectTimeout, Duration readTimeout) {
        return RestClient.builder()
                .baseUrl(baseUrl)
                .requestFactory(jdkRequestFactory(connectTimeout, readTimeout))
                .build();
    }

    private static JdkClientHttpRequestFactory jdkRequestFactory(Duration connectTimeout, Duration readTimeout) {
        HttpClient httpClient = HttpClient.newBuilder()
                .connectTimeout(connectTimeout)
                // The Ledger Service (Go/Gin's net/http) speaks plain HTTP/1.1 only.
                // Without pinning this, the JDK client's HTTP/2-upgrade attempt against
                // an HTTP/1.1-only server intermittently resets the connection.
                .version(HttpClient.Version.HTTP_1_1)
                .build();
        JdkClientHttpRequestFactory requestFactory = new JdkClientHttpRequestFactory(httpClient);
        requestFactory.setReadTimeout(readTimeout);
        return requestFactory;
    }
}
```

The HTTP/1.1 pinning is load-bearing — it's shared by both paths through `jdkRequestFactory` rather than duplicated, and must not be dropped.

- [ ] Make the edit.
- [ ] Run: `cd services/wallet-service && ./mvnw -q compile` — expect success.

## Step 3: Write the failing test for outbound propagation

**Files:**
- Modify: `services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java`

Add this test. It is the regression guard for the Step 2 trap: revert `ledgerRestClient` to the static factory and it must fail.

```java
    @Test
    void outboundLedgerCall_carriesW3CTraceparentHeader() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        stubPosted("55555555-5555-5555-5555-555555555555");

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "100.00")))
                .andExpect(status().isCreated());

        // NFR-OBS-2: W3C traceparent, format 00-<32 hex trace>-<16 hex span>-<2 hex flags>.
        // Guards the RestClient.builder() trap: the static factory produces an
        // uninstrumented client that sends no traceparent at all.
        LEDGER.verify(postRequestedFor(urlEqualTo("/ledger/postings"))
                .withHeader("traceparent", matching("^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$")));
    }
```

Add the required static import alongside the existing WireMock ones:

```java
import static com.github.tomakehurst.wiremock.client.WireMock.matching;
```

- [ ] Add the test and the import.
- [ ] Run: `./mvnw verify -Dit.test=TransferControllerIT -DfailIfNoTests=true`
- [ ] Expected: **PASS**, because Step 2 is already done. To confirm the test actually has teeth, temporarily revert `ledgerRestClient` to `buildRestClient(props.url(), CONNECT_TIMEOUT, READ_TIMEOUT)`, re-run, and watch it fail on a missing `traceparent` header. Restore Step 2 afterwards and note both outcomes in your report — a green test that would also pass against the bug is worth nothing here.

**If the test fails even with Step 2 in place**, the likely cause is that no tracer is active in the test context (no exporter configured means Boot may not create a `Tracer`). Diagnose before working around it: check whether a `Tracer` bean exists in the context. If tracing genuinely requires an exporter to activate, add a test-scoped property that enables a no-op/in-memory exporter rather than deleting the assertion — and record what you found. Do **not** weaken the regex or drop the test.

## Step 4: Verify trace IDs reach the logs

The log pattern at `application.yml:44` already has `%X{traceId}`/`%X{spanId}`; Micrometer Tracing populates that MDC automatically once a tracer is active. This step confirms it.

- [ ] Run: `make down && make up-obs`
- [ ] Run a transfer (steps 1–2 of the milestone overview's demo script).
- [ ] Run: `docker compose logs --no-log-prefix wallet-service | jq -r 'select(.trace_id != "") | "\(.trace_id) \(.msg)"' | tail`
- [ ] Expect non-empty `trace_id` values on request-scoped log lines. Startup lines will still show empty — correct, there's no request in flight.

## Step 5: Verify the span reaches Jaeger

- [ ] Open `http://localhost:16686`, select service **`wallet-service`**, Find Traces.
- [ ] Expect a trace with a `POST /transfers` server span.
- [ ] If Task 03 is already merged, that same trace should also contain `ledger-service` spans — the traceparent from Step 2 is what links them. Confirm this if so; it is the first visible proof of cross-service correlation. (If Task 03 is not yet merged, a wallet-only trace is the correct result here.)

## Step 6: Full suite and commit

- [ ] Run: `./mvnw verify` — expect the full unit + integration suite green (86 unit + 51 integration as of M4, plus the new test).
- [ ] Commit:

```bash
git add services/wallet-service/src/main/resources/application.yml \
        services/wallet-service/src/main/java/com/ledger/wallet/infrastructure/ledger/LedgerClientConfig.java \
        services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferControllerIT.java
git commit -m "feat(wallet-service): enable tracing and propagate traceparent to the ledger

NFR-OBS-2/5. The RestClient was built from the static RestClient.builder()
factory, which carries no observation instrumentation -- so the outbound
posting call sent no trace context and the ledger's span would have started
a separate trace. Now built from the auto-configured RestClient.Builder bean,
with an IT asserting a well-formed W3C traceparent on the outbound request."
```

## Acceptance criteria

| Check | Expected |
|---|---|
| `outboundLedgerCall_carriesW3CTraceparentHeader` | passes; **and** demonstrably fails when `ledgerRestClient` is reverted to the static factory |
| `./mvnw verify` | full suite green |
| Wallet log lines during a request | non-empty `trace_id` |
| Jaeger, service `wallet-service` | `POST /transfers` server span present |
| Local run with no `MANAGEMENT_OTLP_TRACING_ENDPOINT` | service starts normally, no connection errors |

## Done when

A transfer produces a `wallet-service` trace in Jaeger, wallet log lines carry that trace's ID, and the outbound-`traceparent` IT passes while provably failing against the old wiring.

## Notes

- Don't add the OpenTelemetry **Java agent**. The starter-based (Micrometer Tracing) setup already in the pom is sufficient and keeps the Dockerfile unchanged; an agent would be a second, competing instrumentation path.
- If `management.otlp.tracing.endpoint` turns out to be spelled differently in this Boot version, follow the version's actual property and note the deviation — the requirement is http/protobuf export to the collector's 4318, not a specific property name.
- Leave `management.otlp.metrics.export.enabled` at its `false` default. Metrics reach Prometheus by scraping `/actuator/prometheus` (already working, already in `prometheus.yml`); pushing them over OTLP as well would double-count.
