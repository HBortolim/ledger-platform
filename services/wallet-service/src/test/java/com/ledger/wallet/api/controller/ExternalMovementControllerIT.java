package com.ledger.wallet.api.controller;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.ledger.wallet.support.BaseIntegrationTest;
import com.ledger.wallet.support.JwtTestHelper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;

import java.util.UUID;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalToJson;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.header;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

// TST-INT-1's Wallet-Service half for FR-6/FR-7: WireMock stands in for the Ledger Service.
class ExternalMovementControllerIT extends BaseIntegrationTest {

    private static final String SYSTEM_ACCOUNT_ID = "00000000-0000-0000-0000-000000000001";
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
                                {"transactionId":"%s","type":"DEPOSIT","postedAt":"2026-05-19T14:23:00.123Z","entries":[]}
                                """.formatted(transactionId))));
    }

    private String depositBody(String walletId, String amount) {
        return """
                {"destinationWalletId":"%s","amount":"%s","description":"top-up"}
                """.formatted(walletId, amount);
    }

    private String withdrawalBody(String walletId, String amount) {
        return """
                {"sourceWalletId":"%s","amount":"%s","description":"cash-out"}
                """.formatted(walletId, amount);
    }

    @Test
    void adminDeposit_returns201WithCompletedStatusAndSystemAccountDebit() throws Exception {
        String wallet = createWallet(UUID.randomUUID());
        stubPosted("11111111-1111-1111-1111-111111111111");

        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + JwtTestHelper.adminTokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(wallet, "100.00")))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.status").value("COMPLETED"))
                .andExpect(jsonPath("$.transactionId").exists());

        LEDGER.verify(postRequestedFor(urlEqualTo("/ledger/postings"))
                .withRequestBody(equalToJson("""
                        {"type":"DEPOSIT","entries":[
                          {"accountId":"%s","entryType":"DEBIT","amount":"100.00"},
                          {"accountId":"%s","entryType":"CREDIT","amount":"100.00"}
                        ]}
                        """.formatted(SYSTEM_ACCOUNT_ID, wallet), true, true)));
    }

    @Test
    void adminWithdrawal_returns201WithSystemAccountCredit() throws Exception {
        String wallet = createWallet(UUID.randomUUID());
        stubPosted("22222222-2222-2222-2222-222222222222");

        mockMvc.perform(post("/withdrawals")
                        .header("Authorization", "Bearer " + JwtTestHelper.adminTokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(withdrawalBody(wallet, "40.00")))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.status").value("COMPLETED"));

        LEDGER.verify(postRequestedFor(urlEqualTo("/ledger/postings"))
                .withRequestBody(equalToJson("""
                        {"type":"WITHDRAWAL","entries":[
                          {"accountId":"%s","entryType":"DEBIT","amount":"40.00"},
                          {"accountId":"%s","entryType":"CREDIT","amount":"40.00"}
                        ]}
                        """.formatted(wallet, SYSTEM_ACCOUNT_ID), true, true)));
    }

    @Test
    void idempotentReplay_sameKey_returnsByteIdenticalResponseAndPostsOnce() throws Exception {
        String wallet = createWallet(UUID.randomUUID());
        String key = UUID.randomUUID().toString();
        // Idempotency records are scoped by (userId, key) -- both calls must be the same admin.
        String adminToken = JwtTestHelper.adminTokenFor(UUID.randomUUID());
        stubPosted("33333333-3333-3333-3333-333333333333");
        String requestBody = depositBody(wallet, "100.00");

        String first = mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + adminToken)
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();

        String second = mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + adminToken)
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();

        org.assertj.core.api.Assertions.assertThat(second).isEqualTo(first);
        LEDGER.verify(1, postRequestedFor(urlEqualTo("/ledger/postings")));
    }

    @Test
    void sameKeyDifferentAmount_returns422IdempotencyKeyMismatch() throws Exception {
        String wallet = createWallet(UUID.randomUUID());
        String key = UUID.randomUUID().toString();
        String adminToken = JwtTestHelper.adminTokenFor(UUID.randomUUID());
        stubPosted("44444444-4444-4444-4444-444444444444");

        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + adminToken)
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(wallet, "100.00")))
                .andExpect(status().isCreated());

        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + adminToken)
                        .header("Idempotency-Key", key)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(wallet, "999.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("IDEMPOTENCY_KEY_MISMATCH"));
    }

    @Test
    void nonAdminRole_returns403OnDepositsAndWithdrawals() throws Exception {
        String wallet = createWallet(UUID.randomUUID());

        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(wallet, "10.00")))
                .andExpect(status().isForbidden());

        mockMvc.perform(post("/withdrawals")
                        .header("Authorization", "Bearer " + JwtTestHelper.tokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(withdrawalBody(wallet, "10.00")))
                .andExpect(status().isForbidden());
    }

    @Test
    void missingToken_returns401OnDepositsAndWithdrawals() throws Exception {
        String wallet = createWallet(UUID.randomUUID());

        mockMvc.perform(post("/deposits")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(wallet, "10.00")))
                .andExpect(status().isUnauthorized());

        mockMvc.perform(post("/withdrawals")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(withdrawalBody(wallet, "10.00")))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void withdrawal_insufficientFunds_returns422PassedThroughFromLedger() throws Exception {
        String wallet = createWallet(UUID.randomUUID());
        LEDGER.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withStatus(422).withHeader("Content-Type", "application/json")
                        .withBody("{\"code\":\"INSUFFICIENT_FUNDS\",\"message\":\"insufficient balance\"}")));

        mockMvc.perform(post("/withdrawals")
                        .header("Authorization", "Bearer " + JwtTestHelper.adminTokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(withdrawalBody(wallet, "50000.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("INSUFFICIENT_FUNDS"));
    }

    @Test
    void nonExistentDestinationWallet_returns422WalletNotActiveNot403() throws Exception {
        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + JwtTestHelper.adminTokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(UUID.randomUUID().toString(), "10.00")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("WALLET_NOT_ACTIVE"));
    }

    @Test
    void amountWithOneDecimalPlace_returns422InvalidAmount() throws Exception {
        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + JwtTestHelper.adminTokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(UUID.randomUUID().toString(), "10.5")))
                .andExpect(status().isUnprocessableContent())
                .andExpect(jsonPath("$.errors[0].code").value("INVALID_AMOUNT"));
    }

    @Test
    void ledgerServiceDown_returns503WithRetryAfter() throws Exception {
        String wallet = createWallet(UUID.randomUUID());
        LEDGER.stubFor(com.github.tomakehurst.wiremock.client.WireMock.post(urlEqualTo("/ledger/postings"))
                .willReturn(aResponse().withFault(com.github.tomakehurst.wiremock.http.Fault.CONNECTION_RESET_BY_PEER)));

        mockMvc.perform(post("/deposits")
                        .header("Authorization", "Bearer " + JwtTestHelper.adminTokenFor(UUID.randomUUID()))
                        .header("Idempotency-Key", UUID.randomUUID().toString())
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(depositBody(wallet, "10.00")))
                .andExpect(status().isServiceUnavailable())
                .andExpect(header().string("Retry-After", "2"));
    }
}
