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

        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
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
        CountDownLatch startGate = new CountDownLatch(1);
        List<MvcResult> results = new java.util.ArrayList<>();

        try (ExecutorService pool = Executors.newVirtualThreadPerTaskExecutor()) {
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

            for (Future<MvcResult> future : futures) {
                results.add(future.get(15, TimeUnit.SECONDS));
            }
        }

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

    private String createWallet(UUID ownerId) throws Exception {
        String body = mockMvc.perform(post("/wallets")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(ownerId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"currency\":\"BRL\"}"))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        return objectMapper.readTree(body).get("walletId").asText();
    }
}
