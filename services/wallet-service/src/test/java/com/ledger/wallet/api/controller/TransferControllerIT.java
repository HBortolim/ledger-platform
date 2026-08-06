package com.ledger.wallet.api.controller;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.http.Fault;
import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;

import java.util.UUID;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

// TST-INT-1's Wallet-Service half (overview decision #5): WireMock stands in for the Ledger
// Service; "ledger rows exist" is TST-E2E-1's job against the real compose stack.
class TransferControllerIT extends BaseIntegrationTest {

    private static final WireMockServer LEDGER = new WireMockServer(0);

    static {
        LEDGER.start();
    }

    @DynamicPropertySource
    static void ledgerServiceUrl(DynamicPropertyRegistry registry) {
        registry.add("ledger.service.url", LEDGER::baseUrl);
    }

    @BeforeEach
    void setUp() {
        LEDGER.resetAll();
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

    private void stubPosted(String transactionId) {
        LEDGER.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(201).withHeader("Content-Type", "application/json")
                        .withBody("""
                                {"transactionId":"%s","type":"TRANSFER","postedAt":"2026-05-19T14:23:00.123Z","entries":[]}
                                """.formatted(transactionId))));
    }

    private String transferBody(String source, String destination, String amount) {
        return """
                {"sourceWalletId":"%s","destinationWalletId":"%s","amount":"%s","description":"rent"}
                """.formatted(source, destination, amount);
    }

    @Test
    void happyPath_returns201WithCompletedStatus() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        stubPosted("11111111-1111-1111-1111-111111111111");

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "100.00")))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.status").value("COMPLETED"))
                .andExpect(jsonPath("$.transferId").exists())
                .andExpect(jsonPath("$.postedAt").exists());
    }

    @Test
    void idempotentReplay_sameKey_returnsByteIdenticalResponseAndPostsOnce() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        String key = UUID.randomUUID().toString();
        stubPosted("22222222-2222-2222-2222-222222222222");
        String requestBody = transferBody(source, destination, "100.00");

        String first = mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();

        String second = mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();

        assertThat(second).isEqualTo(first);
        LEDGER.verify(1, postRequestedFor(urlEqualTo("/ledger/postings")));
    }

    @Test
    void sameKeyDifferentBody_returns422IdempotencyKeyMismatch() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        String key = UUID.randomUUID().toString();
        stubPosted("33333333-3333-3333-3333-333333333333");

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "100.00")))
                .andExpect(status().isCreated());

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "999.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("IDEMPOTENCY_KEY_MISMATCH"));
    }

    @Test
    void amountWithOneDecimalPlace_returns422InvalidAmount() throws Exception {
        UUID userId = UUID.randomUUID();

        // @Valid runs before the use case does, so the wallets never need to exist.
        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(UUID.randomUUID().toString(), UUID.randomUUID().toString(), "10.5")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("INVALID_AMOUNT"));
    }

    @Test
    void amountWithNoDecimalPlaces_returns422InvalidAmount() throws Exception {
        UUID userId = UUID.randomUUID();

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(UUID.randomUUID().toString(), UUID.randomUUID().toString(), "100")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("INVALID_AMOUNT"));
    }

    @Test
    void selfTransfer_returns422() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, source, "10.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("SELF_TRANSFER"));
    }

    @Test
    void nonOwnerSourceWallet_returns403() throws Exception {
        UUID owner = UUID.randomUUID();
        UUID attacker = UUID.randomUUID();
        String source = createWallet(owner);
        String destination = createWallet(UUID.randomUUID());

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(attacker))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "10.00")))
                .andExpect(status().isForbidden());
    }

    @Test
    void insufficientFunds_returns422PassedThroughFromLedger() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        LEDGER.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(422).withHeader("Content-Type", "application/json")
                        .withBody("{\"code\":\"INSUFFICIENT_FUNDS\",\"message\":\"insufficient balance\"}")));

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "50000.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("INSUFFICIENT_FUNDS"));
    }

    @Test
    void ledgerServiceDown_returns503WithRetryAfter() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        // Simulate the Ledger Service being unreachable without stopping the shared WireMock
        // instance: LEDGER.stop() would force a rebind to a new random port on the next start(),
        // permanently orphaning the RestClient bean Spring already built with the old base URL.
        LEDGER.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withFault(Fault.CONNECTION_RESET_BY_PEER)));

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "10.00")))
                .andExpect(status().isServiceUnavailable())
                .andExpect(header().string("Retry-After", "2"));
    }

    @Test
    void noIdempotencyKey_stillSucceeds() throws Exception {
        UUID userId = UUID.randomUUID();
        String source = createWallet(userId);
        String destination = createWallet(UUID.randomUUID());
        stubPosted("44444444-4444-4444-4444-444444444444");

        mockMvc.perform(post("/transfers")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(userId))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(transferBody(source, destination, "10.00")))
                .andExpect(status().isCreated());
    }
}
