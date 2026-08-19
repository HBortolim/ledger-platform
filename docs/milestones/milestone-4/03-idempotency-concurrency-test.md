# Task 03 — Idempotency under concurrency (TST-CONCURRENCY-4)

**Status:** Not started
**Owner:** Wallet Service
**Depends on:** nothing new — builds on M3 Task 04's `IdempotencyService` and M3 Task 05's `TransferController`
**Blocks:** Task 04
**Spec reference:** [`SPEC.md` §10.4](../../SPEC.md) (TST-CONCURRENCY-4), AC-5.12, AC-5.16, `IdempotencyService.attemptInsert`'s `DuplicateKeyException` handling

---

## Goal

Prove that when the same `Idempotency-Key` arrives 20 times at once — not sequentially, actually concurrently — exactly one of them creates the ledger transaction and all 20 callers get back the identical response. This is the one TST-CONCURRENCY scenario that lives in the Wallet Service, not the Ledger Service: idempotency is owned there (§5.2's ownership table), and the guarantee under test is the `(user_id, key)` primary key's unique-constraint race handling in `IdempotencyService.attemptInsert`, not the ledger's account locking.

## The `@Transactional` trap

Every existing IT extends `BaseIntegrationTest`, which is annotated `@Transactional` at the class level: each test method runs inside one Spring-managed transaction on the **test method's own thread**, rolled back afterward. That's exactly wrong for this test — worker threads spawned inside the test get their own JDBC connections and can't see uncommitted rows the main thread created (e.g. the wallets the test just set up via `mockMvc.perform(post("/wallets")...)`), so cross-thread concurrency wouldn't really be exercised; it would either 404 on wallet lookups or, worse, silently pass without proving anything.

The fix: keep extending `BaseIntegrationTest` (reuse its singleton Postgres container and Spring context), but mark the concurrency test method `@Transactional(propagation = Propagation.NOT_SUPPORTED)` so it runs outside any test-managed transaction — every DB write, from every thread, commits for real and is visible to every other thread immediately.

## Step 1: Write the test

**Files:**
- Create: `services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferIdempotencyConcurrencyIT.java`

```java
package com.ledger.wallet.api.controller;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.transaction.annotation.Propagation;
import org.springframework.transaction.annotation.Transactional;

import java.util.List;
import java.util.UUID;
import java.util.concurrent.Callable;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.stream.Collectors;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

// TST-CONCURRENCY-4: the same Idempotency-Key submitted 20 times in parallel must produce
// exactly one ledger posting and 20 byte-identical responses. Deliberately does NOT extend
// BaseIntegrationTest's class-level @Transactional behavior for its one test method (see the
// overview's decision #3 and this class's javadoc) -- worker threads need real, independently
// committed transactions to exercise the actual (user_id, key) unique-constraint race.
class TransferIdempotencyConcurrencyIT extends BaseIntegrationTest {

    private static final WireMockServer LEDGER = new WireMockServer(0);

    static {
        LEDGER.start();
    }

    @DynamicPropertySource
    static void ledgerServiceUrl(DynamicPropertyRegistry registry) {
        registry.add("ledger.service.url", LEDGER::baseUrl);
    }

    @Test
    @Transactional(propagation = Propagation.NOT_SUPPORTED)
    void sameIdempotencyKey_20ConcurrentRequests_exactlyOnePostingAllResponsesIdentical() throws Exception {
        LEDGER.resetAll();

        UUID userId = UUID.randomUUID();
        String token = JwtTestHelper.tokenFor(userId);

        String source = createWallet(token, userId);
        String destination = createWallet(token, UUID.randomUUID());
        String key = UUID.randomUUID().toString();
        String requestBody = """
                {"sourceWalletId":"%s","destinationWalletId":"%s","amount":"25.00","description":"race"}
                """.formatted(source, destination);

        // A fixed delay widens the window in which concurrent requests can all observe
        // "no record yet" before any of them commits its PENDING insert -- without it, on a
        // fast local machine the whole race could resolve inside a single scheduler tick and
        // never actually exercise the DuplicateKeyException path this test exists to prove.
        LEDGER.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(201).withFixedDelay(300)
                        .withHeader("Content-Type", "application/json")
                        .withBody("""
                                {"transactionId":"11111111-1111-1111-1111-111111111111","type":"TRANSFER","postedAt":"2026-05-19T14:23:00.123Z","entries":[]}
                                """)));

        final int concurrency = 20;
        // Virtual threads (finalized since JDK 21; this project targets 26) are the right fit
        // for 20 blocking MockMvc-to-WireMock round trips -- each parks cheaply while waiting
        // on the stubbed 300ms delay instead of pinning a platform thread. StructuredTaskScope
        // would remove the manual Future bookkeeping below too, but as of this project's JDK it
        // isn't confirmed finalized and the build doesn't pass --enable-preview anywhere; not
        // worth a build-wide toggle for one test.
        ExecutorService pool = Executors.newVirtualThreadPerTaskExecutor();
        CountDownLatch startGate = new CountDownLatch(1);

        List<Callable<MvcResult>> tasks = java.util.stream.IntStream.range(0, concurrency)
                .<Callable<MvcResult>>mapToObj(i -> () -> {
                    startGate.await();
                    return mockMvc.perform(post("/transfers")
                                    .header("Authorization", "Bearer " + token)
                                    .header("Idempotency-Key", key)
                                    .contentType(MediaType.APPLICATION_JSON)
                                    .content(requestBody))
                            .andReturn();
                })
                .collect(Collectors.toList());

        List<Future<MvcResult>> futures = tasks.stream().map(pool::submit).collect(Collectors.toList());
        startGate.countDown();

        List<MvcResult> results = new java.util.ArrayList<>();
        for (Future<MvcResult> future : futures) {
            results.add(future.get(15, TimeUnit.SECONDS));
        }
        pool.shutdown();

        for (MvcResult result : results) {
            assertThat(result.getResponse().getStatus())
                    .as("every one of the %d concurrent requests must eventually return 201, not a stray 409/500", concurrency)
                    .isEqualTo(201);
        }

        List<String> bodies = results.stream()
                .map(r -> {
                    try {
                        return r.getResponse().getContentAsString();
                    } catch (Exception e) {
                        throw new RuntimeException(e);
                    }
                })
                .distinct()
                .collect(Collectors.toList());
        assertThat(bodies)
                .as("all %d responses must be byte-identical (AC-5.12)", concurrency)
                .hasSize(1);

        LEDGER.verify(1, postRequestedFor(urlEqualTo("/ledger/postings")));
    }

    private String createWallet(String token, UUID ownerId) throws Exception {
        String body = mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"currency\":\"BRL\"}"))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        return objectMapper.readTree(body).get("walletId").asText();
    }
}
```

Note: `createWallet(token, ownerId)` takes an explicit `token` parameter (unlike `TransferControllerIT`'s version, which always derives it from `ownerId`) because both wallets in this test must be created and owned correctly while only `userId`'s token is used for the transfer itself — `destination`'s owner is a different random UUID than the caller.

- [ ] Run: `cd services/wallet-service && ./mvnw verify -Dit.test=TransferIdempotencyConcurrencyIT -DfailIfNoTests=true`. **Not** `./mvnw test` — the project's `maven-failsafe-plugin` (which picks up `*IT.java`) is bound to the `integration-test`/`verify` lifecycle phases, not `test`, and takes `-Dit.test`, not Surefire's `-Dtest`. Running `./mvnw test -Dtest=...` against an `*IT.java` class silently runs zero tests and reports success — exactly the false-positive this milestone exists to catch, so get this command right.
- [ ] Expected: `PASS`, `Tests run: 1`. If it fails with wallet-not-found or 403 errors instead of the intended race behavior, double check the `@Transactional(propagation = Propagation.NOT_SUPPORTED)` annotation is present on the test method — that's the symptom of the trap described above. If it reports `Tests run: 0`, the command above was wrong (see the note on `-Dit.test` vs `-Dtest`) — a 0-test "pass" is not a pass.

## Step 2: Confirm it actually exercises the race (not just the happy path)

A test that never hits `DuplicateKeyException` in `IdempotencyService.attemptInsert` isn't proving anything about concurrency — it would pass identically if `/transfers` were single-threaded. Temporarily add a log line (or run with `-Dit.test=TransferIdempotencyConcurrencyIT -Dspring-boot.run.arguments=--logging.level.com.ledger.wallet=DEBUG`) and confirm in the output that `awaitCompletion` is actually invoked — i.e., that more than one thread reached `attemptInsert` before any of them completed. Remove any temporary logging before committing.

- [ ] Confirm via logs or a temporary counter that at least one of the 20 requests took the `DuplicateKeyException → awaitCompletion` path, not just the `New` path.

## Step 3: Run alongside the rest of the wallet-service suite, then commit

- [ ] Run: `cd services/wallet-service && ./mvnw verify` — confirm no regressions and no test-ordering flakiness introduced by the non-transactional test (it commits real rows; other tests use random UUIDs per M3's existing convention, so this should be safe). `verify` runs the full unit + integration suite, unlike plain `test`.
- [ ] Commit:

```bash
git add services/wallet-service/src/test/java/com/ledger/wallet/api/controller/TransferIdempotencyConcurrencyIT.java
git commit -m "test: add TST-CONCURRENCY-4, idempotency under 20 concurrent identical requests"
```

## Acceptance criteria

| Check | Expected |
|---|---|
| TST-CONCURRENCY-4 | all 20 responses are `201` with byte-identical bodies; exactly 1 `POST /ledger/postings` observed by WireMock |
| Non-transactional isolation | test data uses random UUIDs (`userId`, wallet owners) so a committed, non-rolled-back run doesn't collide with other tests |
| `./mvnw verify` (full suite) | still green |

## Done when

`TransferIdempotencyConcurrencyIT` passes reliably across repeated local runs (run it 3–5 times before moving on — this is exactly the kind of test that can pass by luck once and flake on ordering the next time).

## Notes

- If repeated runs show the test *sometimes* returns a `409 IN_PROGRESS` instead of `201` for a straggler request, that's not necessarily a bug — it's AC-5.16's documented behavior when a duplicate waits longer than 5 seconds for the original to complete. Widen `startGate`/thread-pool sizing or check whether the WireMock delay is too large relative to `IdempotencyService`'s `DEFAULT_MAX_WAIT` (5s) before assuming the assertion itself is wrong; a 300ms stub delay should leave enormous headroom under that 5s ceiling.
- Keep the `withFixedDelay(300)` on the WireMock stub — without it, the 20 requests may resolve too fast on a fast machine to reliably hit the `DuplicateKeyException` race path Step 2 asks you to confirm.
