package com.ledger.wallet.infrastructure.ledger;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.client.WireMock;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.client.RestClient;

import java.math.BigDecimal;
import java.net.ServerSocket;
import java.time.Duration;
import java.util.List;
import java.util.UUID;

import static com.github.tomakehurst.wiremock.client.WireMock.*;
import static org.junit.jupiter.api.Assertions.*;

class LedgerClientTest {

    private WireMockServer wireMock;
    private LedgerClient client;

    @BeforeEach
    void setUp() {
        wireMock = new WireMockServer(0);
        wireMock.start();
        client = newClient(wireMock.baseUrl(), Duration.ofSeconds(2), Duration.ofSeconds(5));
    }

    @AfterEach
    void tearDown() {
        wireMock.stop();
    }

    private static LedgerClient newClient(String baseUrl, Duration connectTimeout, Duration readTimeout) {
        RestClient restClient = LedgerClientConfig.buildRestClient(baseUrl, connectTimeout, readTimeout);
        return new LedgerClient(restClient);
    }

    @Test
    void postPosting_returns_Posted_on_201() {
        UUID txId = UUID.randomUUID();
        wireMock.stubFor(post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(201).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {
                                  "transactionId": "%s",
                                  "type": "TRANSFER",
                                  "postedAt": "2026-05-19T14:23:00.123Z",
                                  "entries": [
                                    {"entryId":"11111111-1111-1111-1111-111111111111","accountId":"22222222-2222-2222-2222-222222222222","entryType":"DEBIT","amount":"100.00"},
                                    {"entryId":"33333333-3333-3333-3333-333333333333","accountId":"44444444-4444-4444-4444-444444444444","entryType":"CREDIT","amount":"100.00"}
                                  ]
                                }
                                """.formatted(txId)))
        );

        PostPostingResult result = client.postPosting(txId, "TRANSFER", "demo", List.of(
                new LedgerEntryInstruction(UUID.fromString("22222222-2222-2222-2222-222222222222"), LedgerEntryType.DEBIT, new BigDecimal("100.00")),
                new LedgerEntryInstruction(UUID.fromString("44444444-4444-4444-4444-444444444444"), LedgerEntryType.CREDIT, new BigDecimal("100.00"))
        ));

        assertInstanceOf(PostPostingResult.Posted.class, result);
        PostPostingResult.Posted posted = (PostPostingResult.Posted) result;
        assertEquals(2, posted.entries().size());
        assertNotNull(posted.postedAt());
    }

    @Test
    void postPosting_returns_AlreadyPosted_on_409() {
        UUID txId = UUID.randomUUID();
        wireMock.stubFor(post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(409).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {
                                  "transactionId": "%s",
                                  "type": "TRANSFER",
                                  "postedAt": "2026-05-19T14:23:00.123Z",
                                  "entries": []
                                }
                                """.formatted(txId)))
        );

        PostPostingResult result = client.postPosting(txId, "TRANSFER", null, List.of(
                new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.DEBIT, new BigDecimal("50.00")),
                new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.CREDIT, new BigDecimal("50.00"))
        ));

        assertInstanceOf(PostPostingResult.AlreadyPosted.class, result);
        PostPostingResult.AlreadyPosted already = (PostPostingResult.AlreadyPosted) result;
        assertEquals(txId, already.original().transactionId());
    }

    @Test
    void postPosting_returns_Rejected_on_422() {
        wireMock.stubFor(post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(422).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {"code": "INSUFFICIENT_FUNDS", "message": "account has insufficient funds"}
                                """))
        );

        PostPostingResult result = client.postPosting(UUID.randomUUID(), "TRANSFER", null, List.of(
                new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.DEBIT, new BigDecimal("50.00")),
                new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.CREDIT, new BigDecimal("50.00"))
        ));

        assertInstanceOf(PostPostingResult.Rejected.class, result);
        PostPostingResult.Rejected rejected = (PostPostingResult.Rejected) result;
        assertEquals("INSUFFICIENT_FUNDS", rejected.code());
    }

    @Test
    void postPosting_serializes_amount_as_twoDecimalPlaceString_not_a_raw_number() {
        wireMock.stubFor(post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(201).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {"transactionId":"%s","type":"TRANSFER","postedAt":"2026-05-19T14:23:00.123Z","entries":[]}
                                """.formatted(UUID.randomUUID())))
        );

        client.postPosting(UUID.randomUUID(), "TRANSFER", null, List.of(
                new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.DEBIT, new BigDecimal("100.00")),
                new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.CREDIT, new BigDecimal("100.00"))
        ));

        wireMock.verify(postRequestedFor(urlEqualTo("/ledger/postings"))
                .withRequestBody(matchingJsonPath("$.entries[0].amount", equalTo("100.00"))));
    }

    @Test
    void postPosting_throws_LedgerUnavailable_when_connection_refused() throws Exception {
        int closedPort;
        try (ServerSocket socket = new ServerSocket(0)) {
            closedPort = socket.getLocalPort();
        } // closed immediately: nothing listens on this port now

        LedgerClient unreachable = newClient("http://localhost:" + closedPort, Duration.ofSeconds(1), Duration.ofSeconds(1));

        assertThrows(LedgerUnavailableException.class, () -> unreachable.postPosting(
                UUID.randomUUID(), "TRANSFER", null, List.of(
                        new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.DEBIT, new BigDecimal("1.00")),
                        new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.CREDIT, new BigDecimal("1.00"))
                )));
    }

    @Test
    void postPosting_throws_LedgerUnavailable_when_response_exceeds_read_timeout() {
        wireMock.stubFor(post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(201).withFixedDelay(2000)));
        LedgerClient shortTimeoutClient = newClient(wireMock.baseUrl(), Duration.ofSeconds(1), Duration.ofMillis(200));

        assertThrows(LedgerUnavailableException.class, () -> shortTimeoutClient.postPosting(
                UUID.randomUUID(), "TRANSFER", null, List.of(
                        new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.DEBIT, new BigDecimal("1.00")),
                        new LedgerEntryInstruction(UUID.randomUUID(), LedgerEntryType.CREDIT, new BigDecimal("1.00"))
                )));
    }

    @Test
    void getTransaction_returns_present_on_200() {
        UUID txId = UUID.randomUUID();
        wireMock.stubFor(WireMock.get(urlEqualTo("/admin/ledger/transactions/" + txId))
                .willReturn(aResponse().withStatus(200).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {"transactionId":"%s","type":"TRANSFER","postedAt":"2026-05-19T14:23:00.123Z","entries":[]}
                                """.formatted(txId)))
        );

        var result = client.getTransaction(txId);

        assertTrue(result.isPresent());
        assertEquals(txId, result.get().transactionId());
    }

    @Test
    void getTransaction_returns_empty_on_404() {
        UUID txId = UUID.randomUUID();
        wireMock.stubFor(WireMock.get(urlEqualTo("/admin/ledger/transactions/" + txId))
                .willReturn(aResponse().withStatus(404)));

        var result = client.getTransaction(txId);

        assertTrue(result.isEmpty());
    }

    @Test
    void getTransaction_throws_LedgerUnavailable_when_connection_refused() throws Exception {
        int closedPort;
        try (ServerSocket socket = new ServerSocket(0)) {
            closedPort = socket.getLocalPort();
        }
        LedgerClient unreachable = newClient("http://localhost:" + closedPort, Duration.ofSeconds(1), Duration.ofSeconds(1));

        assertThrows(LedgerUnavailableException.class, () -> unreachable.getTransaction(UUID.randomUUID()));
    }
}
